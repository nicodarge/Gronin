package schedule_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/schedule"
)

type fired struct {
	mu     sync.Mutex
	runs   []time.Time
	missed []string
}

func (f *fired) fire(_ context.Context, _ string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, at)
	return nil
}

func (f *fired) miss(_ context.Context, name string, at time.Time, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.missed = append(f.missed, name+" "+at.Format(time.RFC3339)+" "+reason)
	return nil
}

func mustParse(t *testing.T, expr string) schedule.Schedule {
	t.Helper()
	parsed, err := schedule.Parse(expr)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

// T016, FR-009: once at its time, and not again for the same occurrence.
func TestAnOccurrenceFiresOnceAtItsTime(t *testing.T) {
	got := &fired{}
	scheduler := schedule.New(got.fire, got.miss)

	base := time.Date(2026, 9, 5, 6, 0, 0, 0, time.UTC)
	scheduler.Add("drift-check", mustParse(t, "0 7 * * *"), base)

	// Before it is due.
	if err := scheduler.Tick(t.Context(), base.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(got.runs) != 0 {
		t.Fatalf("it ran before it was due: %v", got.runs)
	}

	// After.
	if err := scheduler.Tick(t.Context(), base.Add(90*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(got.runs) != 1 {
		t.Fatalf("%d runs, want 1", len(got.runs))
	}
	if !got.runs[0].Equal(time.Date(2026, 9, 5, 7, 0, 0, 0, time.UTC)) {
		t.Fatalf("fired for %v", got.runs[0])
	}

	// Ticking again does not re-fire the same occurrence.
	if err := scheduler.Tick(t.Context(), base.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(got.runs) != 1 {
		t.Fatalf("the same occurrence fired %d times", len(got.runs))
	}
}

// FR-030. A backlog is not run: it is recorded. Waking up and spending an hour's worth
// of money at once is the failure, and silence about it is the other one.
func TestABacklogIsRecordedRatherThanRun(t *testing.T) {
	got := &fired{}
	scheduler := schedule.New(got.fire, got.miss)

	base := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	scheduler.Add("every-ten", mustParse(t, "*/10 * * * *"), base)

	// An hour of downtime: six occurrences.
	if err := scheduler.Tick(t.Context(), base.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	if len(got.runs) != 1 {
		t.Fatalf("%d runs, want the most recent one only", len(got.runs))
	}
	if !got.runs[0].Equal(base.Add(time.Hour)) {
		t.Fatalf("the run was for %v, want the most recent occurrence", got.runs[0])
	}
	if len(got.missed) != 5 {
		t.Fatalf("%d missed occurrences recorded, want 5: %v", len(got.missed), got.missed)
	}
	for _, missed := range got.missed {
		if !strings.Contains(missed, "not running") {
			t.Fatalf("a missed occurrence does not say why: %q", missed)
		}
	}
}

// A run that could not start is a missed occurrence with the reason, not silence. This
// is what a schedule firing faster than its playbook finishes looks like in the record.
func TestAnOccurrenceThatCouldNotRunIsRecordedWithItsReason(t *testing.T) {
	got := &fired{}
	refusal := errors.New("a run of this playbook is already in flight")
	scheduler := schedule.New(
		func(context.Context, string, time.Time) error { return refusal },
		got.miss,
	)

	base := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	scheduler.Add("drift-check", mustParse(t, "*/5 * * * *"), base)

	if err := scheduler.Tick(t.Context(), base.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}

	if len(got.missed) != 1 {
		t.Fatalf("%d missed, want 1: %v", len(got.missed), got.missed)
	}
	if !strings.Contains(got.missed[0], "already in flight") {
		t.Fatalf("the reason was lost: %q", got.missed[0])
	}
}

// A week of downtime against a five-minute schedule is two thousand occurrences, and two
// thousand rows saying the same thing help nobody.
func TestALongBacklogIsBoundedAndSaysSo(t *testing.T) {
	got := &fired{}
	scheduler := schedule.New(got.fire, got.miss)

	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	scheduler.Add("every-five", mustParse(t, "*/5 * * * *"), base)

	if err := scheduler.Tick(t.Context(), base.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}

	if len(got.runs) != 1 {
		t.Fatalf("%d runs", len(got.runs))
	}
	if len(got.missed) > 60 {
		t.Fatalf("%d missed rows for two days of downtime", len(got.missed))
	}
	var summarised bool
	for _, missed := range got.missed {
		if strings.Contains(missed, "not recorded individually") {
			summarised = true
		}
	}
	if !summarised {
		t.Fatal("the occurrences that were dropped are not accounted for anywhere")
	}
}

func TestParseTakesTheStandardFiveFieldForm(t *testing.T) {
	for _, expr := range []string{"0 6 * * *", "*/5 * * * *", "0 0 1 * *", "15 3 * * 1-5"} {
		if _, err := schedule.Parse(expr); err != nil {
			t.Errorf("%q was refused: %v", expr, err)
		}
	}
	// Six fields is the seconds extension, which means something different in a reader's
	// own crontab. A playbook is shared, so this refuses rather than guesses.
	for _, expr := range []string{"", "not a schedule", "0 6 * *", "*/5 * * * * *", "@daily"} {
		if _, err := schedule.Parse(expr); err == nil {
			t.Errorf("%q was accepted", expr)
		}
	}
}

func TestNextDueIsTheEarliestArmedOccurrence(t *testing.T) {
	scheduler := schedule.New(
		func(context.Context, string, time.Time) error { return nil }, nil)

	base := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	scheduler.Add("hourly", mustParse(t, "0 * * * *"), base)
	scheduler.Add("daily", mustParse(t, "0 7 * * *"), base)

	if got := scheduler.NextDue(base); !got.Equal(base.Add(time.Hour)) {
		t.Fatalf("next due = %v, want %v", got, base.Add(time.Hour))
	}
	if names := scheduler.Names(); len(names) != 2 || names[0] != "daily" {
		t.Fatalf("names = %v", names)
	}
}

func TestNothingArmedMeansNothingDue(t *testing.T) {
	scheduler := schedule.New(func(context.Context, string, time.Time) error { return nil }, nil)

	if got := scheduler.NextDue(time.Now()); !got.IsZero() {
		t.Fatalf("next due = %v with nothing armed", got)
	}
	if err := scheduler.Tick(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
}
