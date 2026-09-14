package run_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/collections"
	"github.com/nicodarge/Gronin/runtime/internal/fakeagent"
	"github.com/nicodarge/Gronin/runtime/internal/record"
	"github.com/nicodarge/Gronin/runtime/internal/run"
	"github.com/nicodarge/Gronin/runtime/internal/stage/retrieve"
)

// digestCheckPlaybook is the playbook whose reports the collection under test indexes.
// Its schema requires findings, so a report naming none fails it.
const digestCheckPlaybook = `
name: digest-check
trigger:
  type: manual
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

// digestSearchPlaybook retrieves from a collection over digest-check's reports.
const digestSearchPlaybook = `
name: digest-search
trigger:
  type: manual
retrieve:
  - collection: conclusions
    as: conclusions.md
    query: JOURNALVACUUMED
agent:
  model: claude-sonnet-5
  prompt_file: prompt.md
  tools: [Read]
  output_schema:
    type: object
sinks:
  - discord:
      webhook: ${config.ops_webhook}
`

// retrievingReports gives the harness's executor a retrieve stage over one lexical
// collection, conclusions, whose source is the recorded reports of the named playbooks.
func (h *harness) retrievingReports(t *testing.T, playbooks []string) {
	t.Helper()
	state := filepath.Join(h.dir, "reports-state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	catalogue, err := json.Marshal(map[string]any{"conclusions": map[string]any{"reports": playbooks}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "collections.json"), catalogue, 0o600); err != nil {
		t.Fatal(err)
	}
	declared, err := collections.Load(state)
	if err != nil {
		t.Fatal(err)
	}
	h.executor.Retrieve = &retrieve.Stage{
		Catalog:  declared,
		IndexDir: filepath.Join(state, "index"),
		Config:   h.executor.Config,
		Redactor: record.NewRedactor(h.executor.Config.Secrets()),
		Store:    h.store,
	}
}

// SC-208. A run whose report validated, a run whose report did not, a replay of the first
// and a resume of it: a collection over that playbook's reports returns exactly one
// result, naming the first run — FR-214. The resume's report is a copy of the original's
// and the replay is given the same one, so an implementation that includes either returns
// more than one.
func TestOnlyOriginalValidatedReportsAreRetrieved(t *testing.T) {
	h := newHarness(t, fakeagent.ModeSuccess)
	book := h.playbook(t, digestCheckPlaybook)

	validated := fakeagent.ResultVar + `={"findings":[{"id":"JOURNALVACUUMED"}]}`
	h.executor.AgentExecutable = fakeagent.Wrapped(t, validated)
	original, err := h.executor.Execute(t.Context(), book, run.Trigger{Kind: record.TriggerManual})
	if err != nil {
		t.Fatal(err)
	}
	if original.Status != record.StatusSucceeded {
		t.Fatalf("the original run did not succeed: %s %s", original.Status, original.Error)
	}

	h.executor.AgentExecutable = fakeagent.Wrapped(t, fakeagent.ResultVar+`={"summary":"no findings key"}`)
	failed, err := h.executor.Execute(t.Context(), book, run.Trigger{Kind: record.TriggerManual})
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != record.StatusFailed {
		t.Fatalf("the schema-failing run did not fail: %s %s", failed.Status, failed.Error)
	}

	h.executor.AgentExecutable = fakeagent.Wrapped(t, validated)
	replayed, err := h.executor.Replay(t.Context(), original.ID, book)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Status != record.StatusSucceeded {
		t.Fatalf("the replay did not succeed: %s %s", replayed.Status, replayed.Error)
	}

	resumed, err := h.executor.Resume(t.Context(), original.ID, book)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != record.StatusSucceeded {
		t.Fatalf("the resume did not succeed: %s %s", resumed.Status, resumed.Error)
	}

	h.retrievingReports(t, []string{"digest-check"})
	h.quoting(t, "conclusions.md")
	search := h.playbook(t, digestSearchPlaybook)
	got, err := h.executor.Execute(t.Context(), search, run.Trigger{Kind: record.TriggerManual})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != record.StatusSucceeded {
		t.Fatalf("the retrieving run did not succeed: %s %s", got.Status, got.Error)
	}

	retrievals, err := h.store.Retrievals(t.Context(), got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(retrievals) != 1 {
		t.Fatalf("%d retrievals recorded, want 1", len(retrievals))
	}
	if retrievals[0].Outcome != record.RetrievalFound || len(retrievals[0].Items) != 1 {
		t.Fatalf("the retrieval was recorded as %+v, want exactly one found result", retrievals[0])
	}
	if retrievals[0].Items[0].Source != original.ID {
		t.Errorf("the result names %q, want the original run %q", retrievals[0].Items[0].Source, original.ID)
	}

	quoted, _ := quotedBy(t, h.store, got.ID, "conclusions.md").(string)
	if !strings.Contains(quoted, "run "+original.ID) {
		t.Errorf("the results file does not name the run its result came from:\n%s", quoted)
	}
	if !strings.Contains(quoted, "JOURNALVACUUMED") {
		t.Errorf("the results file does not hold the matching passage:\n%s", quoted)
	}
	for _, other := range []string{failed.ID, replayed.ID, resumed.ID} {
		if strings.Contains(quoted, other) {
			t.Errorf("the results file names %s, which is not the original run:\n%s", other, quoted)
		}
	}
}
