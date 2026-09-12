package guard_test

import (
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
	"github.com/nicodarge/Gronin/runtime/internal/guard/guardtest"
	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// FR-118: a refusal's time is the runtime's own wall reading, and the tick it names is
// the instant the schedule computed. The two are far apart here on purpose — the clock
// reads well past the tick — so an implementation that writes one where the other
// belongs is visible in the record rather than merely plausible.
func TestARefusalIsDatedByTheRuntime(t *testing.T) {
	tick := time.Date(2026, 9, 10, 6, 0, 0, 0, time.UTC)
	accepted := tick.Add(37 * time.Minute)

	var (
		backend = guardtest.NewClock(tick)
		runtime = guardtest.NewClock(accepted)
		fake    = guardtest.NewFake(backend, runtime)
		store   = newStore(t)
	)
	holder := &guard.Guard{
		Coordinator: fake.Host(nil), Store: newStore(t), Config: testConfig(), Clock: runtime,
		Host: "host-a.example.com", Instance: "instance-a",
	}
	refused := &guard.Guard{
		Coordinator: fake.Host(nil), Store: store, Config: testConfig(), Clock: runtime,
		Host: "host-b.example.com", Instance: "instance-b",
	}

	if _, err := holder.Admit(t.Context(), book("drift-check"), guard.Request{
		RunID: "run-a", Kind: record.TriggerManual,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := refused.Admit(t.Context(), book("drift-check"), guard.Request{
		RunID: "run-b", Kind: record.TriggerSchedule, DueAt: tick,
	}); !refusedWith(err, record.MechanismClaimHeld) {
		t.Fatalf("the scheduled trigger was not refused as held: %v", err)
	}

	refusals, err := store.ListRefusals(t.Context(), 10)
	if err != nil || len(refusals) != 1 {
		t.Fatalf("refusals = %v, err = %v", refusals, err)
	}
	if !refusals[0].RefusedAt.Equal(accepted) {
		t.Fatalf("refused_at is %s, not the runtime's wall reading %s",
			refusals[0].RefusedAt.Format(time.RFC3339), accepted.Format(time.RFC3339))
	}
	if !refusals[0].DueAt.Equal(tick) {
		t.Fatalf("due_at is %s, not the tick %s",
			refusals[0].DueAt.Format(time.RFC3339), tick.Format(time.RFC3339))
	}
}

// A trigger with no schedule behind it has no due instant, whatever it carries. The
// values a manual invocation passes with --trigger never reach the guard at all, and the
// record is where that is visible: the refusal is dated by the runtime and names no tick.
func TestAManualRefusalNamesNoTick(t *testing.T) {
	accepted := time.Date(2026, 9, 10, 6, 37, 0, 0, time.UTC)
	var (
		backend = guardtest.NewClock(accepted)
		runtime = guardtest.NewClock(accepted)
		fake    = guardtest.NewFake(backend, runtime)
		store   = newStore(t)
	)
	holder := &guard.Guard{
		Coordinator: fake.Host(nil), Store: newStore(t), Config: testConfig(), Clock: runtime,
		Host: "host-a.example.com", Instance: "instance-a",
	}
	refused := &guard.Guard{
		Coordinator: fake.Host(nil), Store: store, Config: testConfig(), Clock: runtime,
		Host: "host-b.example.com", Instance: "instance-b",
	}

	if _, err := holder.Admit(t.Context(), book("drift-check"), guard.Request{
		RunID: "run-a", Kind: record.TriggerManual,
	}); err != nil {
		t.Fatal(err)
	}
	// A due instant offered by a trigger that is not scheduled is not one: it is
	// dropped rather than recorded or compared.
	if _, err := refused.Admit(t.Context(), book("drift-check"), guard.Request{
		RunID: "run-b", Kind: record.TriggerManual, DueAt: accepted.Add(-time.Hour),
	}); !refusedWith(err, record.MechanismClaimHeld) {
		t.Fatalf("the manual trigger was not refused as held: %v", err)
	}

	refusals, err := store.ListRefusals(t.Context(), 10)
	if err != nil || len(refusals) != 1 {
		t.Fatalf("refusals = %v, err = %v", refusals, err)
	}
	if !refusals[0].DueAt.IsZero() {
		t.Fatalf("a manual refusal carries a tick: %s", refusals[0].DueAt.Format(time.RFC3339))
	}
	if !refusals[0].RefusedAt.Equal(accepted) {
		t.Fatalf("refused_at is %s, not the runtime's wall reading", refusals[0].RefusedAt.Format(time.RFC3339))
	}
}
