package run_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/collections"
	"github.com/nicodarge/Gronin/runtime/internal/fakeagent"
	"github.com/nicodarge/Gronin/runtime/internal/record"
	"github.com/nicodarge/Gronin/runtime/internal/stage/retrieve"
)

// retrievingPlaybook retrieves from runbooks with a query the trigger supplies.
const retrievingPlaybook = `
name: disk-check
trigger:
  type: manual
retrieve:
  - collection: runbooks
    as: runbooks.md
    query: ${trigger.symptom}
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

var runbookDocuments = map[string]string{
	"disk-full.md":   "# Disk full\n\nRotate the journal when /var fills.\n",
	"cert-expiry.md": "# Certificate expired\n\nRenew the certificate and reload the proxy.\n",
	"swap.md":        "# Swap exhausted\n\nFind the process holding memory.\n",
}

// retrieving gives the harness's executor a retrieve stage over one lexical collection,
// runbooks, holding docs. It returns the collection's directory and the index directory.
func (h *harness) retrieving(t *testing.T, docs map[string]string) (directory, indexDir string) {
	t.Helper()
	state := filepath.Join(h.dir, "state")
	directory = filepath.Join(h.dir, "runbooks")
	for _, dir := range []string{state, directory} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range docs {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	catalogue, err := json.Marshal(map[string]any{"runbooks": map[string]any{"directory": directory}})
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
	indexDir = filepath.Join(state, "index")
	h.executor.Retrieve = &retrieve.Stage{
		Catalog:  declared,
		IndexDir: indexDir,
		Config:   h.executor.Config,
		Redactor: record.NewRedactor(h.executor.Config.Secrets()),
	}
	return directory, indexDir
}

// quoting makes the agent a stub that quotes the named files, behind a script that leaves
// a mark before it starts it. It returns the mark's path: a run that never reached the
// agent leaves none.
func (h *harness) quoting(t *testing.T, names string) string {
	t.Helper()
	mark := filepath.Join(h.dir, "agent-started")
	script := filepath.Join(h.dir, "agent.sh")
	body := "#!/bin/sh\n: > '" + mark + "'\nexec '" + fakeagent.Build(t) + "' \"$@\"\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	h.executor.AgentExecutable = script
	h.executor.AgentEnv = append(h.executor.AgentEnv, fakeagent.QuoteVar+"="+names)
	return mark
}

// quotedBy is what the agent of a run quoted from its working directory under name: the
// file's content, or nil when it was not there.
func quotedBy(t *testing.T, store *record.Store, runID, name string) any {
	t.Helper()
	stored, err := store.GetRun(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ReportRef == "" {
		t.Fatalf("run %s recorded no report: %s %s", runID, stored.Status, stored.Error)
	}
	data, err := store.Blobs().Get(stored.ReportRef)
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Quoted map[string]any `json:"quoted"`
	}
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	quoted, present := report.Quoted[name]
	if !present {
		t.Fatalf("the stub was not asked to quote %s: %s", name, data)
	}
	return quoted
}
