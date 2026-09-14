package guardtest

import (
	"context"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
)

// capturing keeps the last claim its coordinator handed out, so a test can lapse the claim
// renewing is holding.
type capturing struct {
	guard.Coordinator
	claim guard.Claim
}

func (c *capturing) Acquire(ctx context.Context, req guard.AcquireRequest) (guard.Claim, error) {
	claim, err := c.Coordinator.Acquire(ctx, req)
	c.claim = claim
	return claim, err
}

type renewal struct {
	backend *Clock
	fake    *Fake
	holder  *capturing
	subject Subject
}

const renewalExpiry = 30 * time.Second

func newRenewal() renewal {
	start := time.Date(2026, 9, 10, 6, 0, 0, 0, time.UTC)
	backend := NewClock(start)
	fake := NewFake(backend, NewClock(start))
	return renewal{
		backend: backend,
		fake:    fake,
		holder:  &capturing{Coordinator: fake.Host(nil)},
		subject: Subject{Expiry: renewalExpiry, Backend: backend.Monotonic},
	}
}

func onTheFake(int) string { return "on the fake" }

// A host stalled past the expiry between two renewals, made on the fake's clock so that it
// happens on every run: the lapse it causes is the backend being right, and the attempt
// reports it rather than failing.
func TestAStallPastTheExpiryIsNotTheBackends(t *testing.T) {
	r := newRenewal()
	calls := 0
	claim, gap, err := renewing(t, r.subject, r.holder, r.fake.Host(nil), "stalled", 4, func() {
		calls++
		step := renewalExpiry / 4
		if calls == 2 {
			step += renewalExpiry
		}
		r.backend.Advance(step)
	}, onTheFake)
	if err != nil {
		t.Fatalf("a claim lapsed by a stall past its expiry was judged the backend's: %v", err)
	}
	if claim != nil || gap != renewalExpiry+renewalExpiry/4 {
		t.Fatalf("claim = %v, gap = %s; want no claim and a gap of %s", claim, gap, renewalExpiry+renewalExpiry/4)
	}

	again, _, err := renewing(t, r.subject, r.holder, r.fake.Host(nil), "stalled", 4,
		func() { r.backend.Advance(renewalExpiry / 4) }, onTheFake)
	if err != nil || again == nil {
		t.Fatalf("the attempt after the stall: claim = %v, err = %v", again, err)
	}
}

// A claim lost well inside its expiry is the backend's fault, however the attempt got there.
func TestALossWithinTheExpiryIsTheBackends(t *testing.T) {
	r := newRenewal()
	calls := 0
	claim, _, err := renewing(t, r.subject, r.holder, r.fake.Host(nil), "dropped", 4, func() {
		calls++
		if calls == 2 {
			r.fake.Expire(r.holder.claim)
		}
		r.backend.Advance(renewalExpiry / 4)
	}, onTheFake)
	if err == nil {
		t.Fatalf("a claim lost %s after its last renewal, with an expiry of %s, was not judged the backend's (claim = %v)",
			renewalExpiry/4, renewalExpiry, claim)
	}
}

// delayedAck advances backend right after the underlying Acquire returns, standing in for a
// stall in the trip back to the caller: the grant itself lands at the earlier instant, but a
// caller that reads Backend only after Acquire returns sees the later one instead.
type delayedAck struct {
	guard.Coordinator
	backend *Clock
	delay   time.Duration
}

func (d *delayedAck) Acquire(ctx context.Context, req guard.AcquireRequest) (guard.Claim, error) {
	claim, err := d.Coordinator.Acquire(ctx, req)
	d.backend.Advance(d.delay)
	return claim, err
}

// A stall in the grant's own return trip must not make the claim look younger than it is: sent
// has to be read before Acquire is called, not after it returns, or a loss found right after is
// misjudged not the backend's.
func TestAStallInTheGrantsReturnTripIsStillTheBackends(t *testing.T) {
	r := newRenewal()
	holder := &capturing{Coordinator: &delayedAck{Coordinator: r.fake.Host(nil), backend: r.backend, delay: 2 * renewalExpiry}}
	claim, _, err := renewing(t, r.subject, holder, r.fake.Host(nil), "grant-stall", 1,
		func() { r.fake.Expire(holder.claim) }, onTheFake)
	if err != nil {
		t.Fatalf("a claim lost right after a stalled grant was not judged the backend's: %v", err)
	}
	if claim != nil {
		t.Fatalf("claim = %v, want none: a claim expired since a stalled grant must be lost", claim)
	}
}

// jumpBy returns a Backend func whose reading advances by step every call, whatever else
// moves: a stand-in for a Backend wired to a clock other than the one the backend judges
// expiry on.
func jumpBy(step time.Duration) func() guard.Instant {
	var at guard.Instant
	return func() guard.Instant {
		at = at.Add(step)
		return at
	}
}

// A Backend that jumps beyond backendJumpBound in one round is excused like a stall, not
// failed outright: a real freeze that big is retried rather than hard-failing the clause on
// the spot, the way a Backend wired to the wrong clock — which jumps the same way on every
// attempt — still does once every attempt is spent.
func TestABackendJumpBeyondTheBoundIsExcusedLikeAStall(t *testing.T) {
	r := newRenewal()
	subject := r.subject
	subject.Backend = jumpBy(100 * renewalExpiry)
	claim, gap, err := renewing(t, subject, r.holder, r.fake.Host(nil), "jump", 1, func() {}, onTheFake)
	if err != nil {
		t.Fatalf("a Backend jump beyond the bound was not excused like a stall: %v", err)
	}
	if claim != nil {
		t.Fatalf("claim = %v, want none: a Backend jump beyond the bound must restart the attempt", claim)
	}
	if gap < 100*renewalExpiry {
		t.Fatalf("gap = %s, want at least the jump of %s", gap, 100*renewalExpiry)
	}
}

// lostOnRelease reports ErrLost from Release, which C5 does not forbid: it only obliges Renew
// and Fence to do that on a lapsed claim, and C7 only promises a second release is not an error.
type lostOnRelease struct{ guard.Claim }

func (c *lostOnRelease) Release(context.Context) error { return guard.ErrLost }

// releasingAsLost wraps every claim its underlying coordinator hands out in lostOnRelease.
type releasingAsLost struct{ guard.Coordinator }

func (c *releasingAsLost) Acquire(ctx context.Context, req guard.AcquireRequest) (guard.Claim, error) {
	claim, err := c.Coordinator.Acquire(ctx, req)
	if err != nil {
		return nil, err
	}
	return &lostOnRelease{Claim: claim}, nil
}

// A subject whose Release reports ErrLost on the claim a Backend jump excuses must not turn
// that excuse into a hard failure.
func TestAnExcusedClaimsReleaseMayAlreadyReportItLost(t *testing.T) {
	r := newRenewal()
	holder := &capturing{Coordinator: &releasingAsLost{Coordinator: r.fake.Host(nil)}}
	subject := r.subject
	subject.Backend = jumpBy(100 * renewalExpiry)
	claim, _, err := renewing(t, subject, holder, r.fake.Host(nil), "jump-lost-release", 1, func() {}, onTheFake)
	if err != nil {
		t.Fatalf("a Backend jump beyond the bound, released as already lost, was not excused: %v", err)
	}
	if claim != nil {
		t.Fatalf("claim = %v, want none: a Backend jump beyond the bound must restart the attempt", claim)
	}
}
