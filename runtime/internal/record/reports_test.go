package record_test

import (
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// TestIndexableReports is data-model.md's rule, stated on two facts rather than on a list
// of trigger kinds: a run is a source document when its run recorded a report and derives
// from no other run.
func TestIndexableReports(t *testing.T) {
	ctx := t.Context()
	store := openStore(t)
	started := time.Now().UTC()

	create := func(id, playbook, reportRef, parent string) {
		t.Helper()
		if err := store.CreateRun(ctx, record.Run{
			ID: id, PlaybookName: playbook, TriggerKind: record.TriggerManual,
			Status: record.StatusSucceeded, StartedAt: started, ReportRef: reportRef,
			ParentRunID: parent,
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Order matters: a parent has to exist before a row can name it, and the table below
	// is read back ordered by identifier rather than by insertion order.
	create("run-2-original", "disk-check", "blob-original", "")
	create("run-4-failed", "disk-check", "", "")
	create("run-5-replay", "disk-check", "blob-replay", "run-2-original")
	create("run-6-resume", "disk-check", "blob-resume", "run-2-original")
	create("run-1-other-playbook", "other-check", "blob-other", "")

	got, err := store.IndexableReports(ctx, []string{"disk-check"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].RunID != "run-2-original" || got[0].ReportRef != "blob-original" {
		t.Fatalf("indexable reports of disk-check = %+v, want exactly run-2-original", got)
	}

	if got, err := store.IndexableReports(ctx, nil); err != nil || len(got) != 0 {
		t.Fatalf("no playbooks named = %v, %v; want none", got, err)
	}
	if got, err := store.IndexableReports(ctx, []string{"nothing-runs-this"}); err != nil || len(got) != 0 {
		t.Fatalf("a playbook nothing recorded = %v, %v; want none", got, err)
	}

	both, err := store.IndexableReports(ctx, []string{"disk-check", "other-check"})
	if err != nil {
		t.Fatal(err)
	}
	if len(both) != 2 || both[0].RunID != "run-1-other-playbook" || both[1].RunID != "run-2-original" {
		t.Fatalf("indexable reports of both playbooks = %+v, want run-1-other-playbook then run-2-original", both)
	}
}
