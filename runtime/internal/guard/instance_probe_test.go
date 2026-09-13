package guard_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// A reconciliation looks at an instance by taking its lock. The only other thing that asks
// for a dead instance's lock is another reconciliation looking at the same instant, and
// that one reads the instance as alive and leaves the row: the trigger is dropped once,
// with one refusal, and the dead instance's lock file goes with it.
func TestAReconciliationsProbeOnlyDefersAnother(t *testing.T) {
	stateDir := t.TempDir()
	first, second := newStoreAt(t, stateDir), newStoreAt(t, stateDir)

	// What a killed process leaves: a lock file nobody holds.
	lockFile := filepath.Join(stateDir, guard.InstancesDir, "instance-dead.lock")
	if err := os.MkdirAll(filepath.Dir(lockFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := first.AcceptWaiting(t.Context(), record.WaitingTrigger{
		PlaybookName: "drift-check", PlaybookPath: "/srv/gronin/playbooks/drift-check.yaml",
		TriggerKind: record.TriggerManual, AcceptedAt: start, ExpiresAt: start.Add(30 * time.Minute),
		Instance: "instance-dead",
	}, nil); err != nil {
		t.Fatal(err)
	}

	var (
		probed     bool
		inner      int
		innerError error
	)
	guard.OnProbe(t, func(string) {
		if probed {
			return
		}
		probed = true
		inner, innerError = guard.Reconcile(t.Context(), stateDir, second, nil)
	})
	dropped, err := guard.Reconcile(t.Context(), stateDir, first, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !probed {
		t.Fatal("the reconciliation never held the dead instance's lock")
	}
	if innerError != nil || inner != 0 {
		t.Fatalf("the reconciliation that looked at the same instant dropped %d, err %v", inner, innerError)
	}
	if dropped != 1 {
		t.Fatalf("%d dropped, want the one trigger whose process is gone", dropped)
	}
	refusals, err := first.ListRefusals(t.Context(), 10)
	if err != nil || len(refusals) != 1 || refusals[0].Mechanism != record.MechanismDropped {
		t.Fatalf("refusals = %+v, err = %v, want one drop", refusals, err)
	}
	if _, err := os.Stat(lockFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the dead instance's lock file is still there: %v", err)
	}
}

func newStoreAt(t *testing.T, stateDir string) *record.Store {
	t.Helper()
	store, err := record.Open(t.Context(), filepath.Join(stateDir, "record"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
