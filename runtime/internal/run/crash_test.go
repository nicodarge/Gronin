package run_test

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/record"
	"github.com/nicodarge/Gronin/runtime/internal/run"
)

// crashHelperEnv, when set, turns TestHelperLockHolderThenBlocks from a no-op into the
// subprocess side of TestALockHeldByAKilledProcessIsAcquirable: it is not a test of its
// own, it is the "another process" TestALockHeldByAKilledProcessIsAcquirable needs.
const crashHelperEnv = "GRONIN_RUN_CRASH_HELPER_LOCKS_DIR"

// TestHelperLockHolderThenBlocks is not exercised by `go test` directly — it does
// nothing unless crashHelperEnv is set, which only the test below does, on a
// re-exec of this same test binary.
func TestHelperLockHolderThenBlocks(t *testing.T) {
	locksDir := os.Getenv(crashHelperEnv)
	if locksDir == "" {
		t.Skip("not running as the crash-test subprocess")
	}

	dir := t.TempDir()
	store, err := record.Open(t.Context(), filepath.Join(dir, "record"), nil)
	if err != nil {
		t.Fatal(err)
	}
	manager := run.NewManager(store, filepath.Join(dir, "work"), locksDir)

	if _, err := manager.Begin(t.Context(), "drift-check", record.TriggerSchedule, ""); err != nil {
		t.Fatal(err)
	}

	// Tell the parent the lock is held, then hang until SIGKILL — no cleanup, no
	// deferred unlock. That absence is the point of the test: the kernel is what
	// releases the flock, not this process's own shutdown path.
	if _, err := os.Stdout.WriteString("locked\n"); err != nil {
		t.Fatal(err)
	}
	select {}
}

// The property that justifies flock over a lease in the record store: the kernel
// releases the lock when the holding process dies, including on SIGKILL, so there is no
// stale lock to detect and nothing to clean up by hand.
func TestALockHeldByAKilledProcessIsAcquirable(t *testing.T) {
	locksDir := filepath.Join(t.TempDir(), "locks")

	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestHelperLockHolderThenBlocks$")
	cmd.Env = append(os.Environ(), crashHelperEnv+"="+locksDir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	reader := bufio.NewReader(stdout)
	line, err := reader.ReadString('\n')
	if err != nil || line != "locked\n" {
		t.Fatalf("subprocess did not report holding the lock: line=%q err=%v", line, err)
	}

	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()

	dir := t.TempDir()
	store, err := record.Open(t.Context(), filepath.Join(dir, "record"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager := run.NewManager(store, filepath.Join(dir, "work"), locksDir)

	acquired := make(chan error, 1)
	go func() {
		_, err := manager.Begin(t.Context(), "drift-check", record.TriggerManual, "")
		acquired <- err
	}()

	select {
	case err := <-acquired:
		if err != nil {
			t.Fatalf("the lock was not acquirable after its holder was killed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the lock stayed held after its holder was killed")
	}
}
