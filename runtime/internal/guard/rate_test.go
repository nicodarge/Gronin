package guard_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
	"github.com/nicodarge/Gronin/runtime/internal/guard/guardtest"
	"github.com/nicodarge/Gronin/runtime/internal/playbook"
	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// rated is a playbook limited to runs triggers within per, per FR-114.
func rated(t *testing.T, name string, runs int, per string) *playbook.Playbook {
	t.Helper()
	book := waitable(t, name, "")
	book.Guard = &playbook.Guard{Rate: &playbook.Rate{Runs: runs, Per: per}}
	return book
}

// SC-107. A limit of N runs per window, triggered N+2 times inside it, produces N runs
// and two rate_limited refusals naming the limit, and neither refused trigger enters the
// waiting slot; once the window moves past the earlier runs, a trigger runs again; with no
// limit declared, none is refused for rate and non-concurrency still holds; a trigger
// waiting while other triggers fill the window is refused for rate when the claim frees
// (FR-124); and the window is the deployment's, not each host's.
func TestTheRateLimitBoundsRunsAcrossTheDeployment(t *testing.T) {
	t.Run("bounds runs and releases them when the window moves", func(t *testing.T) {
		backend := guardtest.NewClock(start)
		runtime := guardtest.NewClock(start)
		fake := guardtest.NewFake(backend, runtime)
		book := rated(t, "drift-check", 2, "1m")
		g, store := waitingGuard(t, fake.Host(nil), runtime, t.TempDir(), "instance-a", book)

		for i := range 2 {
			admitted, err := g.Admit(t.Context(), book, guard.Request{
				RunID: fmt.Sprintf("run-%d", i), Kind: record.TriggerManual,
			})
			if err != nil {
				t.Fatalf("run %d was refused: %v", i, err)
			}
			if err := admitted.Claim.Release(t.Context()); err != nil {
				t.Fatal(err)
			}
		}

		for i := 2; i < 4; i++ {
			admitted, err := g.Admit(t.Context(), book, guard.Request{
				RunID: fmt.Sprintf("run-%d", i), Kind: record.TriggerManual,
				OnWait: func(guard.Waiting) { t.Errorf("trigger %d, refused for rate, waited", i) },
			})
			if !refusedWith(err, record.MechanismRateLimited) || admitted != nil {
				t.Fatalf("trigger %d was not refused for rate: admitted %v, err %v", i, admitted, err)
			}
		}
		if waiting, err := store.StillWaiting(t.Context()); err != nil || len(waiting) != 0 {
			t.Fatalf("a trigger refused for rate entered the waiting slot: %+v, err %v", waiting, err)
		}

		refusals, err := store.ListRefusals(t.Context(), 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(refusals) != 2 {
			t.Fatalf("refusals = %+v, want 2", refusals)
		}
		for _, refusal := range refusals {
			if refusal.Mechanism != record.MechanismRateLimited || refusal.Detail == "" {
				t.Fatalf("a rate refusal reads as %+v, want the limit named", refusal)
			}
		}

		// The window has moved past both runs: a further trigger runs.
		backend.Advance(time.Minute + time.Second)
		admitted, err := g.Admit(t.Context(), book, guard.Request{RunID: "run-later", Kind: record.TriggerManual})
		if err != nil {
			t.Fatalf("a trigger once the window moved was refused: %v", err)
		}
		if err := admitted.Claim.Release(t.Context()); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("no limit declared refuses nothing for rate, and non-concurrency holds", func(t *testing.T) {
		backend := guardtest.NewClock(start)
		runtime := guardtest.NewClock(start)
		fake := guardtest.NewFake(backend, runtime)
		book := waitable(t, "drift-check", "")
		g, _ := waitingGuard(t, fake.Host(nil), runtime, t.TempDir(), "instance-a", book)

		for i := range 5 {
			admitted, err := g.Admit(t.Context(), book, guard.Request{
				RunID: fmt.Sprintf("run-%d", i), Kind: record.TriggerManual,
			})
			if err != nil {
				t.Fatalf("run %d was refused with no rate limit declared: %v", i, err)
			}
			if err := admitted.Claim.Release(t.Context()); err != nil {
				t.Fatal(err)
			}
		}

		held, err := g.Admit(t.Context(), book, guard.Request{RunID: "run-holder", Kind: record.TriggerManual})
		if err != nil {
			t.Fatal(err)
		}
		// A scheduled trigger is discarded rather than made to wait, so this checks
		// FR-101 without touching the waiting slot.
		_, err = g.Admit(t.Context(), book, guard.Request{RunID: "run-collide", Kind: record.TriggerSchedule, DueAt: start})
		if !refusedWith(err, record.MechanismClaimHeld) {
			t.Fatalf("a second trigger was admitted while the claim was held: %v", err)
		}
		if err := held.Claim.Release(t.Context()); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("the window is shared across hosts", func(t *testing.T) {
		backend := guardtest.NewClock(start)
		runtime := guardtest.NewClock(start)
		fake := guardtest.NewFake(backend, runtime)
		book := rated(t, "drift-check", 1, "1m")

		a, _ := waitingGuard(t, fake.Host(nil), runtime, t.TempDir(), "instance-a", book)
		b, _ := waitingGuard(t, fake.Host(nil), runtime, t.TempDir(), "instance-b", book)

		admitted, err := a.Admit(t.Context(), book, guard.Request{RunID: "run-a", Kind: record.TriggerManual})
		if err != nil {
			t.Fatalf("host A's run was refused: %v", err)
		}
		if err := admitted.Claim.Release(t.Context()); err != nil {
			t.Fatal(err)
		}

		admitted, err = b.Admit(t.Context(), book, guard.Request{RunID: "run-b", Kind: record.TriggerManual})
		if !refusedWith(err, record.MechanismRateLimited) || admitted != nil {
			t.Fatalf("host B was not refused for a window host A had already filled: admitted %v, err %v", admitted, err)
		}
	})

	t.Run("a waiting trigger is judged against the limit again when it tries to run", func(t *testing.T) {
		// FR-124: the window bounds runs, not acceptances. The run this trigger waits
		// behind takes the sole rate slot, which does not free on release (C9), so the
		// window is still full when the claim frees and the trigger tries to run.
		stateDir := t.TempDir()
		backend := guardtest.NewClock(start)
		runtime := guardtest.NewClock(start)
		fake := guardtest.NewFake(backend, runtime)
		book := rated(t, "drift-check", 1, "1m")

		holder, _ := waitingGuard(t, fake.Host(nil), runtime, stateDir, "instance-holder", book)
		held, err := holder.Admit(t.Context(), book, guard.Request{RunID: "run-held", Kind: record.TriggerManual})
		if err != nil {
			t.Fatal(err)
		}

		waiter, store := waitingGuard(t, fake.Host(nil), runtime, stateDir, "instance-waiter", book)
		waiting, done := admitInBackground(t, waiter, book, "run-waiter")
		accepted := startedWaiting(t, waiting, done)

		if err := held.Claim.Release(t.Context()); err != nil {
			t.Fatal(err)
		}
		result := ended(t, done, 10*time.Second)
		if !refusedWith(result.err, record.MechanismRateLimited) || result.admitted != nil {
			t.Fatalf("a waiting trigger that met a full window was not refused for rate: admitted %v, err %v",
				result.admitted, result.err)
		}
		assertEndedAs(t, store, accepted.TriggerID, record.WaitRateLimited)
	})

	t.Run("a waiting trigger is discarded when an independent admission fills the remaining capacity", func(t *testing.T) {
		// A limit of two: one slot is taken and released by a trigger that has nothing to
		// do with the one that waits or the one it collides with, before either of them is
		// admitted at all. By the time the run it waits behind frees the claim, that
		// independent admission and the run together have already filled the window, so
		// the wait ends discarded rather than run — FR-124, the window bounds runs and not
		// only the run a wait happened to collide with.
		stateDir := t.TempDir()
		backend := guardtest.NewClock(start)
		runtime := guardtest.NewClock(start)
		fake := guardtest.NewFake(backend, runtime)
		book := rated(t, "drift-check", 2, "1m")

		other, _ := waitingGuard(t, fake.Host(nil), runtime, t.TempDir(), "instance-other", book)
		independent, err := other.Admit(t.Context(), book, guard.Request{RunID: "run-other", Kind: record.TriggerManual})
		if err != nil {
			t.Fatalf("the independent admission was refused: %v", err)
		}
		if err := independent.Claim.Release(t.Context()); err != nil {
			t.Fatal(err)
		}

		holder, _ := waitingGuard(t, fake.Host(nil), runtime, stateDir, "instance-holder", book)
		held, err := holder.Admit(t.Context(), book, guard.Request{RunID: "run-held", Kind: record.TriggerManual})
		if err != nil {
			t.Fatal(err)
		}

		waiter, store := waitingGuard(t, fake.Host(nil), runtime, stateDir, "instance-waiter", book)
		waiting, done := admitInBackground(t, waiter, book, "run-waiter")
		accepted := startedWaiting(t, waiting, done)

		if err := held.Claim.Release(t.Context()); err != nil {
			t.Fatal(err)
		}
		result := ended(t, done, 10*time.Second)
		if !refusedWith(result.err, record.MechanismRateLimited) || result.admitted != nil {
			t.Fatalf("a waiting trigger was not refused for rate once an independent admission had filled "+
				"the remaining capacity: admitted %v, err %v", result.admitted, result.err)
		}
		assertEndedAs(t, store, accepted.TriggerID, record.WaitRateLimited)
	})
}
