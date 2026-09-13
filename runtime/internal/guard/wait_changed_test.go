package guard_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
	"github.com/nicodarge/Gronin/runtime/internal/guard/guardtest"
	"github.com/nicodarge/Gronin/runtime/internal/playbook"
	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// touchingHost rewrites a playbook's file the first time a claim is taken through it: a
// change landing inside the one Acquire call between the waiter's read and its claim.
type touchingHost struct {
	*guardtest.FakeHost
	path string
	once sync.Once
}

func (h *touchingHost) Acquire(ctx context.Context, req guard.AcquireRequest) (guard.Claim, error) {
	claim, err := h.FakeHost.Acquire(ctx, req)
	if err == nil {
		h.once.Do(func() {
			if err := os.WriteFile(h.path, []byte("name: drift-renamed\n# rewritten\n"), 0o600); err != nil {
				panic(err)
			}
		})
	}
	return claim, err
}

// FR-121. A waiting trigger reads its file before it asks for the claim and looks again once
// it holds it. A file that changed inside that one call gives the claim back — kept, it would
// block the playbook until it expired — and the next round reads the file and refuses what it
// now declares.
func TestAChangedPlaybookGivesTheClaimBack(t *testing.T) {
	stateDir := t.TempDir()
	fake := guardtest.NewFake(guardtest.NewClock(start), guardtest.NewClock(start))
	runtime := guardtest.NewClock(start)
	drift := waitable(t, "drift-check", "")
	renamed := waitable(t, "drift-renamed", "")

	holder, store := waitingGuard(t, fake.Host(nil), runtime, stateDir, "instance-holder", drift)
	held, err := holder.Admit(t.Context(), drift, guard.Request{RunID: "run-held", Kind: record.TriggerManual})
	if err != nil {
		t.Fatal(err)
	}
	waiter, _ := waitingGuard(t, &touchingHost{FakeHost: fake.Host(nil), path: drift.Path},
		runtime, stateDir, "instance-waiter", drift)
	reads := 0
	waiter.Slot.Reload = func(string) (*playbook.Playbook, error) {
		reads++
		if reads == 1 {
			return drift, nil
		}
		return renamed, nil
	}
	waiting, done := admitInBackground(t, waiter, drift, "run-waiter")
	accepted := startedWaiting(t, waiting, done)

	if err := held.Claim.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	result := ended(t, done, 10*time.Second)
	if !refusedWith(result.err, record.MechanismPlaybookChanged) || result.admitted != nil {
		t.Fatalf("a trigger whose file changed as it took the claim was not refused as changed: admitted %v, err %v",
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
