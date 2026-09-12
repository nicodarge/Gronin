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
	// Every two minutes, not every one. On resume the scheduler fires only the most
	// recent due occurrence, so B reaches tick_already_ran only while that occurrence is
	// still the one A ran; once the next falls due, B races A for it and is refused as
	// claim_held instead. That is a margin, not a guarantee: under -race on a loaded
	// runner A's run alone has taken about a minute and a half, which leaves B roughly
	// thirty seconds of the period to catch up in.
	c := newCluster(t, "*/2 * * * *", "0")

	c.serve(t, 0)
	delayed := c.serve(t, 1)
	if err := delayed.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}

	// A takes the tick and its run is over. The mark is left by the gather step, so it
	// says the run started, not that it ended — and A holds its claim until it ends.
	// Resuming B before then gets it refused as claim_held, which is FR-101 and not what
	// this test is about, so wait for A's run to leave the running state as well.
	c.waitForRuns(t, 1, 150*time.Second)
	waitFor(t, 2*time.Minute, func() bool {
		return !strings.Contains(c.runs(t, 0), string(record.StatusRunning))
	}, func() string {
		return "host A's run never left the running state; it recorded:\n" + c.runs(t, 0)
	})

	if err := delayed.Signal(syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	// B catches up and fires the same tick. It must be refused, and the record is where
	// that is readable — the absence of a second line alone would also be satisfied by a
	// B that never woke up.
	waitFor(t, time.Minute, func() bool {
		return strings.Contains(c.refusals(t, 1), string(record.MechanismTickAlreadyRan))
	}, func() string {
		return "host B recorded no tick_already_ran refusal; it recorded:\n" + c.refusals(t, 1)
	})

	refused := c.refusals(t, 1)
	if !strings.Contains(refused, "ran as run 20") {
		t.Fatalf("the refusal does not name the run that took the tick:\n%s", refused)
	}
	// Two runs here is either the defect, or the next occurrence falling due before this
	// line and A running it. Both hosts' runs are what tell them apart.
	if ran := c.ran(t); ran != 1 {
		t.Fatalf("%d runs where one tick was expected; host A ran:\n%s\nhost B ran:\n%s",
			ran, c.runs(t, 0), c.runs(t, 1))
	}

	// And the next tick, with both hosts live, produces exactly one further run — which
	// is what fails an implementation that refuses every tick once one is recorded.
	c.waitForRuns(t, 2, 150*time.Second)
}

// waitFor polls until done reports true, and fails with why if it never does. why is
// built at the deadline rather than passed in, so it can report the state that was
// waited for and never came.
func waitFor(t *testing.T, within time.Duration, done func() bool, why func() string) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("%s within %s", why(), within)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
