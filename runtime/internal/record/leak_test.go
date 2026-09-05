package record_test

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/record"
	"github.com/nicodarge/Gronin/runtime/internal/testsecret"
)

// SC-005, the inside half: with the value configured as a secret, it appears nowhere in
// the record — not in a row, not in a blob, not in the database file's own bytes.
//
// The scan reads the files rather than querying the tables. A query would only find what
// the schema knows about, and a leak into a column nobody thought to select is exactly
// the leak that survives.
func TestNoConfiguredSecretReachesTheRecord(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	store, err := record.Open(ctx, dir, record.NewRedactor([]string{testsecret.Value}))
	if err != nil {
		t.Fatal(err)
	}

	// Every text field the record has, carrying the secret.
	const runID = "run-leak"
	if err := store.CreateRun(ctx, record.Run{
		ID: runID, PlaybookName: "playbook-" + testsecret.Value,
		TriggerKind: record.TriggerManual, Status: record.StatusRunning, StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	promptRef, err := store.Blobs().Put(runID, "prompt.txt",
		[]byte("the prompt as sent, holding "+testsecret.Value))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Blobs().Put(runID, "transcript.jsonl",
		[]byte(`{"result":"`+testsecret.Value+`"}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.AddGatheredInput(ctx, runID, record.GatheredInput{
		Name: "facts-" + testsecret.Value, Bytes: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddToolCall(ctx, runID, record.ToolCall{
		Sequence: 1, Name: "Read", StartedAt: time.Now(), Outcome: "ok " + testsecret.Value,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddRefusedAction(ctx, runID, record.RefusedAction{
		Sequence: 1, Tool: testsecret.Value, Reason: "reaching for " + testsecret.Value,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddSinkOutcome(ctx, runID, record.SinkOutcome{
		Sink: "discord", Status: "failed", Detail: "posting to " + testsecret.Value,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordMissedOccurrence(ctx, "playbook-"+testsecret.Value, time.Now(),
		"skipped because "+testsecret.Value); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, record.Run{
		ID: runID, Status: record.StatusFailed, EndedAt: time.Now(), PromptRef: promptRef,
		Error: "authenticating with " + testsecret.Value,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	secret := []byte(testsecret.Value)
	var found []string
	err = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path) //nolint:gosec // walking a directory this test made
		if err != nil {
			return err
		}
		if bytes.Contains(data, secret) {
			rel, _ := filepath.Rel(dir, path)
			found = append(found, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) > 0 {
		t.Fatalf("the configured secret is in the record, in: %v", found)
	}
}
