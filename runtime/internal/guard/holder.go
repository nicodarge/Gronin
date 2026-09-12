package guard

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// StopBoundExceeded is this process's exit code when a run it decided to stop is still
// going a stop bound later (R4). The cost is the whole process, not the one run: every
// other run on this host ends with it, and the next start marks them interrupted.
const StopBoundExceeded = 3

// Hold is the runtime's side of holding a claim: one renewal attempt per interval, each
// bounded (FR-122), and a deadline at which a run whose claim can no longer be proven
// held is stopped before that claim can lapse (FR-105).
type Hold struct {
	guard *Guard
	claim Claim
	// stop cancels the run's context, which kills its child process group.
	stop func()

	sent Instant
	// over is closed when the run has ended, whatever its outcome.
	over chan struct{}
	// deciding guards the one stop decision.
	deciding sync.Once
	stopped  atomic.Bool
	reason   atomic.Pointer[string]
	ended    sync.Once
}

// Hold starts the renewal loop for an admitted claim. stop is called once, if the
// runtime decides the run can no longer prove it holds the claim; the caller calls Done
// when the run is over.
func (g *Guard) Hold(admitted *Admitted, stop func()) *Hold {
	h := &Hold{
		guard: g, claim: admitted.Claim, stop: stop,
		sent: admitted.sent, over: make(chan struct{}),
	}
	// A claim that does not expire ends with the process holding it: there is nothing to
	// renew and no deadline to keep. That is the single-host reach of FR-109.
	if admitted.Claim == nil || admitted.Claim.Expiry() == 0 {
		return h
	}
	go h.renew()
	return h
}

// Done says the run is over. It stops the renewal loop and the stop bound's enforcement.
func (h *Hold) Done() { h.ended.Do(func() { close(h.over) }) }

// Stopped reports whether this hold is what ended the run, which is the difference
// between a run recorded claim_lost and one recorded failed.
func (h *Hold) Stopped() bool { return h.stopped.Load() }

// Reason is why the run was stopped, empty while it has not been.
func (h *Hold) Reason() string {
	if why := h.reason.Load(); why != nil {
		return *why
	}
	return ""
}

// now is the reading the stop deadline is measured on: the monotonic one, because time
// synchronisation can step the wall reading backwards and a deadline measured on it is
// lengthened by exactly the step (FR-118).
func (h *Hold) now() Instant { return h.guard.clock().Monotonic() }

// renew is the loop. One attempt per renewal interval, never two in flight (FR-122),
// and a stop decision at `sent + expiry − stop bound − margin floor` (R1).
func (h *Hold) renew() {
	cfg := h.guard.Config
	expiry := h.claim.Expiry()
	sent, attempt := h.sent, h.sent

	for {
		deadline := cfg.StopDeadline(sent, expiry)
		now := h.now()
		if !now.Before(deadline) {
			h.decideToStop("the claim could not be renewed in time to prove it is still held")
			return
		}

		wait := min(attempt.Add(cfg.RenewEvery).Sub(now), deadline.Sub(now))
		timer := h.guard.clock().NewTimer(wait)
		select {
		case <-timer.C():
		case <-h.over:
			timer.Stop()
			return
		}

		attempt = h.now()
		err := h.attempt()
		switch {
		case err == nil:
			// R1: the instant the attempt was SENT, not the instant it was answered. The
			// backend's countdown started no earlier than the send, and taking the reply
			// instead moves the deadline later by a round trip.
			sent = attempt
		case errors.Is(err, ErrLost):
			// R2: a lost claim is not one more failed attempt. There is nothing left to
			// wait for, and the deadline is time the run does not have.
			h.decideToStop("the claim is gone: " + err.Error())
			return
		}
	}
}

// attempt is one renewal, bounded (FR-122). A bound is what tells a renewal that hangs
// from one that succeeded: without it the attempt stays outstanding until the claim
// lapses, which is the window FR-105 exists to close.
func (h *Hold) attempt() error {
	ctx, cancel := bound(context.Background(), h.guard.clock(), h.guard.Config.RenewBound)
	defer cancel()
	return h.claim.Renew(ctx)
}

// Fence is R3: asked before the agent stage starts and before each sink delivers. A
// fence that fails for any reason stops the run, and the side effect does not happen.
func (h *Hold) Fence(ctx context.Context) error {
	if h.claim == nil {
		return nil
	}
	fence, cancel := bound(ctx, h.guard.clock(), h.guard.Config.RenewBound)
	defer cancel()
	if err := h.claim.Fence(fence); err != nil {
		h.decideToStop("the claim could not be fenced: " + err.Error())
		return err
	}
	return nil
}

// decideToStop cancels the run and starts R4's enforcement. It happens once: a second
// decision would start a second exit timer on a run already ending.
func (h *Hold) decideToStop(why string) {
	h.deciding.Do(func() {
		h.reason.Store(&why)
		h.stopped.Store(true)
		h.guard.log().Warn("stopping a run that can no longer prove it holds its claim",
			"reason", why, "stop_bound", h.guard.Config.StopBound.String())
		h.stop()
		go h.enforce()
	})
}

// enforce is R4. Cancelling a context cannot end work that ignores it, and ending this
// process is the only thing left that can — a run still going when its claim lapses is
// FR-101 broken while the record reports it held.
func (h *Hold) enforce() {
	timer := h.guard.clock().NewTimer(h.guard.Config.StopBound)
	defer timer.Stop()
	select {
	case <-h.over:
	case <-timer.C():
		h.guard.exit()
	}
}

func (g *Guard) exit() {
	if g.Exit != nil {
		g.Exit(StopBoundExceeded)
		return
	}
	os.Exit(StopBoundExceeded)
}

// StopDeadline is where a run's stop decision falls for a claim last proven held at
// sent, which is what a test asserts against to the tick (R1).
func (c Config) StopDeadline(sent Instant, expiry time.Duration) Instant {
	return sent.Add(expiry - c.StopBound - MarginFloor)
}
