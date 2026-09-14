package guardtest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
)

// Subject is one Coordinator implementation the contract runs against.
type Subject struct {
	// New returns a coordinator of its own on the backend every other one New returns
	// shares, as a second host would have. seam is called between reading a name's last
	// tick and taking the claim; a subject that has no such gap ignores it and says so in
	// Seams.
	New func(t *testing.T, seam guard.Seam) Node
	// Seams reports whether New honours its seam.
	Seams bool
	// Unreachable returns a coordinator whose backend cannot be reached.
	Unreachable func(t *testing.T) guard.Coordinator
	// Lapse makes a claim lapse as its expiry would, without its holder's help.
	Lapse func(t *testing.T, claim guard.Claim)
	// Elapse lets d pass on the backend's clock.
	Elapse func(t *testing.T, d time.Duration)
	// Backend reads the clock the backend judges a claim's expiry on. Nil only where C2 and
	// C3 are n/a.
	Backend func() guard.Instant
	// StepRuntime moves the runtime's clock, and only the runtime's.
	StepRuntime func(d time.Duration)
	// ShortGrant returns a coordinator whose backend grants less than it is asked; nil
	// where no backend can be made to.
	ShortGrant func(t *testing.T) guard.Coordinator
	// LongGrant returns a coordinator whose backend grants granted when asked for asked.
	LongGrant func(t *testing.T) (coordinator guard.Coordinator, asked, granted time.Duration)
	// Expiry is the claim expiry the suite asks for, and Slack what a lapse may take
	// beyond it before the suite calls the claim stuck.
	Expiry, Slack time.Duration
	// NotApplicable names the clauses the contract's table marks n/a for this subject,
	// each with the reason.
	NotApplicable map[string]string
	// NewWatching is New for a coordinator whose Released waits on the backend's own
	// notification: watching is called once it has begun to, which is how C10 asserts that
	// it does not return while the claim is held without sleeping. Nil for a coordinator
	// whose Released returns at each poll, which says why in ReleasedPolls.
	NewWatching   func(t *testing.T, watching func(name string)) Node
	ReleasedPolls string
}

// Node is one host's coordinator on a subject's backend.
type Node struct {
	guard.Coordinator
	// Hold stops the backend answering this node without closing anything; nil where
	// nothing can be held, as for the file lock, whose calls never wait on a backend.
	Hold func()
}

// The clauses the contract's table allows a subject to be excused from. Anything else a
// subject names is refused: a skipped clause passes, and passing is not what a skip is
// for.
var excusable = map[string]bool{"C2": true, "C3": true, "C5": true, "C6": true, "C12": true}

// Contract runs every clause of specs/002-guard/contracts/coordination.md against subject.
func Contract(t *testing.T, subject Subject) {
	for id := range subject.NotApplicable {
		if !excusable[id] {
			t.Fatalf("%s is not n/a for any subject in the contract's table", id)
		}
	}
	if (subject.NewWatching == nil) == (subject.ReleasedPolls == "") {
		t.Fatal("a subject either watches a claim, through NewWatching, or says in ReleasedPolls why it polls")
	}
	clauses := []struct {
		id  string
		run func(*testing.T, Subject)
	}{
		{"C1", exclusion},
		{"C2", expiryIsTheBackends},
		{"C3", noHostClockJudgesAClaim},
		{"C4", everyCallIsBounded},
		{"C5", lossIsDefinitive},
		{"C6", fencing},
		{"C7", release},
		{"C8", theRateSlotAndTheClaimAreOneStep},
		{"C9", theRateWindowIsTheBackends},
		{"C10", released},
		{"C10", releasedWaitsWhileHeld},
		{"C11", unavailableIsNotHeld},
		{"C12", theGrantedExpiry},
		{"C13", oneTickOnce},
		{"C14", theTickIsTheOneRequested},
	}
	for _, clause := range clauses {
		t.Run(clause.id, func(t *testing.T) {
			if why, excused := subject.NotApplicable[clause.id]; excused {
				t.Skipf("n/a for this subject: %s", why)
			}
			clause.run(t, subject)
		})
	}
}

// callBound is what each call is given unless a clause says otherwise: far more than
// any backend here takes to answer, so that only a hang reaches it.
const callBound = 10 * time.Second

func bounded(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), callBound)
	t.Cleanup(cancel)
	return ctx
}

// watchdogSlack is how long past its deadline a call may take before the suite calls it
// unbounded.
const watchdogSlack = 250 * time.Millisecond

// watched runs call under a deadline and watches it from outside, so that a call which
// ignores its context fails the test here rather than hanging it until go test's own
// timeout, which would take ten minutes to kill each such mutant. A call still running at
// deadline plus watchdogSlack is left running, and errUnbounded returned.
func watched(t *testing.T, deadline time.Duration, call func(context.Context) error) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), deadline)
	done := make(chan error, 1)
	go func() {
		defer cancel()
		done <- call(ctx)
	}()
	watchdog := time.NewTimer(deadline + watchdogSlack)
	defer watchdog.Stop()
	select {
	case err := <-done:
		return err
	case <-watchdog.C:
		return errUnbounded
	}
}

var errUnbounded = errors.New("the call had not returned by its deadline")

func manual(name, run string) guard.AcquireRequest {
	return guard.AcquireRequest{
		Name:    name,
		Holder:  guard.Holder{Host: "host-" + run + ".example.com", Instance: "instance-" + run, RunID: run},
		Trigger: guard.TriggerRef{Kind: guard.KindManual},
	}
}

func scheduled(name, run string, due time.Time) guard.AcquireRequest {
	req := manual(name, run)
	req.Trigger = guard.TriggerRef{Kind: guard.KindSchedule, DueAt: due}
	return req
}

// rated is req with a declared rate limit of runs per per.
func rated(req guard.AcquireRequest, runs int, per time.Duration) guard.AcquireRequest {
	req.Rate = &guard.RateLimit{Runs: runs, Per: per}
	return req
}

func acquire(t *testing.T, s Subject, c guard.Coordinator, req guard.AcquireRequest) guard.Claim {
	t.Helper()
	if req.Expiry == 0 {
		req.Expiry = s.Expiry
	}
	claim, err := c.Acquire(bounded(t), req)
	if err != nil {
		t.Fatalf("acquiring %s for %s: %v", req.Name, req.Holder.RunID, err)
	}
	return claim
}

func try(t *testing.T, s Subject, c guard.Coordinator, req guard.AcquireRequest) (guard.Claim, error) {
	t.Helper()
	if req.Expiry == 0 {
		req.Expiry = s.Expiry
	}
	return c.Acquire(bounded(t), req)
}

func releaseClaim(t *testing.T, claim guard.Claim) {
	t.Helper()
	if err := claim.Release(bounded(t)); err != nil {
		t.Fatalf("releasing: %v", err)
	}
}

func refusedAs(t *testing.T, err, want error, what string) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: got %v, want %v", what, err, want)
	}
}

// C1. While a claim is held nobody else acquires it, and the refusal names who holds it.
func exclusion(t *testing.T, s Subject) {
	a, b := s.New(t, nil), s.New(t, nil)

	held := acquire(t, s, a, manual("c1", "run-a"))
	_, err := try(t, s, b, manual("c1", "run-b"))
	refusedAs(t, err, guard.ErrHeld, "a second acquisition of a held claim")
	if !strings.Contains(err.Error(), "run-a") {
		t.Fatalf("the refusal does not name the run holding the claim: %v", err)
	}

	other := acquire(t, s, b, manual("c1-other", "run-c"))
	releaseClaim(t, other)
	releaseClaim(t, held)
}

// C2. A renewed claim is never acquirable by another; one no longer renewed becomes
// acquirable within its expiry and a stated slack, found by polling rather than by
// sleeping the expiry and hoping.
func expiryIsTheBackends(t *testing.T, s Subject) {
	holder, contender := s.New(t, nil), s.New(t, nil)

	step := s.Expiry / 4
	renewed(t, s, holder, contender, "c2", int(3*s.Expiry/2/step),
		func() { s.Elapse(t, step) },
		func(round int) string { return fmt.Sprintf("after %s", time.Duration(round+1)*step) })

	poll := s.Expiry / 10
	for waited := time.Duration(0); ; waited += poll {
		took, err := try(t, s, contender, manual("c2", "run-b"))
		if err == nil {
			releaseClaim(t, took)
			return
		}
		refusedAs(t, err, guard.ErrHeld, "acquiring a claim that is lapsing")
		if waited > s.Expiry+s.Slack {
			t.Fatalf("the claim is still held %s after its last renewal, with an expiry of %s", waited, s.Expiry)
		}
		s.Elapse(t, poll)
	}
}

// C3. Moving the runtime's clock by hours changes nothing about a claim its holder renews.
func noHostClockJudgesAClaim(t *testing.T, s Subject) {
	holder, contender := s.New(t, nil), s.New(t, nil)
	claim := renewed(t, s, holder, contender, "c3", 4,
		func() {
			s.StepRuntime(time.Hour)
			s.Elapse(t, s.Expiry/4)
		},
		func(int) string { return "after the runtime's clock moved hours" })
	releaseClaim(t, claim)
}

// renewalAttempts is how many attempts in a row renewed lets a stalled host cost it.
const renewalAttempts = 3

// renewed acquires name on holder and renews it rounds times, calling each before every
// renewal and asserting after it that contender is refused. It returns the claim, held.
//
// A backend judging expiry on a real clock keeps counting while the host running the suite
// is stalled, and a stall longer than the expiry lapses a claim nobody was able to renew:
// losing it then is the backend being right, not a clause failing. So a loss found more than
// the expiry after the last renewal was sent starts the attempt over (renewing), and only a
// host that stalls like that in every attempt fails the clause for it.
func renewed(t *testing.T, s Subject, holder, contender guard.Coordinator, name string, rounds int,
	each func(), label func(round int) string,
) guard.Claim {
	t.Helper()
	if s.Backend == nil {
		t.Fatal("a subject whose claims expire reads its backend's clock through Backend")
	}
	var longest time.Duration
	for range renewalAttempts {
		claim, gap, err := renewing(t, s, holder, contender, name, rounds, each, label)
		if err != nil {
			t.Fatal(err)
		}
		if claim != nil {
			return claim
		}
		t.Logf("%s was lost %s after its last renewal was sent, past its expiry: starting over", name, gap)
		longest = max(longest, gap)
	}
	t.Fatalf("every one of %d attempts went longer than the expiry of %s between renewals, up to %s: "+
		"this host stalls too long for any renewal to be judged", renewalAttempts, s.Expiry, longest)
	return nil
}

// renewing is one attempt of renewed. It returns the claim still held; or no claim and how
// long after the last renewal was sent it was found lost, when that is past its expiry; or
// an error, when it was lost sooner, which only the backend can have done.
func renewing(t *testing.T, s Subject, holder, contender guard.Coordinator, name string, rounds int,
	each func(), label func(round int) string,
) (guard.Claim, time.Duration, error) {
	t.Helper()
	sent := s.Backend()
	claim := acquire(t, s, holder, manual(name, "run-a"))
	if granted := claim.Expiry(); granted < s.Expiry {
		t.Fatalf("%s was granted %s, less than the %s asked: the cadence below assumes granted expiry is never shorter than requested", name, granted, s.Expiry)
	}
	for round := range rounds {
		each()
		renewal := s.Backend()
		// A Backend that jumps further than this in one round cannot be the real one this
		// claim's expiry is judged on. Excused like a stall rather than failed outright, so
		// a genuine freeze that big is retried instead of hard-failing on the spot, and a
		// miswired Backend — which jumps the same way on every attempt — still exhausts them.
		// Unlike a loss found through Renew or a contender, nothing here has told the backend
		// this claim is gone, so it is released before the next attempt tries to acquire it
		// under the same name.
		if step := renewal.Sub(sent); step < 0 || step > backendJumpBound(s) {
			// The claim may already be lost by the time this releases it, which C5 permits and is not this call's error to report.
			if err := claim.Release(bounded(t)); err != nil && !errors.Is(err, guard.ErrLost) {
				t.Fatalf("releasing an excused claim: %v", err)
			}
			return nil, step, nil
		}
		if err := claim.Renew(bounded(t)); err != nil {
			if gap, late := overdue(sent, s.Backend(), claim.Expiry()); late && errors.Is(err, guard.ErrLost) {
				return nil, gap, nil
			}
			return nil, 0, fmt.Errorf("renewing %s: %w", label(round), err)
		}
		sent = renewal
		took, err := try(t, s, contender, manual(name, "run-b"))
		if err == nil {
			gap, late := overdue(sent, s.Backend(), claim.Expiry())
			releaseClaim(t, took)
			if late {
				return nil, gap, nil
			}
			return nil, 0, fmt.Errorf("a claim its holder keeps renewing was acquired by another %s, %s after its last renewal was sent",
				label(round), gap)
		}
		if !errors.Is(err, guard.ErrHeld) {
			return nil, 0, fmt.Errorf("acquiring a claim its holder keeps renewing %s: got %w, want %w",
				label(round), err, guard.ErrHeld)
		}
	}
	return claim, 0, nil
}

// backendJumpBound is the most Backend plausibly advances in one renewal round.
func backendJumpBound(s Subject) time.Duration { return 10 * s.Expiry }

// overdue is how long after sent the backend's clock read at, and whether that is past
// expiry. Every sent reading here is taken before the RPC that restarts the countdown, so
// gap is never less than the real elapsed time since that restart — a genuine stall is never
// read as less overdue than it was. It is not bounded from above: jitter after the loss is
// found can still inflate gap past expiry while the real elapsed time was not, excusing a
// genuine early loss.
func overdue(sent, at guard.Instant, expiry time.Duration) (time.Duration, bool) {
	gap := at.Sub(sent)
	return gap, gap > expiry
}

// C4. Every call returns by its context's deadline, asserted from a watchdog of the
// test's own rather than from go test's timeout.
func everyCallIsBounded(t *testing.T, s Subject) {
	node, other := s.New(t, nil), s.New(t, nil)
	claim := acquire(t, s, node, manual("c4", "run-a"))
	elsewhere := acquire(t, s, other, manual("c4-elsewhere", "run-b"))
	t.Cleanup(func() { _ = elsewhere.Release(context.Background()) })

	within := func(what string, call func(context.Context) error) {
		t.Helper()
		if err := watched(t, 500*time.Millisecond, call); errors.Is(err, errUnbounded) {
			t.Errorf("%s had not returned %s after a deadline of %s", what, watchdogSlack, 500*time.Millisecond)
		}
	}
	acquireFresh := func(ctx context.Context) error {
		req := manual("c4-fresh", "run-c")
		req.Expiry = s.Expiry
		_, err := node.Acquire(ctx, req)
		return err
	}
	acquireHeld := func(ctx context.Context) error {
		req := manual("c4-elsewhere", "run-c")
		req.Expiry = s.Expiry
		_, err := node.Acquire(ctx, req)
		return err
	}
	releasedElsewhere := func(ctx context.Context) error { return node.Released(ctx, "c4-elsewhere") }

	if node.Hold == nil {
		// Nothing here can be held, so each call is bounded against what it could wait
		// on instead: a claim held elsewhere.
		within("Acquire of a held claim", acquireHeld)
		within("Released on a held claim", releasedElsewhere)
		within("Renew", claim.Renew)
		within("Fence", claim.Fence)
		within("Release", claim.Release)
		return
	}

	node.Hold()
	within("Acquire against a held backend", acquireFresh)
	within("Renew against a held backend", claim.Renew)
	within("Fence against a held backend", claim.Fence)
	within("Released against a held backend", releasedElsewhere)
	within("Release against a held backend", claim.Release)
}

// C5. A claim that has lapsed, or been taken over since, is lost — not a failure worth
// retrying until a deadline.
func lossIsDefinitive(t *testing.T, s Subject) {
	holder, contender := s.New(t, nil), s.New(t, nil)

	lapsed := acquire(t, s, holder, manual("c5", "run-a"))
	s.Lapse(t, lapsed)
	refusedAs(t, lapsed.Renew(bounded(t)), guard.ErrLost, "renewing a lapsed claim")
	refusedAs(t, lapsed.Fence(bounded(t)), guard.ErrLost, "fencing on a lapsed claim")

	superseded := acquire(t, s, holder, manual("c5-superseded", "run-a"))
	s.Lapse(t, superseded)
	successor := acquire(t, s, contender, manual("c5-superseded", "run-b"))
	refusedAs(t, superseded.Renew(bounded(t)), guard.ErrLost, "renewing a superseded claim")
	refusedAs(t, superseded.Fence(bounded(t)), guard.ErrLost, "fencing on a superseded claim")
	releaseClaim(t, successor)
}

// C6. A later claim's token is greater, and a fence passes only for the current claim.
func fencing(t *testing.T, s Subject) {
	a, b := s.New(t, nil), s.New(t, nil)

	first := acquire(t, s, a, manual("c6", "run-a"))
	if err := first.Fence(bounded(t)); err != nil {
		t.Fatalf("fencing on the current claim: %v", err)
	}
	s.Lapse(t, first)
	second := acquire(t, s, b, manual("c6", "run-b"))

	if second.Token() <= first.Token() {
		t.Fatalf("the later claim's token %d is not greater than the earlier one's %d", second.Token(), first.Token())
	}
	refusedAs(t, first.Fence(bounded(t)), guard.ErrLost, "fencing on a claim taken over since")
	if err := second.Fence(bounded(t)); err != nil {
		t.Fatalf("fencing on the current claim: %v", err)
	}
	releaseClaim(t, second)
}

// C7. A release frees the claim at once, well inside its expiry; releasing twice is not
// an error, and the second release does not free a claim someone else has taken since.
func release(t *testing.T, s Subject) {
	a, b := s.New(t, nil), s.New(t, nil)

	first := acquire(t, s, a, manual("c7", "run-a"))
	releaseClaim(t, first)
	second, err := try(t, s, b, manual("c7", "run-b"))
	if err != nil {
		t.Fatalf("a released claim was not acquirable at once: %v", err)
	}
	if err := first.Release(bounded(t)); err != nil {
		t.Fatalf("releasing a second time: %v", err)
	}
	_, err = try(t, s, a, manual("c7", "run-c"))
	refusedAs(t, err, guard.ErrHeld, "acquiring after a stale release")
	releaseClaim(t, second)
}

// C8. A refusal for rate leaves the claim exactly as it was: the slots fill, an
// acquisition against the full window is refused, and the claim it would have taken is
// still free for a contender that declares no limit.
func theRateSlotAndTheClaimAreOneStep(t *testing.T, s Subject) {
	node := s.New(t, nil)
	const limit = 2
	for i := range limit {
		claim := acquire(t, s, node, rated(manual("c8", fmt.Sprintf("run-%d", i)), limit, s.Expiry))
		releaseClaim(t, claim)
	}
	_, err := try(t, s, node, rated(manual("c8", "run-refused"), limit, s.Expiry))
	refusedAs(t, err, guard.ErrRateLimited, "acquiring when every rate slot is full")

	// The refusal above must have left the claim exactly as it was: with no limit
	// declared, a contender still takes it.
	after := acquire(t, s, s.New(t, nil), manual("c8", "run-after"))
	releaseClaim(t, after)
}

// C9. A slot does not free when the claim it was taken beside is released: only the
// window moving past it does, which none of these clauses waits long enough to see.
func theRateWindowIsTheBackends(t *testing.T, s Subject) {
	node := s.New(t, nil)
	const limit = 2
	for i := range limit {
		claim := acquire(t, s, node, rated(manual("c9", fmt.Sprintf("run-%d", i)), limit, s.Expiry))
		releaseClaim(t, claim)
	}
	_, err := try(t, s, node, rated(manual("c9", "run-refused"), limit, s.Expiry))
	refusedAs(t, err, guard.ErrRateLimited, "acquiring when the rate window is full though every claim was released")
}

// C10. Released returns once the claim is released, well before its own deadline.
func released(t *testing.T, s Subject) {
	holder, waiter := s.New(t, nil), s.New(t, nil)
	claim := acquire(t, s, holder, manual("c10", "run-a"))

	const deadline = 4 * time.Second
	ctx, cancel := context.WithTimeout(t.Context(), deadline)
	defer cancel()
	returned := make(chan time.Time, 1)
	go func() {
		_ = waiter.Released(ctx, "c10")
		returned <- time.Now()
	}()

	// Long enough for Released to have looked at all: releasing before it does leaves it
	// finding the claim already free, which is a correct answer for the wrong reason and
	// passes against an implementation that never notices anything afterwards.
	time.Sleep(500 * time.Millisecond)
	releasedAt := time.Now()
	releaseClaim(t, claim)
	select {
	case at := <-returned:
		if took := at.Sub(releasedAt); took > deadline/2 {
			t.Fatalf("Released returned %s after the release, with a deadline of %s", took, deadline)
		}
	case <-time.After(deadline + time.Second):
		t.Fatal("Released did not return by its deadline")
	}
}

// C11. A backend that cannot be reached, or does not answer in time, is unavailable and
// never held: the second would have a trigger wait on a claim nobody holds.
func unavailableIsNotHeld(t *testing.T, s Subject) {
	req := manual("c11", "run-a")
	req.Expiry = s.Expiry
	acquireFrom := func(c guard.Coordinator) func(context.Context) error {
		return func(ctx context.Context) error {
			_, err := c.Acquire(ctx, req)
			return err
		}
	}

	err := watched(t, time.Second, acquireFrom(s.Unreachable(t)))
	if errors.Is(err, guard.ErrHeld) || !errors.Is(err, guard.ErrUnavailable) {
		t.Fatalf("acquiring from a backend that cannot be reached: got %v, want %v", err, guard.ErrUnavailable)
	}

	node := s.New(t, nil)
	if node.Hold == nil {
		return
	}
	node.Hold()
	err = watched(t, 500*time.Millisecond, acquireFrom(node))
	if errors.Is(err, guard.ErrHeld) || !errors.Is(err, guard.ErrUnavailable) {
		t.Fatalf("acquiring from a backend that did not answer in time: got %v, want %v", err, guard.ErrUnavailable)
	}
}

// C12. A grant shorter than asked is refused and gives back what it got; a longer one is
// accepted and reported.
func theGrantedExpiry(t *testing.T, s Subject) {
	if s.ShortGrant != nil {
		_, err := try(t, s, s.ShortGrant(t), manual("c12", "run-a"))
		refusedAs(t, err, guard.ErrUnavailable, "acquiring with a grant shorter than asked")
		after := acquire(t, s, s.New(t, nil), manual("c12", "run-b"))
		releaseClaim(t, after)
	} else {
		t.Log("this backend cannot be made to grant less than asked; the adapter's comparison is shown able to fail by its own table test")
	}

	coordinator, asked, granted := s.LongGrant(t)
	req := manual("c12-long", "run-c")
	req.Expiry = asked
	claim := acquire(t, s, coordinator, req)
	if claim.Expiry() != granted {
		t.Fatalf("asked for %s and granted %s, the claim reports %s", asked, granted, claim.Expiry())
	}
	releaseClaim(t, claim)
}

// C13. One scheduled tick takes the claim at most once, whoever asks and in whatever
// order; a manual trigger neither reads the record nor advances it.
func oneTickOnce(t *testing.T, s Subject) {
	tick := time.Date(2026, 9, 10, 6, 0, 0, 0, time.UTC)
	a, b := s.New(t, nil), s.New(t, nil)

	first := acquire(t, s, a, scheduled("c13", "run-a", tick))
	releaseClaim(t, first)

	_, err := try(t, s, b, scheduled("c13", "run-b", tick))
	refusedAs(t, err, guard.ErrTickRan, "a tick that already ran")
	if !strings.Contains(err.Error(), "run-a") || !strings.Contains(err.Error(), "06:00:00Z") {
		t.Fatalf("the refusal does not name the recorded tick and its holder: %v", err)
	}
	_, err = try(t, s, b, scheduled("c13", "run-b", tick.Add(-time.Minute)))
	refusedAs(t, err, guard.ErrTickRan, "a tick before the one that ran")

	// The refusals left the claim as it was, and a manual trigger reads no record.
	byHand := acquire(t, s, b, manual("c13", "run-m"))
	_, err = try(t, s, a, scheduled("c13", "run-x", tick))
	refusedAs(t, err, guard.ErrHeld, "a tick that already ran, while the claim is held")
	releaseClaim(t, byHand)

	// The record is compared, not merely present: the next tick runs.
	next := acquire(t, s, b, scheduled("c13", "run-b", tick.Add(time.Minute)))
	releaseClaim(t, next)
	_, err = try(t, s, a, scheduled("c13", "run-a", tick.Add(time.Minute)))
	refusedAs(t, err, guard.ErrTickRan, "the next tick, once it ran")

	if !s.Seams {
		t.Log("this subject reads and writes the tick under one lock, so no interleaving puts a caller between the two")
		return
	}
	// Two calls in sequence never read before the other has written, so the suite
	// chooses the interleaving: B is held between its read and its transaction while A
	// takes the tick and lets it go.
	t.Run("with a tick recorded", func(t *testing.T) {
		earlier := acquire(t, s, a, scheduled("c13-recorded", "run-a", tick))
		releaseClaim(t, earlier)
		interleaved(t, s, "c13-recorded", tick.Add(time.Minute))
	})
	t.Run("with none recorded yet", func(t *testing.T) {
		interleaved(t, s, "c13-first", tick)
	})
}

func interleaved(t *testing.T, s Subject, name string, due time.Time) {
	t.Helper()
	var reached sync.Once
	atSeam, proceed := make(chan struct{}), make(chan struct{})
	b := s.New(t, func(ctx context.Context, _ string) {
		reached.Do(func() { close(atSeam) })
		select {
		case <-proceed:
		case <-ctx.Done():
		}
	})
	a := s.New(t, nil)

	result := make(chan error, 1)
	go func() {
		claim, err := try(t, s, b, scheduled(name, "run-b", due))
		if err == nil {
			_ = claim.Release(context.Background())
		}
		result <- err
	}()
	select {
	case <-atSeam:
	case <-time.After(callBound):
		t.Fatal("B never reached the seam between its read and its transaction")
	}

	taken := acquire(t, s, a, scheduled(name, "run-a", due))
	releaseClaim(t, taken)
	close(proceed)

	select {
	case err := <-result:
		refusedAs(t, err, guard.ErrTickRan, "a tick taken and let go while B was between its read and its transaction")
	case <-time.After(callBound):
		t.Fatal("B did not return")
	}
}

// C14. The tick recorded and compared is the one the request carries, never a clock
// reading: ticks decades before any clock here are taken and refused as ticks.
func theTickIsTheOneRequested(t *testing.T, s Subject) {
	long := time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC)
	a, b := s.New(t, nil), s.New(t, nil)

	first := acquire(t, s, a, scheduled("c14", "run-a", long))
	releaseClaim(t, first)
	next, err := try(t, s, b, scheduled("c14", "run-b", long.Add(time.Minute)))
	if err != nil {
		t.Fatalf("the tick after one decades old was refused, as if a clock reading had been recorded: %v", err)
	}
	releaseClaim(t, next)
	_, err = try(t, s, a, scheduled("c14", "run-c", long.Add(time.Minute)))
	refusedAs(t, err, guard.ErrTickRan, "a decades-old tick that already ran, which a clock reading would let through")
}
