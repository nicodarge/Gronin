package record_test

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// acceptHelperEnv, when set, turns TestHelperAcceptThenWaits from a no-op into the
// subprocess side of TestOneIdentityIsNewOnce: another process accepting the same
// identity, on a re-exec of this same test binary.
const acceptHelperEnv = "GRONIN_RECORD_ACCEPT_HELPER_DIR"

// TestHelperAcceptThenWaits is not exercised by `go test` directly. It accepts identity
// "x" for source "alerts", stopping inside that acceptance at Accept's own seam (T049) —
// after Accept has decided the identity is new and written that decision, but before its
// transaction commits — and reports there so the parent can start its own acceptance of
// the same identity while this one is still open. It waits for the parent to say it may
// commit, then reports what Accept returned.
func TestHelperAcceptThenWaits(t *testing.T) {
	dir := os.Getenv(acceptHelperEnv)
	if dir == "" {
		t.Skip("not running as the accept-race subprocess")
	}

	store, err := record.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}

	stdin := bufio.NewReader(os.Stdin)
	record.AcceptSeam = func() {
		if _, err := os.Stdout.WriteString("waiting\n"); err != nil {
			t.Error(err)
		}
		if _, err := stdin.ReadString('\n'); err != nil {
			t.Error(err)
		}
	}

	received := time.Date(2026, 9, 12, 6, 0, 0, 0, time.UTC)
	delivery, isNew, err := record.Accept(context.Background(), store, record.AcceptParams{
		Source: "alerts", Identity: "x", IdentityKind: record.IdentityDeclared,
		Body: []byte(`{"id":"x"}`), Peer: "192.0.2.10", ReceivedAt: received,
		Instance: "instance-child", ReplayWindow: time.Minute,
		Playbooks: []string{"alert-triage"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("%t %s\n", isNew, delivery.ID)
}

// SC-306, and contracts/ingress.md's A4: two acceptances of one identity inside its
// window produce one delivery and one set of hand-offs, across processes sharing the
// state directory. Forced, not raced: the subprocess is stopped at Accept's own seam
// with its transaction open and uncommitted, and only once it has reported stopping
// there does this process start its own acceptance of the same identity and then tell
// the subprocess it may commit — so an implementation that decided newness from a read
// outside its write transaction would decide "new" for both, which a race between two
// live processes might never expose but this ordering always does.
func TestOneIdentityIsNewOnce(t *testing.T) {
	dir := t.TempDir()

	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestHelperAcceptThenWaits$")
	cmd.Env = append(os.Environ(), acceptHelperEnv+"="+dir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	reader := bufio.NewReader(stdout)
	line, err := reader.ReadString('\n')
	if err != nil || line != "waiting\n" {
		t.Fatalf("the subprocess did not report stopping at the seam: line=%q err=%v", line, err)
	}

	store, err := record.Open(t.Context(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Inside the source's replay window, and later than the subprocess's ReceivedAt:
	// a bound comfortably longer than the hold, since the store's own busy timeout
	// (5s) is what this acceptance actually waits on for the write lock the
	// subprocess holds.
	received := time.Date(2026, 9, 12, 6, 0, 30, 0, time.UTC)

	type outcome struct {
		delivery record.Delivery
		isNew    bool
		err      error
	}
	started := make(chan struct{})
	parent := make(chan outcome, 1)
	go func() {
		close(started)
		delivery, isNew, err := record.Accept(context.Background(), store, record.AcceptParams{
			Source: "alerts", Identity: "x", IdentityKind: record.IdentityDeclared,
			Body: []byte(`{"id":"x","extra":1}`), Peer: "192.0.2.11", ReceivedAt: received,
			Instance: "instance-parent", ReplayWindow: time.Minute,
			Playbooks: []string{"alert-triage"},
		}, nil)
		parent <- outcome{delivery: delivery, isNew: isNew, err: err}
	}()
	<-started

	if _, err := stdin.Write([]byte("go\n")); err != nil {
		t.Fatal(err)
	}

	childLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("the subprocess did not report its acceptance: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("the subprocess exited with an error: %v", err)
	}

	var parentResult outcome
	select {
	case parentResult = <-parent:
	case <-time.After(5 * time.Second):
		t.Fatal("this process's own acceptance never returned")
	}
	if parentResult.err != nil {
		t.Fatal(parentResult.err)
	}

	fields := strings.Fields(childLine)
	if len(fields) != 2 {
		t.Fatalf("the subprocess's report %q did not have two fields", childLine)
	}
	childNew, err := strconv.ParseBool(fields[0])
	if err != nil {
		t.Fatalf("the subprocess's report %q: %v", childLine, err)
	}
	childID := fields[1]

	if childNew == parentResult.isNew {
		t.Fatalf("both acceptances reported new=%v; exactly one of the two must be new", childNew)
	}
	if !childNew {
		t.Fatal("the subprocess accepted first; it, not this process, must be the one reporting new")
	}

	delivery, err := store.GetDelivery(t.Context(), childID)
	if err != nil {
		t.Fatal(err)
	}
	if delivery.Repeats != 1 {
		t.Fatalf("the delivery's repeats = %d, want 1", delivery.Repeats)
	}
	handoffs, err := store.HandOffsOf(t.Context(), childID)
	if err != nil || len(handoffs) != 1 {
		t.Fatalf("hand-offs = %+v, err = %v; want exactly one set of hand-offs", handoffs, err)
	}
}
