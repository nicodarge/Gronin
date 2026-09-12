package guard_test

import (
	"bufio"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
	"github.com/nicodarge/Gronin/runtime/internal/guard/guardtest"
	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// stopBoundHelperEnv turns the helper below from a no-op into the subprocess the test
// after it needs: a runtime holding a claim it has lost, whose run ignores the
// cancellation. Only the test sets it, on a re-exec of this same test binary.
const stopBoundHelperEnv = "GRONIN_GUARD_STOP_BOUND_HELPER"

// helperStopBound is the run built not to stop. A run that ends when its context is
// cancelled satisfies SC-115 without the enforcement ever executing, which is why this
// one never does (R4).
const helperStopBound = 2 * time.Second

// TestHelperARunThatIgnoresItsContext is not exercised by `go test` directly.
func TestHelperARunThatIgnoresItsContext(t *testing.T) {
	if os.Getenv(stopBoundHelperEnv) == "" {
		t.Skip("not running as the stop-bound subprocess")
	}

	now := time.Date(2026, 9, 10, 6, 0, 0, 0, time.UTC)
	// The backend's clock is injected; the runtime's is the host's, because the stop
	// bound this measures is real elapsed time in another process.
	fake := guardtest.NewFake(guardtest.NewClock(now), guard.SystemClock())
	cfg := guard.Config{
		ClaimExpiry: 20 * time.Second, RenewEvery: time.Second, RenewBound: 500 * time.Millisecond,
		StopBound: helperStopBound, DecisionBound: 2 * time.Second,
	}
	subject := &guard.Guard{
		Coordinator: fake.Host(nil), Store: newStore(t), Config: cfg,
		Host: "host-a.example.com", Instance: "instance-a",
	}

	admitted, err := subject.Admit(t.Context(), book("drift-check"), guard.Request{
		RunID: "run-a", Kind: record.TriggerManual,
	})
	if err != nil {
		t.Fatal(err)
	}
	// The run: it is handed a way to stop and ignores it, and it never reports being
	// over. Nothing but the process ending can finish it.
	subject.Hold(admitted, func() {
		if _, err := os.Stdout.WriteString("stopping\n"); err != nil {
			t.Error(err)
		}
	})

	fake.Expire(admitted.Claim)
	select {}
}

// SC-115, R4: from the stop decision the run has the stop bound to be over, and the
// runtime ends its own process when it is not.
func TestTheStopBound(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), os.Args[0],
		"-test.run=^TestHelperARunThatIgnoresItsContext$")
	cmd.Env = append(os.Environ(), stopBoundHelperEnv+"=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "stopping\n" {
		t.Fatalf("the subprocess never reported its stop decision: line = %q, err = %v", line, err)
	}
	decided := time.Now()

	ended := make(chan error, 1)
	go func() { ended <- cmd.Wait() }()
	select {
	case <-ended:
		if took := time.Since(decided); took > helperStopBound+2*time.Second {
			t.Fatalf("the process ended %s after the stop decision, past its %s bound",
				took, helperStopBound)
		}
	case <-time.After(helperStopBound + 2*time.Second):
		t.Fatalf("a run that ignored its cancellation was still going %s after the stop decision",
			helperStopBound+2*time.Second)
	}
	if code := cmd.ProcessState.ExitCode(); code != guard.StopBoundExceeded {
		t.Fatalf("the process exited %d, not %d", code, guard.StopBoundExceeded)
	}
}
