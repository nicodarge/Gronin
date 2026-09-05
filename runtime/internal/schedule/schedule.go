// Package schedule parses cron expressions, runs the scheduler, and records an
// occurrence that was missed.
package schedule

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

// parser is the standard five-field form, and only that. The optional-seconds and
// descriptor extensions are deliberately absent: a playbook is shared, and an expression
// that means one thing here and another in the reader's crontab is worse than one this
// refuses.
var parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// Schedule is a parsed cron expression.
type Schedule struct {
	expr  string
	inner cron.Schedule
}

// Parse reads a five-field cron expression.
func Parse(expr string) (Schedule, error) {
	inner, err := parser.Parse(expr)
	if err != nil {
		return Schedule{}, fmt.Errorf("%q is not a five-field cron expression: %w", expr, err)
	}
	return Schedule{expr: expr, inner: inner}, nil
}

// String returns the expression as it was written.
func (s Schedule) String() string { return s.expr }

// Next is the first occurrence strictly after t.
func (s Schedule) Next(t time.Time) time.Time { return s.inner.Next(t) }

// Fire runs one occurrence.
//
// Its error means the occurrence never became a run — the playbook was already in
// flight, or it could not be found. It does NOT mean the run failed: a run that
// happened and went badly is a run record, and recording it as a missed occurrence as
// well would tell an operator that something did not happen when it did. So a fire that
// produced a run of any status returns nil.
type Fire func(ctx context.Context, playbookName string, dueAt time.Time) error

// Missed records an occurrence that did not execute, and why.
type Missed func(ctx context.Context, playbookName string, dueAt time.Time, reason string) error

// catchUpLimit bounds how many past occurrences one tick will enumerate. A runtime that
// was down for a week against a five-minute schedule has two thousand of them, and
// writing two thousand rows to say the same thing helps nobody.
const catchUpLimit = 50

// Scheduler arms schedules and fires them.
//
// Time enters through Tick rather than being read inside, so the whole of this is
// testable against fixed instants — and so the one clock in use is the host's, which is
// what the constitution requires. A payload timestamp never reaches here.
type Scheduler struct {
	fire   Fire
	missed Missed

	mu      sync.Mutex
	entries map[string]*entry
}

type entry struct {
	name     string
	schedule Schedule
	seenTo   time.Time
}

// New returns a scheduler.
func New(fire Fire, missed Missed) *Scheduler {
	return &Scheduler{fire: fire, missed: missed, entries: map[string]*entry{}}
}

// Add arms a playbook. from is the instant the scheduler considers itself caught up to:
// occurrences after it are due, and occurrences before it belong to a run of the
// runtime that is over.
func (s *Scheduler) Add(name string, sched Schedule, from time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[name] = &entry{name: name, schedule: sched, seenTo: from}
}

// Names returns the armed playbooks, ordered.
func (s *Scheduler) Names() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.entries))
	for name := range s.entries {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// NextDue is when the earliest armed occurrence falls after now, or zero if nothing is
// armed. The loop sleeps on it rather than polling.
func (s *Scheduler) NextDue(now time.Time) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()

	var earliest time.Time
	for _, e := range s.entries {
		next := e.schedule.Next(now)
		if earliest.IsZero() || next.Before(earliest) {
			earliest = next
		}
	}
	return earliest
}

// Tick fires every occurrence that has come due since the last tick.
//
// Only the most recent one runs. The rest are recorded as missed with their reason,
// because a runtime that was down for an hour should not wake up and spend an hour's
// backlog of money at once — and because the record has to say that they did not run,
// which is the half silence would hide.
func (s *Scheduler) Tick(ctx context.Context, now time.Time) error {
	s.mu.Lock()
	entries := make([]*entry, 0, len(s.entries))
	for _, e := range s.entries {
		entries = append(entries, e)
	}
	s.mu.Unlock()

	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })

	var problems []error
	for _, e := range entries {
		problems = append(problems, s.tickOne(ctx, e, now)...)
	}
	return errors.Join(problems...)
}

func (s *Scheduler) tickOne(ctx context.Context, e *entry, now time.Time) []error {
	var (
		due      []time.Time
		problems []error
		overflow int
	)

	s.mu.Lock()
	from := e.seenTo
	s.mu.Unlock()

	for at := e.schedule.Next(from); !at.After(now); at = e.schedule.Next(at) {
		if len(due) == catchUpLimit {
			overflow++
			// Keep walking to reach the last one, but stop remembering the middle.
			due[len(due)-1] = at
			continue
		}
		due = append(due, at)
	}
	if len(due) == 0 {
		return nil
	}

	s.mu.Lock()
	e.seenTo = now
	s.mu.Unlock()

	last := due[len(due)-1]
	for _, at := range due[:len(due)-1] {
		reason := "the runtime was not running when this occurrence was due"
		if err := s.record(ctx, e.name, at, reason); err != nil {
			problems = append(problems, err)
		}
	}
	if overflow > 0 {
		problems = append(problems, s.record(ctx, e.name, last.Add(-time.Nanosecond),
			fmt.Sprintf("%d further occurrences were missed and not recorded individually", overflow)))
	}

	if err := s.fire(ctx, e.name, last); err != nil {
		// The occurrence did not become a run. That is what a missed occurrence is.
		if recordErr := s.record(ctx, e.name, last, err.Error()); recordErr != nil {
			problems = append(problems, recordErr)
		}
	}
	return problems
}

func (s *Scheduler) record(ctx context.Context, name string, at time.Time, reason string) error {
	if s.missed == nil {
		return nil
	}
	return s.missed(ctx, name, at, reason)
}

// Run ticks until the context ends, waking for the next armed occurrence.
func (s *Scheduler) Run(ctx context.Context, now func() time.Time, log *slog.Logger) error {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	for {
		at := now()
		if err := s.Tick(ctx, at); err != nil {
			// A tick's problems do not stop the scheduler — one playbook failing is not
			// a reason for the others to stop being scheduled — but they are said out
			// loud. A failure to record a missed occurrence that disappears silently is
			// FR-030 quietly not happening.
			log.Error("the scheduler could not record everything this tick produced",
				"err", err)
		}

		at = now()
		next := s.NextDue(at)
		var wait time.Duration
		if next.IsZero() {
			wait = time.Minute
		} else {
			// Against the injected clock, not time.Until: a method that takes a clock
			// and then reads another one is a seam that only looks like one.
			wait = next.Sub(at)
		}
		if wait < time.Second {
			wait = time.Second
		}

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
