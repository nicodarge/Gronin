package main

import (
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/fakeagent"
	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// SC-117, FR-128: one tick runs at most once across the deployment, including when the
// second host is delayed past the end of the first host's run of it.
//
// The run is deliberately short — shorter than the gap between the two deliveries — so
// that FR-101 alone cannot explain the refusal: a run that outlasted the gap would be
// refused as held and the test would pass with FR-128 removed.
//
// B is delayed by being stopped and resumed rather than by having its clock set behind,
// which a test cannot do to one process (specs/002-guard/research.md §5).
func TestADelayedHostDoesNotRunATickAgain(t *testing.T) {
	t.Setenv(fakeagent.ModeVar, fakeagent.ModeSuccess)
	c := newCluster(t, "0")

	c.serve(t, 0)
	delayed := c.serve(t, 1)
	if err := delayed.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}

	// A takes the tick and its run is over.
	c.waitForRuns(t, 1, 100*time.Second)

	if err := delayed.Signal(syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	// B catches up and fires the same tick. It must be refused, and the record is where
	// that is readable — the absence of a second line alone would also be satisfied by a
	// B that never woke up.
	waitFor(t, 30*time.Second, func() bool {
		return strings.Contains(c.refusals(t, 1), string(record.MechanismTickAlreadyRan))
	}, "host B recorded no tick_already_ran refusal")

	refused := c.refusals(t, 1)
	if !strings.Contains(refused, "ran as run 20") {
		t.Fatalf("the refusal does not name the run that took the tick:\n%s", refused)
	}
	if ran := c.ran(t); ran != 1 {
		t.Fatalf("%d runs of one tick", ran)
	}

	// And the next tick, with both hosts live, produces exactly one further run — which
	// is what fails an implementation that refuses every tick once one is recorded.
	c.waitForRuns(t, 2, 100*time.Second)
}

// waitFor polls until done reports true, and fails with why if it never does.
func waitFor(t *testing.T, within time.Duration, done func() bool, why string) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("%s within %s", why, within)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
