package run_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
	"github.com/nicodarge/Gronin/runtime/internal/guard/guardtest"
	"github.com/nicodarge/Gronin/runtime/internal/record"
	"github.com/nicodarge/Gronin/runtime/internal/run"
)

// The same contract the fake and the etcd adapter are held to, where its table says it
// applies to the file lock. What is n/a is n/a because a lock is not a lease: it has no
// expiry to judge, no token, and cannot be lost while its holder lives.
//
// Each node is a Manager of its own over one state directory, which is what two gronin
// processes sharing a deployment are.
func TestFileLockContract(t *testing.T) {
	dir := t.TempDir()
	locks, records := filepath.Join(dir, "locks"), filepath.Join(dir, "record")

	manager := func(t *testing.T, recordDir, locksDir string) *run.Manager {
		t.Helper()
		store, err := record.Open(t.Context(), recordDir, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		return run.NewManager(store, filepath.Join(dir, "work"), locksDir)
	}

	guardtest.Contract(t, guardtest.Subject{
		// The seam is ignored: this coordinator reads and writes the last tick under the
		// lock it has already taken, so nothing can come between the two.
		New: func(t *testing.T, _ guard.Seam) guardtest.Node {
			t.Helper()
			return guardtest.Node{Coordinator: manager(t, records, locks).FileLock()}
		},
		Unreachable: func(t *testing.T) guard.Coordinator {
			t.Helper()
			// A regular file where the locks directory has to be: MkdirAll fails with
			// ENOTDIR whatever the caller's privileges, which a permissions-based fixture
			// cannot promise.
			blocked := filepath.Join(t.TempDir(), "locks")
			if err := os.WriteFile(blocked, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			return manager(t, filepath.Join(t.TempDir(), "record"), blocked).FileLock()
		},
		Expiry: 30 * time.Second,
		NotApplicable: map[string]string{
			"C2":  "a lock ends with the process holding it, which TestALockHeldByAKilledProcessIsAcquirable proves",
			"C3":  "no clock takes part in holding a lock",
			"C5":  "a lock cannot be lost while its holder lives",
			"C6":  "a lock carries no fencing token, and a run under it records none",
			"C12": "a lock has no expiry to grant",
		},
	})
}
