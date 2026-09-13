// Package guardtest holds what the guard's tests run against: a fake Coordinator, the
// contract every Coordinator is held to, an etcd server embedded in the test process, and
// a proxy that can hold or sever the connection to it. It is imported by _test.go files
// only; the shipped binary linking any of it is what TestTheBinaryLinksNoEtcdServer
// refuses.
package guardtest

import (
	"context"
	"sync"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
)

// Clock is a guard.Clock the test moves by hand. Its wall reading can be stepped on its
// own, in either direction, without moving the monotonic one — which is the one thing a
// test cannot do to the host's clock and the one a deadline has to survive.
type Clock struct {
	mu      sync.Mutex
	mono    time.Duration
	wall    time.Time
	timers  []*timer
	set     int
	changed chan struct{}
}

// NewClock returns a clock whose wall reading starts at wall.
func NewClock(wall time.Time) *Clock {
	return &Clock{wall: wall.UTC(), changed: make(chan struct{})}
}

// Monotonic implements guard.Clock.
func (c *Clock) Monotonic() guard.Instant {
	c.mu.Lock()
	defer c.mu.Unlock()
	return guard.InstantAt(c.mono)
}

// Wall implements guard.Clock.
func (c *Clock) Wall() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.wall
}

// NewTimer implements guard.Clock. The timer fires when Advance takes the monotonic
// reading to or past its deadline; a timer for no time at all fires at once.
func (c *Clock) NewTimer(d time.Duration) guard.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.set++
	t := &timer{clock: c, at: c.mono + d, seq: c.set, c: make(chan time.Time, 1)}
	if d <= 0 {
		t.fired = true
		t.c <- c.wall
		return t
	}
	c.timers = append(c.timers, t)
	c.notifyLocked()
	return t
}

// Advance moves both readings forward by d and fires every timer now due, earliest first.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mono += d
	c.wall = c.wall.Add(d)
	kept := c.timers[:0]
	var due []*timer
	for _, t := range c.timers {
		if t.at <= c.mono {
			due = append(due, t)
		} else {
			kept = append(kept, t)
		}
	}
	c.timers = kept
	for len(due) > 0 {
		earliest := 0
		for i, t := range due {
			if t.at < due[earliest].at {
				earliest = i
			}
		}
		t := due[earliest]
		due = append(due[:earliest], due[earliest+1:]...)
		t.fired = true
		t.c <- c.wall
	}
	c.notifyLocked()
}

// StepWall moves the wall reading alone, as time synchronisation does; d may be negative.
func (c *Clock) StepWall(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.wall = c.wall.Add(d)
	c.notifyLocked()
}

// Changed is closed the next time either reading moves or a timer is set.
func (c *Clock) Changed() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.changed
}

// Timers is how many timers are set and have not fired or been stopped.
func (c *Clock) Timers() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.timers)
}

// Set is how many timers this clock has set so far, fired and stopped ones included. A
// test reads it before whatever makes the code under test set its next timer, and hands
// it to WaitForTimer.
func (c *Clock) Set() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.set
}

// WaitForTimer returns once a timer set after the first `since` is pending and due at at,
// so a test advances the clock only once the code under test is waiting on it. A count
// of pending timers is not that: a bound's timer is stopped by a goroutine of its own
// after the bounded call has returned, and while it lingers it passes for the next wait.
// It gives up when ctx ends.
func (c *Clock) WaitForTimer(ctx context.Context, at guard.Instant, since int) error {
	for {
		c.mu.Lock()
		set, changed := false, c.changed
		for _, t := range c.timers {
			if t.seq > since && guard.InstantAt(t.at) == at {
				set = true
			}
		}
		c.mu.Unlock()
		if set {
			return nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (c *Clock) notifyLocked() {
	close(c.changed)
	c.changed = make(chan struct{})
}

type timer struct {
	clock *Clock
	at    time.Duration
	seq   int
	c     chan time.Time
	fired bool
}

func (t *timer) C() <-chan time.Time { return t.c }

func (t *timer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	if t.fired {
		return false
	}
	for i, pending := range t.clock.timers {
		if pending == t {
			t.clock.timers = append(t.clock.timers[:i], t.clock.timers[i+1:]...)
			t.clock.notifyLocked()
			return true
		}
	}
	return false
}
