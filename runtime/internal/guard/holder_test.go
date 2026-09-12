package guard_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
	"github.com/nicodarge/Gronin/runtime/internal/guard/guardtest"
	"github.com/nicodarge/Gronin/runtime/internal/playbook"
	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// start is when every injected clock in these tests begins.
var start = time.Date(2026, 9, 10, 6, 0, 0, 0, time.UTC)

// testConfig is research.md §3's claim set: the stop deadline it gives falls 18s after
// the last renewal that succeeded was sent.
func testConfig() guard.Config { return guard.DefaultConfig() }

// book is a playbook with no guard block, which is still held to non-concurrency
// (FR-120).
func book(name string) *playbook.Playbook { return &playbook.Playbook{Name: name} }

// newStore is a record store of its own, as each host has.
func newStore(t *testing.T) *record.Store {
	t.Helper()
	store, err := record.Open(t.Context(), filepath.Join(t.TempDir(), "record"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// roundTrip is how long the backend takes to answer on the runtime's own clock. It is
// what separates the instant a renewal was sent from the instant it was answered, which
// is the whole of R1.
const roundTrip = time.Second

// watchedHost is a coordinator that says when each renewal attempt is over, and whose
// answer takes `answersIn` on the runtime's clock.
type watchedHost struct {
	*guardtest.FakeHost
	clock     *guardtest.Clock
	answersIn time.Duration
	attempted chan struct{}
}

func watching(fake *guardtest.Fake, clock *guardtest.Clock, answersIn time.Duration) *watchedHost {
	return &watchedHost{
		FakeHost: fake.Host(nil), clock: clock, answersIn: answersIn,
		attempted: make(chan struct{}, 16),
	}
}

func (c *watchedHost) Acquire(ctx context.Context, req guard.AcquireRequest) (guard.Claim, error) {
	held, err := c.FakeHost.Acquire(ctx, req)
	if err != nil {
		return nil, err
	}
	return &watchedClaim{Claim: held, host: c}, nil
}

type watchedClaim struct {
	guard.Claim
	host *watchedHost
}

func (c *watchedClaim) Renew(ctx context.Context) error {
	err := c.Claim.Renew(ctx)
	if err == nil && c.host.answersIn > 0 {
		c.host.clock.Advance(c.host.answersIn)
	}
	c.host.attempted <- struct{}{}
	return err
}

// waitForAttempt returns once the renewal loop has finished its next attempt, so the
// test moves the clock only between attempts and never inside one.
func (c *watchedHost) waitForAttempt(t *testing.T) {
	t.Helper()
	select {
	case <-c.attempted:
	case <-time.After(10 * time.Second):
		t.Fatal("the renewal loop made no further attempt")
	}
}

// SC-103: a holder that loses its backend stops its run before the claim can lapse, at
// the instant R1 computes and not a tick later, and a contender takes the claim only
// once both have happened.
func TestHolderStopsAtTheDeadlineItComputed(t *testing.T) {
	var (
		backend = guardtest.NewClock(start)
		runtime = guardtest.NewClock(start)
		fake    = guardtest.NewFake(backend, runtime)
		cfg     = testConfig()
	)
	holderHost := watching(fake, runtime, roundTrip)
	held := &guard.Guard{
		Coordinator: holderHost, Store: newStore(t), Config: cfg, Clock: runtime,
		Host: "host-a.example.com", Instance: "instance-a",
	}
	contender := &guard.Guard{
		Coordinator: fake.Host(nil), Store: newStore(t), Config: cfg, Clock: runtime,
		Host: "host-b.example.com", Instance: "instance-b",
	}

	admitted, err := held.Admit(t.Context(), book("drift-check"), guard.Request{
		RunID: "run-a", Kind: record.TriggerManual,
	})
	if err != nil {
		t.Fatalf("taking the claim: %v", err)
	}

	stoppedAt := make(chan guard.Instant, 1)
	hold := held.Hold(admitted, func() { stoppedAt <- runtime.Monotonic() })
	t.Cleanup(hold.Done)

	// The advance that carries the whole arithmetic: the renewal at 5s succeeds and is
	// answered at 6s, so a deadline anchored on the answer would fall a second later
	// than the one anchored on the send.
	advance(t, runtime, backend, 5*time.Second)
	holderHost.waitForAttempt(t)

	holderHost.Sever()

	// A backward step of the wall reading changes nothing: the deadline is on the
	// monotonic reading (FR-118).
	runtime.StepWall(-time.Hour)

	for _, at := range []time.Duration{10, 15, 20} {
		advanceTo(t, runtime, backend, at*time.Second)
		holderHost.waitForAttempt(t)
	}
	advanceTo(t, runtime, backend, 22900*time.Millisecond)
	if hold.Stopped() {
		t.Fatalf("the run was stopped at %s, before the deadline at 23s", runtime.Monotonic().Sub(guard.InstantAt(0)))
	}
	// While A has not stopped, B is refused — the claim has not lapsed.
	if _, err := contender.Admit(t.Context(), book("drift-check"), guard.Request{
		RunID: "run-b", Kind: record.TriggerManual,
	}); !refusedWith(err, record.MechanismClaimHeld) {
		t.Fatalf("a contender was not refused while the claim was held: %v", err)
	}

	advanceTo(t, runtime, backend, 23*time.Second)
	select {
	case at := <-stoppedAt:
		if got := at.Sub(guard.InstantAt(0)); got < 23*time.Second || got > 24*time.Second {
			t.Fatalf("the run was stopped at %s, not at the 23s deadline", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the run was never stopped, so its claim would have lapsed under it")
	}
	if !hold.Stopped() {
		t.Fatal("the hold does not report having stopped the run, so it would be recorded failed")
	}

	// The run ends. A holder severed from its backend cannot tell it so, which is the
	// case that matters: the claim lapses on the backend's own clock, and the contender
	// gets it only then — after the first run was already over.
	hold.Done()
	_ = admitted.Claim.Release(t.Context())
	backend.Advance(cfg.ClaimExpiry)
	if _, err := contender.Admit(t.Context(), book("drift-check"), guard.Request{
		RunID: "run-b", Kind: record.TriggerManual,
	}); err != nil {
		t.Fatalf("the contender was refused once the claim had lapsed: %v", err)
	}
}

// R2: a claim reported lost stops the run at once rather than at the deadline.
func TestHolderStopsAtOnceOnALostClaim(t *testing.T) {
	var (
		backend = guardtest.NewClock(start)
		runtime = guardtest.NewClock(start)
		fake    = guardtest.NewFake(backend, runtime)
	)
	host := watching(fake, runtime, 0)
	subject := &guard.Guard{
		Coordinator: host, Store: newStore(t), Config: testConfig(), Clock: runtime,
		Host: "host-a.example.com", Instance: "instance-a",
	}

	admitted, err := subject.Admit(t.Context(), book("drift-check"), guard.Request{
		RunID: "run-a", Kind: record.TriggerManual,
	})
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan struct{}, 1)
	hold := subject.Hold(admitted, func() { stopped <- struct{}{} })
	t.Cleanup(hold.Done)

	// Expired out of band, as its lease lapsing would do. The next renewal says so, and
	// the deadline is still 13 seconds away.
	fake.Expire(admitted.Claim)
	advance(t, runtime, backend, 5*time.Second)

	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("a claim reported lost did not stop the run")
	}
	if at := runtime.Monotonic().Sub(guard.InstantAt(0)); at > 10*time.Second {
		t.Fatalf("the run was stopped at %s, which is the deadline rather than at once", at)
	}
}

// advance moves both clocks forward by d.
func advance(t *testing.T, runtime, backend *guardtest.Clock, d time.Duration) {
	t.Helper()
	waitForTimer(t, runtime)
	runtime.Advance(d)
	backend.Advance(d)
}

// advanceTo moves both clocks to d after their origin. The renewal loop's timers are set
// at absolute instants, so a test that arrives late still fires them where they were.
func advanceTo(t *testing.T, runtime, backend *guardtest.Clock, d time.Duration) {
	t.Helper()
	waitForTimer(t, runtime)
	elapsed := runtime.Monotonic().Sub(guard.InstantAt(0))
	if d <= elapsed {
		t.Fatalf("the clock is already at %s, past %s", elapsed, d)
	}
	runtime.Advance(d - elapsed)
	backend.Advance(d - elapsed)
}

func waitForTimer(t *testing.T, clock *guardtest.Clock) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := clock.WaitForTimers(ctx, 1); err != nil {
		t.Fatalf("nothing was waiting on the clock: %v", err)
	}
}

// refusedWith reports whether err is the guard's refusal under the mechanism given.
func refusedWith(err error, mechanism record.Mechanism) bool {
	var refused *guard.Refused
	return errors.As(err, &refused) && refused.Mechanism == mechanism
}
