package guard_test

import (
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
	"github.com/nicodarge/Gronin/runtime/internal/guard/guardtest"
	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// SC-106's expiry half, FR-112 and FR-118. The wait is measured on the runtime's monotonic
// reading: the injected clock is advanced past the declared wait, and in one case its wall
// reading is stepped back by an hour first, which a wait measured on the wall would then
// outlast.
func TestTheWaitExpiresOnItsMonotonicReading(t *testing.T) {
	for name, stepWall := range map[string]time.Duration{
		"past its declared wait":              0,
		"with the wall stepped back mid-wait": -time.Hour,
	} {
		t.Run(name, func(t *testing.T) {
			stateDir := t.TempDir()
			fake := guardtest.NewFake(guardtest.NewClock(start), guardtest.NewClock(start))
			runtime := guardtest.NewClock(start)
			drift := waitable(t, "drift-check", "10m")

			holder, store := waitingGuard(t, fake.Host(nil), runtime, stateDir, "instance-holder", drift)
			if _, err := holder.Admit(t.Context(), drift, guard.Request{
				RunID: "run-held", Kind: record.TriggerManual,
			}); err != nil {
				t.Fatal(err)
			}
			waiter, _ := waitingGuard(t, fake.Host(nil), runtime, stateDir, "instance-waiter", drift)
			waiting, done := admitInBackground(t, waiter, drift, "run-waiter")
			accepted := startedWaiting(t, waiting, done)
			if accepted.UpTo != 10*time.Minute {
				t.Fatalf("the trigger says it waits up to %s, not the 10m declared", accepted.UpTo)
			}

			runtime.StepWall(stepWall)
			runtime.Advance(10*time.Minute + time.Second)

			result := ended(t, done, 5*time.Second)
			if !refusedWith(result.err, record.MechanismWaitExpired) || result.admitted != nil {
				t.Fatalf("a trigger past its wait was not refused as expired: admitted %v, err %v",
					result.admitted, result.err)
			}
			assertEndedAs(t, store, accepted.TriggerID, record.WaitExpired)
		})
	}
}

// A wait of nothing still enters the slot — the row is there, and says how it ended — and
// expires at once rather than being refused as if it had never been accepted.
func TestTheWaitExpiresAtOnceWhenItIsZero(t *testing.T) {
	stateDir := t.TempDir()
	fake := guardtest.NewFake(guardtest.NewClock(start), guardtest.NewClock(start))
	runtime := guardtest.NewClock(start)
	drift := waitable(t, "drift-check", "0s")

	holder, store := waitingGuard(t, fake.Host(nil), runtime, stateDir, "instance-holder", drift)
	if _, err := holder.Admit(t.Context(), drift, guard.Request{
		RunID: "run-held", Kind: record.TriggerManual,
	}); err != nil {
		t.Fatal(err)
	}
	waiter, _ := waitingGuard(t, fake.Host(nil), runtime, stateDir, "instance-waiter", drift)
	_, done := admitInBackground(t, waiter, drift, "run-waiter")

	result := ended(t, done, 5*time.Second)
	if !refusedWith(result.err, record.MechanismWaitExpired) {
		t.Fatalf("a wait of 0s was not refused as expired: %v", result.err)
	}
	refusals, err := store.ListRefusals(t.Context(), 10)
	if err != nil || len(refusals) != 1 || refusals[0].WaitingTriggerID == "" {
		t.Fatalf("refusals = %+v, err = %v, want one naming the wait it ended", refusals, err)
	}
	assertEndedAs(t, store, refusals[0].WaitingTriggerID, record.WaitExpired)
}

// assertEndedAs reads the waiting trigger's row, and the refusal that ended it.
func assertEndedAs(t *testing.T, store *record.Store, id string, outcome record.WaitOutcome) {
	t.Helper()
	trigger, err := store.GetWaitingTrigger(t.Context(), id)
	if err != nil {
		t.Fatalf("the waiting trigger %s was never recorded: %v", id, err)
	}
	if trigger.Outcome != outcome {
		t.Fatalf("the waiting trigger ended as %q, want %q", trigger.Outcome, outcome)
	}
	refusals, err := store.ListRefusals(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, refusal := range refusals {
		if refusal.WaitingTriggerID == id && string(refusal.Mechanism) == string(outcome) {
			return
		}
	}
	t.Fatalf("no %s refusal names waiting trigger %s: %+v", outcome, id, refusals)
}
