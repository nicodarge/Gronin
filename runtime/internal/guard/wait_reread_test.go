package guard_test

import (
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
	"github.com/nicodarge/Gronin/runtime/internal/guard/guardtest"
	"github.com/nicodarge/Gronin/runtime/internal/playbook"
	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// FR-110, User Story 2's scenario 5 and C10. A waiting trigger reads its file again once the
// claim frees, and a scheduled tick arrives from another process while it does. The waiter
// has not asked for the claim yet, so the tick takes it: a waiter holding the claim through
// its read would refuse the tick as held, naming a run that may never exist, and a tick does
// not wait.
func TestATickDuringTheRereadIsNotRefusedByTheWaiter(t *testing.T) {
	stateDir := t.TempDir()
	fake := guardtest.NewFake(guardtest.NewClock(start), guardtest.NewClock(start))
	runtime := guardtest.NewClock(start)
	drift := waitable(t, "drift-check", "")

	holder, store := waitingGuard(t, fake.Host(nil), runtime, stateDir, "instance-holder", drift)
	held, err := holder.Admit(t.Context(), drift, guard.Request{RunID: "run-held", Kind: record.TriggerManual})
	if err != nil {
		t.Fatal(err)
	}
	serving, _ := waitingGuard(t, fake.Host(nil), runtime, stateDir, "instance-serve", drift)
	waiter, _ := waitingGuard(t, fake.Host(nil), runtime, stateDir, "instance-waiter", drift)

	var (
		tick    *guard.Admitted
		tickErr error
		reread  = make(chan struct{})
		fired   bool
	)
	waiter.Slot.Reload = func(string) (*playbook.Playbook, error) {
		if !fired {
			fired = true
			tick, tickErr = serving.Admit(t.Context(), drift, guard.Request{
				RunID: "run-tick", Kind: record.TriggerSchedule, DueAt: start,
			})
			close(reread)
		}
		return drift, nil
	}
	waiting, done := admitInBackground(t, waiter, drift, "run-waiter")
	startedWaiting(t, waiting, done)

	if err := held.Claim.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-reread:
	case result := <-done:
		t.Fatalf("the waiter ended without reading its file again: %v", result.err)
	case <-time.After(10 * time.Second):
		t.Fatal("the waiter never read its file again")
	}
	if tickErr != nil {
		t.Fatalf("a tick arriving while the waiter read its file was refused: %v", tickErr)
	}

	// The tick's run ends; the waiter, which found the claim taken, runs after it.
	if err := tick.Claim.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if result := ended(t, done, 10*time.Second); result.err != nil {
		t.Fatalf("the waiter did not run once the tick's run ended: %v", result.err)
	}
	refusals, err := store.ListRefusals(t.Context(), 10)
	if err != nil || len(refusals) != 0 {
		t.Fatalf("refusals = %+v, err = %v, want none", refusals, err)
	}
}
