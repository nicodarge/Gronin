package guardtest

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
)

// Fake is an in-memory claim backend: the runtime's belief about etcd, held to the same
// contract as the real adapter so that the two cannot drift apart unnoticed.
//
// It judges expiry on a clock of its own, never the runtime's (C3): the runtime's clock is
// only ever read to stamp when a claim was taken, for a reader. Each host reaches it
// through a FakeHost of its own, so one holder can lose the backend while a contender
// still reaches it (SC-103).
type Fake struct {
	clock   *Clock
	runtime guard.Clock

	mu       sync.Mutex
	revision int64
	claims   map[string]*fakeClaim
	ticks    map[string]fakeTick
	slots    map[string][]fakeSlot
	changed  chan struct{}
}

// fakeSlot is one rate slot in flight: taken with the claim, and free again once its own
// expiry passes on the backend's clock — never when the claim it was taken beside is
// released (C9).
type fakeSlot struct {
	expires guard.Instant
}

type fakeClaim struct {
	token   int64 // the revision that created it
	holder  guard.Holder
	taken   time.Time
	granted time.Duration
	expires guard.Instant
}

type fakeTick struct {
	dueAt    time.Time
	holder   guard.Holder
	revision int64
}

// NewFake returns a backend judging expiry on backend. runtime is the runtime's clock,
// kept apart from it.
func NewFake(backend *Clock, runtime guard.Clock) *Fake {
	return &Fake{
		clock: backend, runtime: runtime,
		claims: map[string]*fakeClaim{}, ticks: map[string]fakeTick{},
		slots:   map[string][]fakeSlot{},
		changed: make(chan struct{}),
	}
}

// Host returns a view of the backend with switches of its own. seam, when set, is
// called between reading a name's last tick and taking the claim (C13).
func (f *Fake) Host(seam guard.Seam) *FakeHost {
	return &FakeHost{fake: f, seam: seam, grant: func(asked time.Duration) time.Duration { return asked }}
}

// Expire ends a claim now, as its expiry would, without its holder knowing.
func (f *Fake) Expire(claim guard.Claim) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for name, live := range f.claims {
		if live.token == claim.Token() {
			delete(f.claims, name)
			f.notifyLocked()
		}
	}
}

// liveLocked is the claim on name if it has not lapsed on the backend's clock. Called
// with mu held.
func (f *Fake) liveLocked(name string) *fakeClaim {
	claim, found := f.claims[name]
	if !found {
		return nil
	}
	if !f.clock.Monotonic().Before(claim.expires) {
		delete(f.claims, name)
		f.notifyLocked()
		return nil
	}
	return claim
}

// pruneSlotsLocked drops name's rate slots whose expiry has passed on the backend's clock
// (C9): a slot frees on its own, never because the claim it was taken beside was released.
func (f *Fake) pruneSlotsLocked(name string) {
	live := f.slots[name][:0]
	for _, slot := range f.slots[name] {
		if f.clock.Monotonic().Before(slot.expires) {
			live = append(live, slot)
		}
	}
	f.slots[name] = live
}

// rateFullLocked reports whether every one of limit's slots for name is taken.
func (f *Fake) rateFullLocked(name string, limit *guard.RateLimit) bool {
	f.pruneSlotsLocked(name)
	return len(f.slots[name]) >= limit.Runs
}

// takeRateSlotLocked takes one of name's rate slots, in the same critical section as the
// claim (C8): a caller refused between the check above and here would leave the claim
// exactly as it was, because both happen under the same lock.
func (f *Fake) takeRateSlotLocked(name string, limit *guard.RateLimit) {
	f.slots[name] = append(f.slots[name], fakeSlot{expires: f.clock.Monotonic().Add(limit.Per)})
}

func (f *Fake) notifyLocked() {
	close(f.changed)
	f.changed = make(chan struct{})
}

// FakeHost is one host's connection to a Fake. It implements guard.Coordinator.
type FakeHost struct {
	fake *Fake
	seam guard.Seam

	mu      sync.Mutex
	held    bool
	severed bool
	grant   func(asked time.Duration) time.Duration
	// watching is called each time Released begins to wait on the fake's notification.
	watching func(name string)
	// rateSeam is called between the first rate-slot check and the commit that would take
	// one, for any request that declares a limit — unlike guard.Seam, whether or not the
	// request is scheduled. Nil outside the test that exercises that exact window (C8).
	rateSeam func(ctx context.Context, name string)
}

// OnRateCheck sets what Acquire calls between checking a name's rate slots and committing
// one, so a test can hold one caller there while a second one takes the last slot — the
// same race a caller reading the check under one lock and committing under a later one
// would otherwise only hit by chance.
func (h *FakeHost) OnRateCheck(hook func(ctx context.Context, name string)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.rateSeam = hook
}

var _ guard.Coordinator = (*FakeHost)(nil)

// Hold makes every call through this host wait until its context ends and then fail,
// as a backend that stopped answering. A held call never completes late: that is what
// tells a hang from a slow success.
func (h *FakeHost) Hold() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.held = true
}

// Sever makes every call through this host fail at once.
func (h *FakeHost) Sever() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.severed = true
}

// OnWatch sets what Released calls each time it begins to wait on the fake's own
// notification, which is how the contract suite knows it is waiting (C10).
func (h *FakeHost) OnWatch(watching func(name string)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.watching = watching
}

func (h *FakeHost) watched(name string) {
	h.mu.Lock()
	watching := h.watching
	h.mu.Unlock()
	if watching != nil {
		watching(name)
	}
}

// Resume undoes Hold and Sever.
func (h *FakeHost) Resume() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.held, h.severed = false, false
}

// Grant makes the backend grant what grant returns for what is asked, rather than what
// is asked (C12).
func (h *FakeHost) Grant(grant func(asked time.Duration) time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.grant = grant
}

// reach is the round trip every call makes.
func (h *FakeHost) reach(ctx context.Context) error {
	h.mu.Lock()
	held, severed := h.held, h.severed
	h.mu.Unlock()
	if severed {
		return fmt.Errorf("%w: the fake is severed from this host", guard.ErrUnavailable)
	}
	if held {
		<-ctx.Done()
		return fmt.Errorf("%w: the fake held its answer: %w", guard.ErrUnavailable, ctx.Err())
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %w", guard.ErrUnavailable, err)
	}
	return nil
}

// Reach implements guard.Coordinator: the fake stands for a shared backend.
func (h *FakeHost) Reach() string { return guard.ReachCrossHost }

// Acquire implements guard.Coordinator, in the adapter's order: grant, read, decide, and
// a transaction that takes the claim only if nothing it read has changed.
func (h *FakeHost) Acquire(ctx context.Context, req guard.AcquireRequest) (guard.Claim, error) {
	if err := req.Check(); err != nil {
		return nil, err
	}
	if req.Expiry <= 0 || req.Expiry%time.Second != 0 {
		return nil, fmt.Errorf("claim expiry %s is not a whole number of seconds", req.Expiry)
	}
	if err := h.reach(ctx); err != nil {
		return nil, err
	}
	h.mu.Lock()
	granted := h.grant(req.Expiry)
	h.mu.Unlock()
	if granted < req.Expiry {
		return nil, fmt.Errorf("%w: granted an expiry of %s, asked for %s", guard.ErrUnavailable, granted, req.Expiry)
	}
	scheduled := req.Trigger.Kind == guard.KindSchedule
	f := h.fake

	for {
		f.mu.Lock()
		read, recorded := f.ticks[req.Name]
		err := decide(f.liveLocked(req.Name), read, recorded, req)
		if err == nil && req.Rate != nil && f.rateFullLocked(req.Name, req.Rate) {
			// C13: when the tick already ran as well, that refusal names the run rather
			// than the limit, so it is decided first, above.
			err = guard.RateLimitedBy(*req.Rate)
		}
		f.mu.Unlock()
		if err != nil {
			return nil, err
		}

		if scheduled && h.seam != nil {
			h.seam(ctx, req.Name)
		}
		h.mu.Lock()
		rateSeam := h.rateSeam
		h.mu.Unlock()
		if req.Rate != nil && rateSeam != nil {
			rateSeam(ctx, req.Name)
		}
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("%w: %w", guard.ErrUnavailable, err)
		}

		f.mu.Lock()
		current := f.ticks[req.Name]
		if f.liveLocked(req.Name) != nil || (scheduled && current.revision != read.revision) ||
			(req.Rate != nil && f.rateFullLocked(req.Name, req.Rate)) {
			// Overtaken between the read and the transaction — by another claim, another
			// tick, or another caller that took the rate slot this one was about to: read
			// and decide again. Two callers racing the last slot both pass the check above
			// before either commits, so the check has to run again here, under the same
			// lock the commit itself takes (C8) — otherwise both take the claim.
			f.mu.Unlock()
			continue
		}
		f.revision++
		claim := &fakeClaim{
			token: f.revision, holder: req.Holder, taken: f.runtime.Wall(),
			granted: granted, expires: f.clock.Monotonic().Add(granted),
		}
		f.claims[req.Name] = claim
		if req.Rate != nil {
			// The rate slot and the claim are one step (C8): both are decided under the
			// same lock a refused caller never got past.
			f.takeRateSlotLocked(req.Name, req.Rate)
		}
		if scheduled {
			f.ticks[req.Name] = fakeTick{dueAt: req.Trigger.DueAt, holder: req.Holder, revision: f.revision}
		}
		f.notifyLocked()
		f.mu.Unlock()
		return &fakeHeld{host: h, name: req.Name, token: claim.token, granted: granted}, nil
	}
}

// decide refuses from what was read. A held claim is named whatever the tick record
// says: the holder is what an operator needs to see.
func decide(live *fakeClaim, tick fakeTick, recorded bool, req guard.AcquireRequest) error {
	if live != nil {
		return guard.HeldBy(live.holder)
	}
	if req.Trigger.Kind == guard.KindSchedule && recorded && !req.Trigger.DueAt.After(tick.dueAt) {
		return guard.TickRanAs(tick.dueAt, tick.holder)
	}
	return nil
}

// Released implements guard.Coordinator.
func (h *FakeHost) Released(ctx context.Context, name string) error {
	if err := h.reach(ctx); err != nil {
		return err
	}
	f := h.fake
	for {
		f.mu.Lock()
		free := f.liveLocked(name) == nil
		changed := f.changed
		f.mu.Unlock()
		if free {
			return nil
		}
		h.watched(name)
		select {
		case <-changed:
		case <-f.clock.Changed():
		case <-ctx.Done():
			return fmt.Errorf("%w: %w", guard.ErrUnavailable, ctx.Err())
		}
	}
}

type fakeHeld struct {
	host    *FakeHost
	name    string
	token   int64
	granted time.Duration
}

func (c *fakeHeld) Token() int64          { return c.token }
func (c *fakeHeld) Expiry() time.Duration { return c.granted }

// current is the claim this handle holds if it is still the live one on its name.
func (c *fakeHeld) currentLocked() *fakeClaim {
	live := c.host.fake.liveLocked(c.name)
	if live == nil || live.token != c.token {
		return nil
	}
	return live
}

func (c *fakeHeld) Renew(ctx context.Context) error {
	if err := c.host.reach(ctx); err != nil {
		return err
	}
	f := c.host.fake
	f.mu.Lock()
	defer f.mu.Unlock()
	live := c.currentLocked()
	if live == nil {
		return fmt.Errorf("%w: %s", guard.ErrLost, c.name)
	}
	live.expires = f.clock.Monotonic().Add(live.granted)
	return nil
}

func (c *fakeHeld) Fence(ctx context.Context) error {
	if err := c.host.reach(ctx); err != nil {
		return err
	}
	f := c.host.fake
	f.mu.Lock()
	defer f.mu.Unlock()
	if c.currentLocked() == nil {
		return fmt.Errorf("%w: %s is no longer held by token %d", guard.ErrLost, c.name, c.token)
	}
	return nil
}

func (c *fakeHeld) Release(ctx context.Context) error {
	if err := c.host.reach(ctx); err != nil {
		return err
	}
	f := c.host.fake
	f.mu.Lock()
	defer f.mu.Unlock()
	if c.currentLocked() != nil {
		delete(f.claims, c.name)
		f.notifyLocked()
	}
	return nil
}
