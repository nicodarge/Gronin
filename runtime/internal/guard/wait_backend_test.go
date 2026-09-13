package guard_test

import (
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
	"github.com/nicodarge/Gronin/runtime/internal/guard/guardtest"
	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// FR-107 for a trigger that waited. When the claim frees and the backend cannot be reached,
// the trigger is refused naming the backend and runs nothing — waiting on is not an answer
// the backend gave, and running under no claim is the failure FR-107 exists to refuse.
func TestAWaitMeetsAnUnavailableBackend(t *testing.T) {
	stateDir := t.TempDir()
	fake := guardtest.NewFake(guardtest.NewClock(start), guardtest.NewClock(start))
	runtime := guardtest.NewClock(start)
	drift := waitable("drift-check", "")

	holder, store := waitingGuard(t, fake.Host(nil), runtime, stateDir, "instance-holder", drift)
	held, err := holder.Admit(t.Context(), drift, guard.Request{RunID: "run-held", Kind: record.TriggerManual})
	if err != nil {
		t.Fatal(err)
	}
	waiterHost := fake.Host(nil)
	waiter, _ := waitingGuard(t, waiterHost, runtime, stateDir, "instance-waiter", drift)
	waiting, done := admitInBackground(t, waiter, drift, "run-waiter")
	accepted := startedWaiting(t, waiting, done)

	waiterHost.Sever()
	if err := held.Claim.Release(t.Context()); err != nil {
		t.Fatal(err)
	}

	result := ended(t, done, 2*time.Second)
	if !refusedWith(result.err, record.MechanismBackendUnavailable) || result.admitted != nil {
		t.Fatalf("a wait that met an unreachable backend was not refused naming it: admitted %v, err %v",
			result.admitted, result.err)
	}
	assertEndedAs(t, store, accepted.TriggerID, record.WaitBackendUnavailable)

	// Nothing took the claim on its way out: another host still can.
	if _, err := fake.Host(nil).Acquire(t.Context(), guard.AcquireRequest{
		Name: drift.Name, Holder: guard.Holder{RunID: "run-after"}, Expiry: 30 * time.Second,
		Trigger: guard.TriggerRef{Kind: guard.KindManual},
	}); err != nil {
		t.Fatalf("the claim was left taken: %v", err)
	}
}
