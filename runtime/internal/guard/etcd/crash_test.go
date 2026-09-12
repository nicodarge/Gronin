package etcd

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
	"github.com/nicodarge/Gronin/runtime/internal/guard/guardtest"
)

// crashHelperEnv carries the endpoint of the embedded server to the subprocess, and
// turns the helper below from a no-op into the holder the test needs. Only the test sets
// it, on a re-exec of this same test binary.
const crashHelperEnv = "GRONIN_GUARD_ETCD_CRASH_ENDPOINT"

// lapsingExpiry is what the killed holder asks for. The server grants at least its own
// minimum whatever it is asked, so this is the floor and not a choice.
const lapsingExpiry = guardtest.MinimumTTL

// TestHelperAClaimHolderThenBlocks is not exercised by `go test` directly: it does
// nothing unless crashHelperEnv is set.
func TestHelperAClaimHolderThenBlocks(t *testing.T) {
	endpoint := os.Getenv(crashHelperEnv)
	if endpoint == "" {
		t.Skip("not running as the crash-test subprocess")
	}

	client := guardtest.NewClient(t, endpoint)
	if _, err := New(client, Options{Prefix: "gronin/"}).Acquire(bounded(t), guard.AcquireRequest{
		Name:    "drift-check",
		Holder:  guard.Holder{Host: "host.example.com", Instance: "helper", RunID: "run-helper"},
		Expiry:  lapsingExpiry,
		Trigger: guard.TriggerRef{Kind: guard.KindManual},
	}); err != nil {
		t.Fatal(err)
	}

	// Say the claim is held, then hang until SIGKILL — no release, no cleanup. That
	// absence is the point: the lease is what ends the claim, not this process's own
	// shutdown path.
	if _, err := os.Stdout.WriteString("held\n"); err != nil {
		t.Fatal(err)
	}
	select {}
}

// SC-102, FR-104: a process killed mid-run leaves its playbook runnable again within the
// claim's expiry, with no operator action.
//
// Polled with a deadline rather than slept out: a sleep shorter than the expiry passes
// without the recovery ever having happened.
func TestAKilledHoldersClaimLapses(t *testing.T) {
	server := guardtest.StartServer(t)

	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestHelperAClaimHolderThenBlocks$")
	cmd.Env = append(os.Environ(), crashHelperEnv+"="+server.Endpoint())
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "held\n" {
		t.Fatalf("the subprocess did not report holding the claim: line = %q, err = %v", line, err)
	}
	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()

	after := New(guardtest.NewClient(t, server.Endpoint()), Options{Prefix: "gronin/"})
	contend := func() (guard.Claim, error) {
		return after.Acquire(bounded(t), guard.AcquireRequest{
			Name:    "drift-check",
			Holder:  guard.Holder{Host: "host.example.com", Instance: "after", RunID: "run-after"},
			Expiry:  lapsingExpiry,
			Trigger: guard.TriggerRef{Kind: guard.KindManual},
		})
	}

	// The claim existed and was not released on the way out: that is what makes the
	// recovery below a recovery rather than a claim that was never taken.
	if _, err := contend(); !errors.Is(err, guard.ErrHeld) {
		t.Fatalf("the killed holder's claim was not held immediately after: %v", err)
	}

	const slack = 5 * time.Second
	deadline := time.Now().Add(lapsingExpiry + slack)
	for {
		claim, err := contend()
		if err == nil {
			_ = claim.Release(bounded(t))
			return
		}
		if !errors.Is(err, guard.ErrHeld) {
			t.Fatalf("acquiring a lapsing claim: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("the claim was still held %s after its holder was killed, with an expiry of %s",
				lapsingExpiry+slack, lapsingExpiry)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
