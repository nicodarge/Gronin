package guard_test

import (
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
	"github.com/nicodarge/Gronin/runtime/internal/guard/guardtest"
	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// SC-114, FR-117 and FR-125. A trigger that waits and then runs was deferred, not refused:
// it leaves no refusal record, and its run says how long it waited — the interval on the
// monotonic reading, which a wall reading stepped forward mid-wait would lengthen.
func TestAWaitedRunLeavesNoRefusalAndSaysHowLongItWaited(t *testing.T) {
	stateDir := t.TempDir()
	fake := guardtest.NewFake(guardtest.NewClock(start), guardtest.NewClock(start))
	runtime := guardtest.NewClock(start)
	drift := waitable("drift-check", "")

	holder, store := waitingGuard(t, fake.Host(nil), runtime, stateDir, "instance-holder", drift)
	held, err := holder.Admit(t.Context(), drift, guard.Request{RunID: "run-held", Kind: record.TriggerManual})
	if err != nil {
		t.Fatal(err)
	}
	waiter, _ := waitingGuard(t, fake.Host(nil), runtime, stateDir, "instance-waiter", drift)
	waiting, done := admitInBackground(t, waiter, drift, "run-waiter")
	accepted := startedWaiting(t, waiting, done)

	runtime.StepWall(time.Hour)
	runtime.Advance(7 * time.Minute)
	if err := held.Claim.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	result := ended(t, done, 10*time.Second)
	if result.err != nil {
		t.Fatalf("the waiting trigger did not run: %v", result.err)
	}
	admitted := result.admitted
	if admitted.Waited != 7*time.Minute {
		t.Fatalf("waited %s, want the 7m that passed on the monotonic reading", admitted.Waited)
	}

	// The run the executor would begin, which ends the wait it started from.
	if err := store.CreateRun(t.Context(), record.Run{
		ID: admitted.RunID, PlaybookName: drift.Name, TriggerKind: record.TriggerManual,
		Status: record.StatusRunning, WaitingTriggerID: admitted.WaitingTriggerID,
		WaitedMS: admitted.Waited.Milliseconds(),
	}); err != nil {
		t.Fatal(err)
	}

	refusals, err := store.ListRefusals(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(refusals) != 0 {
		t.Fatalf("a trigger that waited and ran left refusals: %+v", refusals)
	}
	trigger, err := store.GetWaitingTrigger(t.Context(), accepted.TriggerID)
	if err != nil {
		t.Fatal(err)
	}
	if trigger.Outcome != record.WaitRan || trigger.RunID != admitted.RunID {
		t.Fatalf("the waiting trigger reads %+v, want ran as %s", trigger, admitted.RunID)
	}
	run, err := store.GetRun(t.Context(), admitted.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.WaitingTriggerID != accepted.TriggerID || run.WaitedMS != (7*time.Minute).Milliseconds() {
		t.Fatalf("the run reads waiting trigger %q after %dms", run.WaitingTriggerID, run.WaitedMS)
	}
}
