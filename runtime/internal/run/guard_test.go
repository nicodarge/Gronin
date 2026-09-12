package run_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/fakeagent"
	"github.com/nicodarge/Gronin/runtime/internal/guard"
	"github.com/nicodarge/Gronin/runtime/internal/guard/guardtest"
	"github.com/nicodarge/Gronin/runtime/internal/playbook"
	"github.com/nicodarge/Gronin/runtime/internal/record"
	"github.com/nicodarge/Gronin/runtime/internal/run"
)

// No guard block at all, and its gather step leaves a mark outside the run's working
// directory — which is the only way to tell a refused trigger from one that ran and was
// tidied up after.
const tracingPlaybook = `
name: drift-check
trigger:
  type: cron
  schedule: "0 6 * * *"
gather:
  - run: printf 'ran\n' >> %s; echo '{"drift":0}'
    as: facts.json
agent:
  model: claude-sonnet-5
  prompt_file: prompt.md
  tools: [Read]
  output_schema:
    type: object
    required: [findings]
    properties:
      findings:
        type: array
sinks:
  - discord:
      webhook: ${config.ops_webhook}
`

// guarded is a harness whose executor decides through one fake backend, with a second
// coordinator on it standing for the other host.
type guarded struct {
	*harness
	book  *playbook.Playbook
	trace string
	fake  *guardtest.Fake
	other *guard.Guard
}

func newGuarded(t *testing.T) *guarded {
	t.Helper()
	h := newHarness(t, fakeagent.ModeSuccess)
	h.executor.AgentEnv = append(h.executor.AgentEnv,
		fakeagent.ResultVar+`={"findings":[{"id":"one"}]}`)

	now := time.Date(2026, 9, 10, 6, 0, 0, 0, time.UTC)
	fake := guardtest.NewFake(guardtest.NewClock(now), guardtest.NewClock(now))
	h.executor.Guard = &guard.Guard{
		Coordinator: fake.Host(nil), Store: h.store, Config: guard.DefaultConfig(),
		Host: "host-a.example.com", Instance: "instance-a",
	}
	trace := filepath.Join(h.dir, "trace")
	return &guarded{
		harness: h,
		book:    h.playbook(t, fmt.Sprintf(tracingPlaybook, trace)),
		trace:   trace,
		fake:    fake,
		other: &guard.Guard{
			Coordinator: fake.Host(nil), Store: h.store, Config: guard.DefaultConfig(),
			Host: "host-b.example.com", Instance: "instance-b",
		},
	}
}

// ran is how many times the gather step has run. A refused trigger must leave it where
// it was (FR-102).
func (g *guarded) ran(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile(g.trace)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for at := range len(data) {
		if data[at] == '\n' {
			count++
		}
	}
	return count
}

// FR-101, FR-102 and FR-120: a playbook declaring no guard block is still held to
// non-concurrency, the refusal happens before gather, and the run that does happen once
// the claim frees is what says the refusal was the claim's doing and not the playbook's.
func TestGuardRefusesAScheduledTriggerWhileThePlaybookRuns(t *testing.T) {
	g := newGuarded(t)
	tick := time.Date(2026, 9, 10, 6, 0, 0, 0, time.UTC)

	held, err := g.other.Admit(t.Context(), g.book, guard.Request{
		RunID: "run-elsewhere", Kind: record.TriggerManual,
	})
	if err != nil {
		t.Fatalf("the other host could not take the claim: %v", err)
	}

	_, err = g.executor.Execute(t.Context(), g.book, run.Trigger{
		Kind: record.TriggerSchedule, DueAt: tick,
	})
	var refused *guard.Refused
	if !errors.As(err, &refused) || refused.Mechanism != record.MechanismClaimHeld {
		t.Fatalf("the trigger was not refused as held: %v", err)
	}
	if !strings.Contains(refused.Detail, "run-elsewhere") {
		t.Fatalf("the refusal does not name the run that holds the claim: %q", refused.Detail)
	}
	if ran := g.ran(t); ran != 0 {
		t.Fatalf("the gather step ran %d time(s) for a refused trigger", ran)
	}
	// The stub agent is never started either: no run exists to have started it.
	runs, err := g.store.ListRuns(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("a refused trigger produced %d run(s): %+v", len(runs), runs)
	}

	// The claim frees and the next tick runs, which is what says the refusal above was
	// the claim's doing.
	if err := held.Claim.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := g.executor.Execute(t.Context(), g.book, run.Trigger{
		Kind: record.TriggerSchedule, DueAt: tick.Add(time.Hour),
	}); err != nil {
		t.Fatalf("the next tick was refused once the claim was free: %v", err)
	}
	if ran := g.ran(t); ran != 1 {
		t.Fatalf("the gather step ran %d time(s), want 1", ran)
	}
}

// A replay and a resume take the claim like any other run, and are refused rather than
// made to run beside the one that holds it.
func TestGuardRefusesAReplayAndAResumeWhileThePlaybookRuns(t *testing.T) {
	g := newGuarded(t)

	first, err := g.executor.Execute(t.Context(), g.book, run.Trigger{Kind: record.TriggerManual})
	if err != nil {
		t.Fatalf("the first run: %v", err)
	}
	if first.Status != record.StatusSucceeded {
		t.Fatalf("the first run %s: %s", first.Status, first.Error)
	}

	if _, err := g.other.Admit(t.Context(), g.book, guard.Request{
		RunID: "run-elsewhere", Kind: record.TriggerManual,
	}); err != nil {
		t.Fatal(err)
	}

	for name, derive := range map[string]func() (record.Run, error){
		"a replay": func() (record.Run, error) {
			return g.executor.Replay(t.Context(), first.ID, g.book)
		},
		"a resume": func() (record.Run, error) {
			return g.executor.Resume(t.Context(), first.ID, g.book)
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := derive()
			var refused *guard.Refused
			if !errors.As(err, &refused) || refused.Mechanism != record.MechanismClaimHeld {
				t.Fatalf("%s was not refused as held: %v", name, err)
			}
		})
	}
	// The run that did happen is still the only one, and the one message it delivered is
	// still the only one: nothing ran beside the claim.
	runs, err := g.store.ListRuns(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("%d runs recorded, want the one that held the claim", len(runs))
	}
	if posted := g.posted.all(); len(posted) != 1 {
		t.Fatalf("the sink's endpoint received %d message(s), want 1", len(posted))
	}
}
