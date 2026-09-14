package main

import (
	"fmt"
	"strings"
	"syscall"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/nicodarge/Gronin/runtime/internal/bintest"
	"github.com/nicodarge/Gronin/runtime/internal/fakeagent"
	"github.com/nicodarge/Gronin/runtime/internal/guard/guardtest"
	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// SC-117, FR-128: one tick runs at most once across the deployment, including when the
// second host is delayed past the end of the first host's run of it.
//
// B is delayed by being stopped and resumed rather than by having its clock set behind,
// which a test cannot do to one process (specs/002-guard/research.md §5).
//
// On resume the scheduler fires only the most recent occurrence due, so which tick B asks
// for depends on when it wakes. The schedule is pinned to two instants and nothing after
// them for a year: once A has run the second, it is the only tick B can ask for, however
// late B wakes.
func TestADelayedHostDoesNotRunATickAgain(t *testing.T) {
	t.Setenv(fakeagent.ModeVar, fakeagent.ModeSuccess)
	// Built before the deadline is taken, so that compiling is not charged to arming.
	bintest.Build(t)
	fakeagent.Build(t)

	armedBy := time.Now().UTC().Add(armWithin)
	// Both hosts must be armed, and B stopped, before the first instant falls due. The
	// margin covers sending one signal after both hosts have said they are armed.
	first := armedBy.Add(10 * time.Second).Truncate(time.Minute).Add(time.Minute)
	if first.Minute() == 59 {
		first = first.Add(time.Minute)
	}
	second := first.Add(time.Minute)
	c := newCluster(t, fmt.Sprintf("%d,%d %d %d %d *",
		first.Minute(), second.Minute(), first.Hour(), first.Day(), int(first.Month())), "0")

	host := c.start(t, 0)
	delayed := c.start(t, 1)
	host.Expect(t, "armed 1 schedule", time.Until(armedBy))
	delayed.Expect(t, "armed 1 schedule", time.Until(armedBy))
	if err := delayed.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	if now := time.Now().UTC(); !now.Before(first) {
		t.Fatalf("host B was stopped at %s, after the first tick %s fell due", now, first)
	}

	// A runs both instants. The second is a tick admitted after one is recorded, which is
	// what fails an implementation that refuses every tick once one is.
	c.waitForRuns(t, 2, time.Until(second)+2*time.Minute)

	// The mark is left by the gather step, so it says the run started, not that it ended,
	// and the record says a run ended before its claim is released. B resumed while the
	// claim is held is refused as claim_held, which is FR-101 and not what this test is
	// about, so the claim itself is what is waited for.
	client := guardtest.NewClient(t, c.server.Endpoint())
	waitFor(t, 2*time.Minute, func() bool {
		held, err := client.Get(t.Context(), "gronin/claims/drift-check", clientv3.WithCountOnly())
		if err != nil {
			t.Fatal(err)
		}
		return held.Count == 0
	}, func() string {
		return "host A's claim was never released; it recorded:\n" + c.runs(t, 0)
	})

	if err := delayed.Signal(syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	// B catches up and fires the second tick. It must be refused, and the record is where
	// that is readable — the absence of a further line alone would also be satisfied by a
	// B that never woke up.
	waitFor(t, time.Minute, func() bool {
		return strings.Contains(c.refusals(t, 1), string(record.MechanismTickAlreadyRan))
	}, func() string {
		return "host B recorded no tick_already_ran refusal; it recorded:\n" + c.refusals(t, 1)
	})

	refused := c.refusals(t, 1)
	if want := "tick " + second.Format(time.RFC3339) + " ran as run 20"; !strings.Contains(refused, want) {
		t.Fatalf("the refusal does not name the tick A ran and its run (%q):\n%s", want, refused)
	}
	if ran := c.ran(t); ran != 2 {
		t.Fatalf("%d runs of two ticks; host A ran:\n%s\nhost B ran:\n%s",
			ran, c.runs(t, 0), c.runs(t, 1))
	}
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
