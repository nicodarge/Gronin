package run_test

import (
	"os"
	"strings"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/fakeagent"
	"github.com/nicodarge/Gronin/runtime/internal/record"
	"github.com/nicodarge/Gronin/runtime/internal/run"
)

// SC-219. A retrieval that cannot run as declared refuses the run, recorded refused and
// not failed, before any agent starts; and its row says refused, with no item. A missing
// directory read as an empty one would let the agent run told that nothing was found.
func TestARetrievalThatCannotRunRefuses(t *testing.T) {
	for name, probe := range map[string]struct {
		symptom      string
		removeSource bool
		names        func(directory string) string
	}{
		"a collection whose directory is missing": {
			symptom: "journal", removeSource: true,
			names: func(directory string) string { return directory },
		},
		"a query that resolves to whitespace": {
			symptom: "  \t ",
			names:   func(string) string { return "empty" },
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, fakeagent.ModeSuccess)
			directory, _ := h.retrieving(t, runbookDocuments)
			mark := h.quoting(t, "runbooks.md")
			book := h.playbook(t, retrievingPlaybook)
			if probe.removeSource {
				if err := os.RemoveAll(directory); err != nil {
					t.Fatal(err)
				}
			}

			got, err := h.executor.Execute(t.Context(), book, run.Trigger{
				Kind: record.TriggerManual, Values: map[string]string{"symptom": probe.symptom},
			})
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != record.StatusRefused {
				t.Fatalf("status = %q (%s), want refused", got.Status, got.Error)
			}
			if !strings.Contains(got.Error, probe.names(directory)) {
				t.Errorf("the refusal does not name its cause %q: %s", probe.names(directory), got.Error)
			}
			if !strings.Contains(got.Error, "retrieve[0]") || !strings.Contains(got.Error, "runbooks") {
				t.Errorf("the refusal does not name the retrieval and its collection: %s", got.Error)
			}
			if _, err := os.Stat(mark); !os.IsNotExist(err) {
				t.Error("the agent was started for a run a retrieval refused")
			}

			retrievals, err := h.store.Retrievals(t.Context(), got.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(retrievals) != 1 {
				t.Fatalf("%d retrievals recorded, want the refused one", len(retrievals))
			}
			if retrievals[0].Outcome != record.RetrievalRefused || len(retrievals[0].Items) != 0 ||
				retrievals[0].ResultsRef != "" {
				t.Errorf("the refused retrieval was recorded as %+v", retrievals[0])
			}
			if !strings.Contains(retrievals[0].Error, probe.names(directory)) {
				t.Errorf("the recorded retrieval does not name its cause: %q", retrievals[0].Error)
			}
		})
	}
}

// SC-219's other side: a search that ran and matched nothing is not a refusal. The run
// proceeds, the agent is told nothing was found, and the record says the search was empty.
func TestNothingFoundIsSaid(t *testing.T) {
	h := newHarness(t, fakeagent.ModeSuccess)
	h.retrieving(t, runbookDocuments)
	h.quoting(t, "runbooks.md")
	book := h.playbook(t, retrievingPlaybook)

	got, err := h.executor.Execute(t.Context(), book, run.Trigger{
		Kind: record.TriggerManual, Values: map[string]string{"symptom": "kubernetes"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != record.StatusSucceeded {
		t.Fatalf("status = %q (%s), want succeeded", got.Status, got.Error)
	}
	quoted, _ := quotedBy(t, h.store, got.ID, "runbooks.md").(string)
	if !strings.Contains(quoted, "Nothing in this collection matched the query.") {
		t.Errorf("the agent was not told nothing was found: %q", quoted)
	}

	retrievals, err := h.store.Retrievals(t.Context(), got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(retrievals) != 1 || retrievals[0].Outcome != record.RetrievalEmpty || len(retrievals[0].Items) != 0 {
		t.Fatalf("the retrieval was recorded as %+v, want empty with no item", retrievals)
	}
}
