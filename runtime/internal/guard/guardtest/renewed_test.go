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
