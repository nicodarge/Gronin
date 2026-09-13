package guard_test

import (
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
	"github.com/nicodarge/Gronin/runtime/internal/guard/guardtest"
	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// FR-121. A waiting trigger takes the claim under the name it collided with, then reads its
// file again. When the file no longer declares that name the trigger does not run, and the
// claim it took goes back: kept, it would block the playbook until it expired.
func TestAChangedPlaybookGivesTheClaimBack(t *testing.T) {
	stateDir := t.TempDir()
	fake := guardtest.NewFake(guardtest.NewClock(start), guardtest.NewClock(start))
	runtime := guardtest.NewClock(start)
	drift := waitable("drift-check", "")

	holder, store := waitingGuard(t, fake.Host(nil), runtime, stateDir, "instance-holder", drift)
	held, err := holder.Admit(t.Context(), drift, guard.Request{RunID: "run-held", Kind: record.TriggerManual})
	if err != nil {
		t.Fatal(err)
	}
	renamed := waitable("drift-renamed", "")
	waiter, _ := waitingGuard(t, fake.Host(nil), runtime, stateDir, "instance-waiter", renamed)
	waiting, done := admitInBackground(t, waiter, drift, "run-waiter")
	accepted := startedWaiting(t, waiting, done)

	if err := held.Claim.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	result := ended(t, done, 10*time.Second)
	if !refusedWith(result.err, record.MechanismPlaybookChanged) || result.admitted != nil {
		t.Fatalf("a trigger whose file was renamed was not refused as changed: admitted %v, err %v",
			result.admitted, result.err)
	}
	assertEndedAs(t, store, accepted.TriggerID, record.WaitPlaybookChanged)

	if _, err := fake.Host(nil).Acquire(t.Context(), guard.AcquireRequest{
		Name: drift.Name, Holder: guard.Holder{RunID: "run-after"}, Expiry: 30 * time.Second,
		Trigger: guard.TriggerRef{Kind: guard.KindManual},
	}); err != nil {
		t.Fatalf("the claim the refused trigger took was kept: %v", err)
	}
}
