package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/bintest"
	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// A run stopped because its claim could no longer be proven held appears in `gronin runs`
// like any other status (contracts/cli.md). The listing prints the stored status string
// and has no branch of its own, so this is what says a status added to the record reaches
// the operator at all.
func TestRunsListsAClaimLostRun(t *testing.T) {
	stateDir := t.TempDir()
	store, err := record.Open(t.Context(), filepath.Join(stateDir, "record"), nil)
	if err != nil {
		t.Fatal(err)
	}

	started := time.Date(2026, 9, 10, 6, 0, 0, 0, time.UTC)
	const id = "20260910T060000Z-3f9a1c0b2e4d"
	if err := store.CreateRun(t.Context(), record.Run{
		ID: id, PlaybookName: "drift-check", TriggerKind: record.TriggerSchedule,
		Status: record.StatusRunning, StartedAt: started, ClaimReach: record.ReachCrossHost,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(t.Context(), record.Run{
		ID: id, Status: record.StatusClaimLost, EndedAt: started.Add(18 * time.Second),
		Error: "the claim is gone",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	listed := bintest.Run(t, "runs", "--state-dir", stateDir)
	if listed.ExitCode != 0 {
		t.Fatalf("gronin runs exited %d: %q", listed.ExitCode, listed.Stderr)
	}
	if !strings.Contains(listed.Stdout, string(record.StatusClaimLost)) {
		t.Fatalf("gronin runs does not list a claim_lost run:\n%s", listed.Stdout)
	}
	if !strings.Contains(listed.Stdout, id) {
		t.Fatalf("gronin runs does not name the run:\n%s", listed.Stdout)
	}

	// And `gronin show` says which guarantee it ran under, which is the other half of
	// what a reader of a stopped run needs.
	shown := bintest.Run(t, "show", id, "--state-dir", stateDir)
	if !strings.Contains(shown.Stdout, string(record.ReachCrossHost)) {
		t.Fatalf("gronin show does not say the guarantee:\n%s", shown.Stdout)
	}
}
