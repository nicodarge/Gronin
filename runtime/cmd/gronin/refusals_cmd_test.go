package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/bintest"
	"github.com/nicodarge/Gronin/runtime/internal/fakeagent"
	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// SC-108 for the mechanisms User Story 1 writes. The records are seeded through the
// record package and read back through the built executable, which is the operator's own
// surface.
func TestRefusalsAreReadableAfterwards(t *testing.T) {
	stateDir := t.TempDir()
	store, err := record.Open(t.Context(), filepath.Join(stateDir, "record"), nil)
	if err != nil {
		t.Fatal(err)
	}

	tick := time.Date(2026, 9, 10, 6, 0, 0, 0, time.UTC)
	seeded := []record.Refusal{
		{
			PlaybookName: "drift-check", TriggerKind: record.TriggerSchedule, DueAt: tick,
			Mechanism: record.MechanismClaimHeld,
			Detail:    "held by run 20260910T060000Z-3f9a1c0b2e4d on host-b.example.com, process 8f2c1a0b",
			RefusedAt: tick,
		},
		{
			PlaybookName: "drift-check", TriggerKind: record.TriggerSchedule, DueAt: tick.Add(5 * time.Minute),
			Mechanism: record.MechanismTickAlreadyRan,
			Detail:    "tick already ran: tick 2026-09-10T06:05:00Z ran as run 20260910T060500Z-7b21e4c09d3a",
			RefusedAt: tick.Add(5 * time.Minute),
		},
		{
			PlaybookName: "drift-check", TriggerKind: record.TriggerManual,
			Mechanism: record.MechanismBackendUnavailable,
			Detail:    "etcd at unix:///run/gronin/etcd.sock: backend unavailable: deciding: context deadline exceeded",
			RefusedAt: tick.Add(10 * time.Minute),
		},
		{
			PlaybookName: "drift-check", TriggerKind: record.TriggerManual,
			Mechanism: record.MechanismWaitingSlotFull,
			Detail:    "a trigger accepted at 2026-09-10T06:14:02Z is already waiting",
			RefusedAt: tick.Add(15 * time.Minute),
		},
		{
			PlaybookName: "drift-check", TriggerKind: record.TriggerManual,
			Mechanism: record.MechanismRateLimited,
			Detail:    "rate limit reached: 2 runs per 1m",
			RefusedAt: tick.Add(20 * time.Minute),
		},
		{
			PlaybookName: "drift-check", TriggerKind: record.TriggerManual,
			WaitingTriggerID: "0a1b2c3d4e5f6071",
			Mechanism:        record.MechanismWaitExpired,
			Detail:           "waited the 30m drift-check allows, and its claim was still held",
			RefusedAt:        tick.Add(44 * time.Minute),
		},
		{
			PlaybookName: "drift-check", TriggerKind: record.TriggerManual,
			WaitingTriggerID: "1b2c3d4e5f607182",
			Mechanism:        record.MechanismPlaybookChanged,
			Detail:           `/srv/gronin/playbooks/book.yaml now declares "drift-renamed", not "drift-check"`,
			RefusedAt:        tick.Add(50 * time.Minute),
		},
		{
			PlaybookName: "drift-check", TriggerKind: record.TriggerManual,
			WaitingTriggerID: "2c3d4e5f60718293",
			Mechanism:        record.MechanismDropped,
			Detail:           "trigger 2c3d4e5f60718293 accepted at 2026-09-10T06:52:40Z; process 8f2c1a0b ended before it ran",
			RefusedAt:        tick.Add(55 * time.Minute),
		},
	}
	for _, refusal := range seeded {
		if err := store.RecordRefusal(t.Context(), refusal); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	got := bintest.Run(t, "refusals", "--state-dir", stateDir)
	if got.ExitCode != 0 {
		t.Fatalf("gronin refusals exited %d: %q", got.ExitCode, got.Stderr)
	}
	lines := strings.Split(strings.TrimSpace(got.Stdout), "\n")
	if len(lines) != len(seeded) {
		t.Fatalf("%d line(s) for %d refusals:\n%s", len(lines), len(seeded), got.Stdout)
	}

	// Most recent first.
	if !strings.Contains(lines[0], string(seeded[len(seeded)-1].Mechanism)) {
		t.Fatalf("the most recent refusal is not first:\n%s", got.Stdout)
	}
	if !strings.Contains(lines[len(lines)-1], string(record.MechanismClaimHeld)) {
		t.Fatalf("the oldest refusal is not last:\n%s", got.Stdout)
	}

	// Each line names its time in UTC, the playbook, the trigger kind, the mechanism and
	// the detail. None of the five can be dropped without an operator losing the answer
	// to a question they came here with.
	for at, refusal := range seeded {
		line := lines[len(lines)-1-at]
		for _, named := range []string{
			refusal.RefusedAt.Format(time.RFC3339), refusal.PlaybookName,
			string(refusal.TriggerKind), string(refusal.Mechanism), refusal.Detail,
		} {
			if !strings.Contains(line, named) {
				t.Fatalf("the line for %s does not name %q: %q", refusal.Mechanism, named, line)
			}
		}
	}
}

// FR-118, at the surface where a trigger can actually carry a timestamp: a value passed
// with --trigger is a playbook's to interpolate and never the guard's to judge by. The
// refusal is dated by the runtime and names no tick.
func TestATriggerValueIsNeitherTheRefusalsTimeNorItsTick(t *testing.T) {
	t.Setenv(fakeagent.ModeVar, fakeagent.ModeSuccess)
	stateDir, _ := oneHost(t, "http://127.0.0.1:9/unreachable")
	nothing := "unix://" + filepath.Join(t.TempDir(), "nothing.sock")
	writeCoordinationFile(t, stateDir, map[string]any{
		"etcd":           map[string]any{"endpoints": []string{nothing}, "prefix": "gronin/"},
		"decision_bound": "1s",
	})

	frozen := "2019-01-01T00:00:00Z"
	before := time.Now().UTC().Add(-time.Minute)
	got := bintest.Run(t, "run", "drift-check", "--state-dir", stateDir,
		"--agent", fakeagent.Build(t), "--trigger", "startsAt="+frozen)
	if got.ExitCode == 0 {
		t.Fatalf("the run was not refused: %q", got.Stdout)
	}

	store, err := record.Open(t.Context(), filepath.Join(stateDir, "record"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	refusals, err := store.ListRefusals(t.Context(), 10)
	if err != nil || len(refusals) != 1 {
		t.Fatalf("refusals = %+v, err = %v", refusals, err)
	}
	if !refusals[0].DueAt.IsZero() {
		t.Fatalf("a manual trigger's refusal carries a tick: %s", refusals[0].DueAt.Format(time.RFC3339))
	}
	if refusals[0].RefusedAt.Before(before) {
		t.Fatalf("refused_at is %s, which is not the runtime's own reading",
			refusals[0].RefusedAt.Format(time.RFC3339))
	}
	if strings.Contains(refusals[0].Detail, frozen) {
		t.Fatalf("the trigger's timestamp reached the refusal: %q", refusals[0].Detail)
	}
	// And nothing ran: the file a run would have written is not there.
	if _, err := os.Stat(filepath.Join(stateDir, "trace")); !os.IsNotExist(err) {
		t.Fatalf("the gather step ran for a refused trigger: %v", err)
	}
}
