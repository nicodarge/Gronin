package guard_test

import (
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
	"github.com/nicodarge/Gronin/runtime/internal/guard/guardtest"
	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// SC-118, FR-129, R6: a tick is judged by the instant its schedule computed, whatever
// the clock of the process accepting it reads.
//
// Two halves, and each passes against the mutant the other catches. The first expects a
// run, so it fails an implementation that records or compares a clock reading and
// thereby refuses too much. The second expects a refusal, so it fails one that lets too
// much through.
func TestATickIsJudgedByItsSchedule(t *testing.T) {
	tick := time.Date(2026, 9, 10, 6, 0, 0, 0, time.UTC)
	next := tick.Add(time.Minute)

	for name, probe := range map[string]struct {
		// second is where the second host's clock reads.
		second time.Time
		// due is the tick it fires for, and ran whether it must produce a run.
		due  time.Time
		runs bool
	}{
		"the next tick, on a host whose clock is well behind": {
			second: tick.Add(-time.Hour), due: next, runs: true,
		},
		"a tick that already ran, on a host whose clock is well ahead": {
			second: tick.Add(time.Hour), due: tick, runs: false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			backend := guardtest.NewClock(tick)
			// The first host's clock reads past the next tick already, so a recorded
			// clock reading would be later than the tick the second host fires for.
			first := guardtest.NewClock(next.Add(4 * time.Minute))
			second := guardtest.NewClock(probe.second)
			fake := guardtest.NewFake(backend, first)

			a := &guard.Guard{
				Coordinator: fake.Host(nil), Store: newStore(t), Config: testConfig(), Clock: first,
				Host: "host-a.example.com", Instance: "instance-a",
			}
			b := &guard.Guard{
				Coordinator: fake.Host(nil), Store: newStore(t), Config: testConfig(), Clock: second,
				Host: "host-b.example.com", Instance: "instance-b",
			}

			taken, err := a.Admit(t.Context(), book("drift-check"), guard.Request{
				RunID: "run-a", Kind: record.TriggerSchedule, DueAt: tick,
			})
			if err != nil {
				t.Fatalf("the first tick was refused: %v", err)
			}
			if err := taken.Claim.Release(t.Context()); err != nil {
				t.Fatal(err)
			}

			admitted, err := b.Admit(t.Context(), book("drift-check"), guard.Request{
				RunID: "run-b", Kind: record.TriggerSchedule, DueAt: probe.due,
			})
			switch {
			case probe.runs && err != nil:
				t.Fatalf("the tick at %s was refused: %v", probe.due.Format(time.RFC3339), err)
			case probe.runs:
				if err := admitted.Claim.Release(t.Context()); err != nil {
					t.Fatal(err)
				}
			case !probe.runs && !refusedWith(err, record.MechanismTickAlreadyRan):
				t.Fatalf("the tick at %s was not refused as one that already ran: %v",
					probe.due.Format(time.RFC3339), err)
			}
		})
	}
}
