package guard_test

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
	"github.com/nicodarge/Gronin/runtime/internal/guard/guardtest"
	"github.com/nicodarge/Gronin/runtime/internal/playbook"
	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// waitable is a playbook read from a file, as a waiting trigger's has to be: it is read
// again from there when the trigger runs (FR-121). wait is its guard.wait, or empty.
func waitable(name, wait string) *playbook.Playbook {
	book := &playbook.Playbook{Name: name, Path: "/srv/gronin/playbooks/" + name + ".yaml"}
	if wait != "" {
		book.Guard = &playbook.Guard{Wait: wait}
	}
	return book
}

// waitingGuard is one process on stateDir: its own record store on the shared directory,
// its own instance lock, and a waiting slot whose playbook file still declares book.
func waitingGuard(
	t *testing.T, host guard.Coordinator, clock guard.Clock, stateDir, instance string,
	book *playbook.Playbook,
) (*guard.Guard, *record.Store) {
	t.Helper()
	store, err := record.Open(t.Context(), filepath.Join(stateDir, "record"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	held, err := guard.HoldInstance(stateDir, instance)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	return &guard.Guard{
		Coordinator: host, Store: store, Config: testConfig(), Clock: clock,
		Host: "host-a.example.com", Instance: instance,
		Slot: &guard.WaitSlot{
			Instance: held,
			Reload:   func(string) (*playbook.Playbook, error) { return book, nil },
		},
	}, store
}

type admission struct {
	admitted *guard.Admitted
	err      error
}

// admitInBackground asks for a manual run that may wait, and reports when it starts to.
func admitInBackground(
	t *testing.T, g *guard.Guard, book *playbook.Playbook, runID string,
) (<-chan guard.Waiting, <-chan admission) {
	t.Helper()
	waiting := make(chan guard.Waiting, 1)
	done := make(chan admission, 1)
	go func() {
		admitted, err := g.Admit(t.Context(), book, guard.Request{
			RunID: runID, Kind: record.TriggerManual,
			OnWait: func(w guard.Waiting) { waiting <- w },
		})
		done <- admission{admitted, err}
	}()
	return waiting, done
}

// startedWaiting returns once the trigger says it is waiting, and fails if it ended first.
func startedWaiting(t *testing.T, waiting <-chan guard.Waiting, done <-chan admission) guard.Waiting {
	t.Helper()
	select {
	case w := <-waiting:
		return w
	case ended := <-done:
		t.Fatalf("the trigger ended without waiting: admitted %v, err %v", ended.admitted, ended.err)
	case <-time.After(10 * time.Second):
		t.Fatal("the trigger neither waited nor ended")
	}
	return guard.Waiting{}
}

// ended returns how a waiting trigger ended, and fails if it has not within the bound — a
// watchdog of the test's own, since go test's timeout would take ten minutes to notice.
func ended(t *testing.T, done <-chan admission, within time.Duration) admission {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(within):
		t.Fatalf("the waiting trigger had not ended %s later", within)
	}
	return admission{}
}

// SC-105, FR-110, FR-111 and FR-102. During one run the playbook's tick fires, then three
// manual invocations arrive, each a process of its own on the state directory. Counts cannot
// tell a correct slot from one the tick occupies — both give one run and three refusals — so
// what each record and the run refer to is read.
func TestOneDeep(t *testing.T) {
	stateDir := t.TempDir()
	fake := guardtest.NewFake(guardtest.NewClock(start), guardtest.NewClock(start))
	runtime := guardtest.NewClock(start)
	drift := waitable("drift-check", "")

	holder, store := waitingGuard(t, fake.Host(nil), runtime, stateDir, "instance-holder", drift)
	held, err := holder.Admit(t.Context(), drift, guard.Request{RunID: "run-held", Kind: record.TriggerManual})
	if err != nil {
		t.Fatalf("the run could not take the claim: %v", err)
	}

	// The tick comes first, so the slot it must leave free is empty when it arrives.
	serving, _ := waitingGuard(t, fake.Host(nil), runtime, stateDir, "instance-serve", drift)
	tick, err := serving.Admit(t.Context(), drift, guard.Request{
		RunID: "run-tick", Kind: record.TriggerSchedule, DueAt: start,
		OnWait: func(guard.Waiting) { t.Error("the scheduled tick waited") },
	})
	if !refusedWith(err, record.MechanismClaimHeld) || tick != nil {
		t.Fatalf("the tick was not refused as held: admitted %v, err %v", tick, err)
	}
	if waiting, err := store.StillWaiting(t.Context()); err != nil || len(waiting) != 0 {
		t.Fatalf("the tick left the slot occupied: %+v, err %v", waiting, err)
	}

	first, _ := waitingGuard(t, fake.Host(nil), runtime, stateDir, "instance-1", drift)
	waiting, done := admitInBackground(t, first, drift, "run-1")
	accepted := startedWaiting(t, waiting, done)

	for at := 2; at <= 3; at++ {
		another, _ := waitingGuard(t, fake.Host(nil), runtime, stateDir, fmt.Sprintf("instance-%d", at), drift)
		admitted, err := another.Admit(t.Context(), drift, guard.Request{
			RunID: fmt.Sprintf("run-%d", at), Kind: record.TriggerManual,
			OnWait: func(guard.Waiting) { t.Errorf("invocation %d waited beside another", at) },
		})
		if !refusedWith(err, record.MechanismWaitingSlotFull) || admitted != nil {
			t.Fatalf("invocation %d was not refused for the slot: admitted %v, err %v", at, admitted, err)
		}
	}

	if err := held.Claim.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	result := ended(t, done, 10*time.Second)
	if result.err != nil {
		t.Fatalf("the waiting invocation did not run once the claim freed: %v", result.err)
	}
	if result.admitted.WaitingTriggerID != accepted.TriggerID {
		t.Fatalf("the run started from waiting trigger %q, not the first invocation's %q",
			result.admitted.WaitingTriggerID, accepted.TriggerID)
	}

	refusals, err := store.ListRefusals(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	byMechanism := map[record.Mechanism][]record.Refusal{}
	for _, refusal := range refusals {
		byMechanism[refusal.Mechanism] = append(byMechanism[refusal.Mechanism], refusal)
	}
	if held := byMechanism[record.MechanismClaimHeld]; len(held) != 1 || held[0].TriggerKind != record.TriggerSchedule {
		t.Fatalf("claim_held refusals = %+v, want the tick's alone", held)
	}
	full := byMechanism[record.MechanismWaitingSlotFull]
	if len(full) != 2 || len(refusals) != 3 {
		t.Fatalf("refusals = %+v, want the tick's and two for the slot", refusals)
	}
	for _, refusal := range full {
		if refusal.TriggerKind != record.TriggerManual || refusal.WaitingTriggerID != "" {
			t.Fatalf("a slot refusal reads as %+v", refusal)
		}
	}
}
