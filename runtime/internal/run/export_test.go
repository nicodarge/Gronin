package run

import "github.com/nicodarge/Gronin/runtime/internal/guard"

// OnPoll sets what the file lock calls each time a waiter polls a playbook's lock, so that
// a test can act at the instant the poll happens.
func (l *FileLock) OnPoll(hook func(name string)) { l.poll = hook }

// SetClock replaces the file lock's clock, so a test can move the wall reading its rate
// window is judged on without waiting on the real one.
func (l *FileLock) SetClock(clock guard.Clock) { l.clock = clock }
