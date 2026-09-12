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

// A deployment adding retrieval already has a record, written under the runtime core's
// schema alone. The migration is a new file rather than an edit of the first one, which
// schema.go skips wherever it already ran — so what is asserted is that such a store
// opens, reads its old runs back, and can hold a retrieval afterwards.
func TestRetrievalsMigrateOntoARecordWrittenBeforeThem(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()

	initial, err := os.ReadFile("migrations/0001_initial.sql")
	if err != nil {
		t.Fatal(err)
	}
	db := rawDB(t, dir)
	for _, statement := range []string{
		string(initial),
		`CREATE TABLE schema_migrations (name TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`,
		`INSERT INTO schema_migrations VALUES ('0001_initial.sql', '2026-09-01T00:00:00Z')`,
		`INSERT INTO runs (id, playbook_name, trigger_kind, status, started_at, ended_at)
		 VALUES ('20260901T000000Z-000000000001', 'drift-check', 'schedule', 'succeeded',
		         '2026-09-01T00:00:00Z', '2026-09-01T00:01:00Z')`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := record.Open(ctx, dir, nil)
	if err != nil {
		t.Fatalf("a store written before retrieval did not open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const runID = "20260901T000000Z-000000000001"
	old, err := store.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if old.Status != record.StatusSucceeded || old.PlaybookName != "drift-check" {
		t.Fatalf("the existing run read back as %+v", old)
	}
	retrievals, err := store.Retrievals(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(retrievals) != 0 {
		t.Fatalf("a run recorded before retrieval reads back with %d retrievals", len(retrievals))
	}

	// The new tables are there, which is what a retrieval writes next.
	if err := store.AddRetrieval(ctx, runID, record.Retrieval{
		Sequence: 1, AsName: "runbooks.md", Collection: "runbooks",
		Mode: record.RetrievalLexical, Query: "disk full", Outcome: record.RetrievalEmpty,
	}); err != nil {
		t.Fatalf("the migrated store cannot hold a retrieval: %v", err)
	}
}

func TestRetrievalsRoundTripWithTheirItemsInRankOrder(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	store, err := record.Open(ctx, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const runID = "20260910T061214Z-a41c09e7b6f2"
	if err := store.CreateRun(ctx, record.Run{
		ID: runID, PlaybookName: "disk-check", TriggerKind: record.TriggerManual,
		Status: record.StatusRunning, StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	builtAt := time.Date(2026, 9, 10, 6, 0, 0, 0, time.UTC)
	found := record.Retrieval{
		Sequence: 1, AsName: "runbooks.md", Collection: "runbooks",
		Mode: record.RetrievalLexical, Generation: "3f9a1c0b2e4d", GenerationBuiltAt: builtAt,
		Identity: "9c1d", Query: "disk full on /var", QueryTruncated: true,
		Outcome: record.RetrievalFound, ResultsRef: "blob-results", ResultsBytes: 412,
		CountTruncated: true, BytesTruncated: true,
		// Written out of rank order, so reading them back in it is the record's doing.
		Items: []record.RetrievedItem{
			{Rank: 2, Score: -1.932, Source: "swap.md", Ordinal: 1, Offset: 64, ContentRef: "blob-two"},
			{Rank: 1, Score: -2.271, Source: "disk-full.md", Ordinal: 2, Offset: 1024, ContentRef: "blob-one"},
		},
	}
	empty := record.Retrieval{
		Sequence: 2, AsName: "nothing.md", Collection: "runbooks",
		Mode: record.RetrievalSemantic, Generation: "7b21e4c09d3a", GenerationBuiltAt: builtAt,
		Outcome: record.RetrievalEmpty, ResultsRef: "blob-empty", ResultsBytes: 96,
	}
	refused := record.Retrieval{
		Sequence: 3, AsName: "gone.md", Collection: "by-meaning",
		Mode: record.RetrievalSemantic, Query: "cert expiry",
		Outcome: record.RetrievalRefused, Error: "the collection's directory is not there",
	}
	for _, retrieval := range []record.Retrieval{refused, found, empty} {
		if err := store.AddRetrieval(ctx, runID, retrieval); err != nil {
			t.Fatal(err)
		}
	}

	got, err := store.Retrievals(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("read back %d retrievals, want 3", len(got))
	}
	for at, want := range []record.Retrieval{found, empty, refused} {
		if got[at].Sequence != want.Sequence {
			t.Fatalf("retrieval %d is sequence %d, want %d — they read back out of order",
				at, got[at].Sequence, want.Sequence)
		}
		if got[at].AsName != want.AsName || got[at].Collection != want.Collection ||
			got[at].Mode != want.Mode || got[at].Outcome != want.Outcome ||
			got[at].Query != want.Query || got[at].Error != want.Error {
			t.Errorf("retrieval %d read back as %+v, want %+v", want.Sequence, got[at], want)
		}
		if got[at].Generation != want.Generation || got[at].Identity != want.Identity ||
			!got[at].GenerationBuiltAt.Equal(want.GenerationBuiltAt) {
			t.Errorf("retrieval %d lost its generation: %q %q %v",
				want.Sequence, got[at].Generation, got[at].Identity, got[at].GenerationBuiltAt)
		}
		if got[at].QueryTruncated != want.QueryTruncated ||
			got[at].CountTruncated != want.CountTruncated ||
			got[at].BytesTruncated != want.BytesTruncated {
			t.Errorf("retrieval %d lost what was cut: %+v", want.Sequence, got[at])
		}
		if got[at].ResultsRef != want.ResultsRef || got[at].ResultsBytes != want.ResultsBytes {
			t.Errorf("retrieval %d lost its results file: %q %d",
				want.Sequence, got[at].ResultsRef, got[at].ResultsBytes)
		}
	}

	items := got[0].Items
	if len(items) != 2 || items[0].Rank != 1 || items[1].Rank != 2 {
		t.Fatalf("the items read back as %+v, want rank 1 then rank 2", items)
	}
	if items[0].Source != "disk-full.md" || items[0].Ordinal != 2 || items[0].Offset != 1024 ||
		items[0].Score != -2.271 || items[0].ContentRef != "blob-one" {
		t.Errorf("the first result read back as %+v", items[0])
	}
	if len(got[1].Items) != 0 || len(got[2].Items) != 0 {
		t.Errorf("a retrieval that found nothing carries items: %+v %+v", got[1], got[2])
	}
}

// SC-213 for the retrieval's own fields, the check leak_test.go makes for the runtime
// core's. The scan reads the files rather than querying the tables: a leak into a column
// nobody thought to select is exactly the leak that survives.
func TestRetrievalsHoldNoConfiguredSecret(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	store, err := record.Open(ctx, dir, record.NewRedactor([]string{testsecret.Value}))
	if err != nil {
		t.Fatal(err)
	}

	const runID = "run-retrieval-leak"
	if err := store.CreateRun(ctx, record.Run{
		ID: runID, PlaybookName: "disk-check", TriggerKind: record.TriggerManual,
		Status: record.StatusRunning, StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	content, err := store.Blobs().Put(runID, "runbooks.md",
		[]byte("the passage the agent read, holding "+testsecret.Value))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRetrieval(ctx, runID, record.Retrieval{
		Sequence: 1, AsName: "runbooks.md", Collection: "runbooks",
		Mode: record.RetrievalLexical, Query: "reaching for " + testsecret.Value,
		Outcome: record.RetrievalRefused, Error: "authenticating with " + testsecret.Value,
		Items: []record.RetrievedItem{
			{Rank: 1, Source: "notes-" + testsecret.Value + ".md", Ordinal: 1, ContentRef: content},
		},
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

	// And the redaction is visible rather than a silent drop, so a reader can tell a
	// redacted query from one that was never recorded.
	reopened, err := record.Open(ctx, dir, record.NewRedactor([]string{testsecret.Value}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	retrievals, err := reopened.Retrievals(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(retrievals) != 1 {
		t.Fatalf("read back %d retrievals, want 1", len(retrievals))
	}
	if retrievals[0].Query != "reaching for "+record.Placeholder {
		t.Errorf("the query read back as %q", retrievals[0].Query)
	}
	if retrievals[0].Error != "authenticating with "+record.Placeholder {
		t.Errorf("the error read back as %q", retrievals[0].Error)
	}
}
