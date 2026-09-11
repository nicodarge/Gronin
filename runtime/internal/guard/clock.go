package guard

import "time"

// Clock is the runtime's own clock, with its two readings kept apart (FR-118, the
// constitution's Time constraint): the monotonic reading for durations and deadlines,
// the wall reading for timestamps that are recorded. Time synchronisation can step the
// wall reading backwards, which would lengthen any deadline measured on it; it cannot
// step the monotonic one.
type Clock interface {
	// Monotonic is a reading for durations and deadlines.
	Monotonic() Instant
	// Wall is a reading for a timestamp someone reads later, in UTC.
	Wall() time.Time
	// NewTimer fires once d has passed on the monotonic reading.
	NewTimer(d time.Duration) Timer
}

// Timer is a Clock's one-shot timer.
type Timer interface {
	// C receives once, when the timer fires.
	C() <-chan time.Time
	// Stop prevents the timer from firing, and reports whether it did.
	Stop() bool
}

// Instant is a monotonic reading. It compares only with readings of the same Clock, and
// deliberately cannot be turned into a wall time: the two are not the same axis.
type Instant struct{ sinceOrigin time.Duration }

// InstantAt is the reading sinceOrigin after the clock's own origin, for Clock
// implementations.
func InstantAt(sinceOrigin time.Duration) Instant { return Instant{sinceOrigin: sinceOrigin} }

// Add is the reading d later.
func (i Instant) Add(d time.Duration) Instant { return Instant{sinceOrigin: i.sinceOrigin + d} }

// Sub is how long after j this reading is.
func (i Instant) Sub(j Instant) time.Duration { return i.sinceOrigin - j.sinceOrigin }

// Before reports whether i is earlier than j.
func (i Instant) Before(j Instant) bool { return i.sinceOrigin < j.sinceOrigin }

// After reports whether i is later than j.
func (i Instant) After(j Instant) bool { return i.sinceOrigin > j.sinceOrigin }

// SystemClock is the host's clock.
func SystemClock() Clock { return systemClock{origin: time.Now()} }

type systemClock struct{ origin time.Time }

// time.Since reads the monotonic component origin carries, never the wall reading.
func (c systemClock) Monotonic() Instant { return Instant{sinceOrigin: time.Since(c.origin)} }

func (systemClock) Wall() time.Time { return time.Now().UTC().Round(0) }

func (systemClock) NewTimer(d time.Duration) Timer { return systemTimer{time.NewTimer(d)} }

type systemTimer struct{ timer *time.Timer }

func (t systemTimer) C() <-chan time.Time { return t.timer.C }
func (t systemTimer) Stop() bool          { return t.timer.Stop() }
