package main

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
	"github.com/nicodarge/Gronin/runtime/internal/guard/guardtest"
	"github.com/nicodarge/Gronin/runtime/internal/playbook"
	"github.com/nicodarge/Gronin/runtime/internal/record"
	"github.com/nicodarge/Gronin/runtime/internal/run"
)

// The distinction this branch got wrong. The scheduler records a fire's error as a missed
// occurrence, so anything that returns an error here is claiming the occurrence never
// happened.
func TestOnlyAnOccurrenceThatNeverRanIsReportedMissed(t *testing.T) {
	for name, probe := range map[string]struct {
		finished record.Run
		err      error
		missed   bool
	}{
		"a run that succeeded":   {record.Run{ID: "r1", Status: record.StatusSucceeded}, nil, false},
		"a run that failed":      {record.Run{ID: "r1", Status: record.StatusFailed}, nil, false},
		"a run that was refused": {record.Run{ID: "r1", Status: record.StatusRefused}, nil, false},
		"a run that timed out":   {record.Run{ID: "r1", Status: record.StatusTimedOut}, nil, false},
		"a run that was capped":  {record.Run{ID: "r1", Status: record.StatusCapped}, nil, false},
		"a run that lost its claim": {
			record.Run{ID: "r1", Status: record.StatusClaimLost}, nil, false,
		},
		// The guard refused it, which is recorded as a refusal naming the mechanism.
		// Reporting it as missed as well would say one event happened twice.
		"the guard refused the trigger": {
			record.Run{}, &guard.Refused{Mechanism: record.MechanismClaimHeld, Detail: "held"}, false,
		},
		"the run could not begin":    {record.Run{}, errors.New("creating the working directory"), true},
		"neither a run nor an error": {record.Run{}, nil, true},
	} {
		t.Run(name, func(t *testing.T) {
			got := scheduledOutcome(probe.finished, probe.err)
			if probe.missed && got == nil {
				t.Fatalf("%s was not reported as a missed occurrence", name)
			}
			if !probe.missed && got != nil {
				t.Fatalf("%s was reported as a missed occurrence: %v", name, got)
			}
		})
	}
}

// T040: a refused trigger leaves one record and not two, and the instant it is judged by
// is the one the scheduler fired for rather than this host's clock (FR-129).
func TestAGuardRefusalIsNotAMissedOccurrence(t *testing.T) {
	dir := t.TempDir()
	store, err := record.Open(t.Context(), filepath.Join(dir, "record"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	book := loadOnePlaybook(t, dir)
	now := time.Date(2026, 9, 10, 6, 0, 0, 0, time.UTC)
	fake := guardtest.NewFake(guardtest.NewClock(now), guardtest.NewClock(now))
	elsewhere := &guard.Guard{
		Coordinator: fake.Host(nil), Store: store, Config: guard.DefaultConfig(),
		Host: "host-b.example.com", Instance: "instance-b",
	}
	if _, err := elsewhere.Admit(t.Context(), book.Playbooks[0], guard.Request{
		RunID: "run-elsewhere", Kind: record.TriggerManual,
	}); err != nil {
		t.Fatal(err)
	}

	manager := run.NewManager(store, filepath.Join(dir, "work"), filepath.Join(dir, "locks"))
	deployed := &deployment{
		store:   store,
		manager: manager,
		log:     slog.New(slog.DiscardHandler),
		executor: &run.Executor{
			Manager: manager, Store: store,
			Guard: &guard.Guard{
				Coordinator: fake.Host(nil), Store: store, Config: guard.DefaultConfig(),
				Host: "host-a.example.com", Instance: "instance-a",
			},
		},
	}

	// The tick is hours behind this host's clock, so a refusal dated or judged by the
	// clock is visible in the record rather than merely possible.
	tick := time.Date(2026, 9, 10, 3, 0, 0, 0, time.UTC)
	// A nil return is what keeps the scheduler from recording a missed occurrence: an
	// error here is how it is told an occurrence never happened.
	if err := fire(t.Context(), deployed, book, "drift-check", tick); err != nil {
		t.Fatalf("a refused trigger was reported as a missed occurrence: %v", err)
	}

	refusals, err := store.ListRefusals(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(refusals) != 1 {
		t.Fatalf("%d refusal(s) recorded, want one", len(refusals))
	}
	if refusals[0].Mechanism != record.MechanismClaimHeld {
		t.Fatalf("mechanism = %q", refusals[0].Mechanism)
	}
	if !refusals[0].DueAt.Equal(tick) {
		t.Fatalf("the refusal names %s, not the tick the scheduler fired for, %s",
			refusals[0].DueAt.Format(time.RFC3339), tick.Format(time.RFC3339))
	}
}

// loadOnePlaybook lays a cron playbook on disk and loads it through the gate, so the
// test drives the same value `serve` would hold.
func loadOnePlaybook(t *testing.T, dir string) playbook.Loaded {
	t.Helper()
	books := filepath.Join(dir, "playbooks")
	if err := os.MkdirAll(books, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(books, "book.yaml"), []byte(`
name: drift-check
trigger:
  type: cron
  schedule: "0 6 * * *"
gather:
  - run: echo '{"drift":0}'
    as: facts.json
agent:
  model: claude-sonnet-5
  prompt_file: prompt.md
  tools: [Read]
  output_schema:
    type: object
sinks:
  - discord:
      webhook: http://127.0.0.1:9/unreachable
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(books, "prompt.md"), []byte("report"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := playbook.Load(books, capabilities(nil, nil, nil))
	if err != nil || !loaded.OK() {
		t.Fatalf("loading: %v, refusals = %+v", err, loaded.Refusals)
	}
	return loaded
}
