package run_test

import (
	"os"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/fakeagent"
	"github.com/nicodarge/Gronin/runtime/internal/record"
	"github.com/nicodarge/Gronin/runtime/internal/run"
)

// SC-215, the lexical half. After the run, the collection's index and its directory are
// both gone; the replay still hands the agent the results file the run's agent read, byte
// for byte, and records the same retrieval. A replay that searches again fails on what
// was removed.
func TestAReplayDoesNotSearch(t *testing.T) {
	h := newHarness(t, fakeagent.ModeSuccess)
	directory, indexDir := h.retrieving(t, runbookDocuments)
	h.quoting(t, "runbooks.md")
	book := h.playbook(t, retrievingPlaybook)

	original, err := h.executor.Execute(t.Context(), book, run.Trigger{
		Kind: record.TriggerManual, Values: map[string]string{"symptom": "journal"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if original.Status != record.StatusSucceeded {
		t.Fatalf("the original run did not succeed: %s %s", original.Status, original.Error)
	}
	read := quotedBy(t, h.store, original.ID, "runbooks.md")
	if text, ok := read.(string); !ok || text == "" {
		t.Fatalf("the original run's agent read no results file: %v", read)
	}

	for _, gone := range []string{indexDir, directory} {
		if err := os.RemoveAll(gone); err != nil {
			t.Fatal(err)
		}
	}

	replayed, err := h.executor.Replay(t.Context(), original.ID, book)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Status != record.StatusSucceeded {
		t.Fatalf("the replay did not succeed: %s %s", replayed.Status, replayed.Error)
	}
	if again := quotedBy(t, h.store, replayed.ID, "runbooks.md"); again != read {
		t.Fatalf("the replay's agent read a different results file:\n  original %q\n  replay   %v", read, again)
	}
	if _, err := os.Stat(indexDir); !os.IsNotExist(err) {
		t.Errorf("the replay recreated the index at %s", indexDir)
	}

	was, err := h.store.Retrievals(t.Context(), original.ID)
	if err != nil {
		t.Fatal(err)
	}
	now, err := h.store.Retrievals(t.Context(), replayed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(was) != 1 || len(now) != 1 {
		t.Fatalf("retrievals: original %d, replay %d, want one each", len(was), len(now))
	}
	if !sameRetrieval(t, h.store, was[0], now[0]) {
		t.Fatalf("the replay recorded a different retrieval:\n  original %+v\n  replay   %+v", was[0], now[0])
	}
}

// sameRetrieval compares two recorded retrievals by what they hold rather than by where
// their blobs are, which differs between a run and its replay.
func sameRetrieval(t *testing.T, store *record.Store, a, b record.Retrieval) bool {
	t.Helper()
	blob := func(ref string) string {
		if ref == "" {
			return ""
		}
		data, err := store.Blobs().Get(ref)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	if blob(a.ResultsRef) != blob(b.ResultsRef) || len(a.Items) != len(b.Items) {
		return false
	}
	for at := range a.Items {
		if blob(a.Items[at].ContentRef) != blob(b.Items[at].ContentRef) {
			return false
		}
		a.Items[at].ContentRef, b.Items[at].ContentRef = "", ""
		if a.Items[at] != b.Items[at] {
			return false
		}
	}
	a.ResultsRef, b.ResultsRef, a.Items, b.Items = "", "", nil, nil
	return a.Sequence == b.Sequence && a.AsName == b.AsName && a.Collection == b.Collection &&
		a.Mode == b.Mode && a.Generation == b.Generation && a.Identity == b.Identity &&
		a.GenerationBuiltAt.Equal(b.GenerationBuiltAt) && a.Query == b.Query &&
		a.QueryTruncated == b.QueryTruncated && a.Outcome == b.Outcome &&
		a.ResultsBytes == b.ResultsBytes && a.CountTruncated == b.CountTruncated &&
		a.BytesTruncated == b.BytesTruncated && a.Error == b.Error
}
