package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/bintest"
	"github.com/nicodarge/Gronin/runtime/internal/fakeagent"
	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// retrievingDeployment is a state directory whose collections.json declares one lexical
// collection, runbooks, over a directory the test fills, and whose playbooks directory
// holds one playbook, disk-check.
type retrievingDeployment struct {
	state     string
	directory string
}

// newRetrievingDeployment lays the deployment out. SINK_URL in book is replaced by a
// loopback server that accepts every delivery, so a run that retrieved and reported
// exits zero.
func newRetrievingDeployment(t *testing.T, directory, book string) *retrievingDeployment {
	t.Helper()
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(sink.Close)

	state := t.TempDir()
	playbooks := filepath.Join(state, "playbooks")
	if err := os.MkdirAll(playbooks, 0o700); err != nil {
		t.Fatal(err)
	}
	catalogue, err := json.Marshal(map[string]any{"runbooks": map[string]any{"directory": directory}})
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		filepath.Join(state, "collections.json"):    string(catalogue),
		filepath.Join(playbooks, "disk-check.yaml"): strings.ReplaceAll(book, "SINK_URL", sink.URL),
		filepath.Join(playbooks, "prompt.md"):       "report on what was retrieved",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return &retrievingDeployment{state: state, directory: directory}
}

// writeDocuments puts files into a directory, creating it.
func writeDocuments(t *testing.T, dir string, docs map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range docs {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// run invokes disk-check through the executable with the stub agent quoting the named
// files, and returns the run's identifier and what the agent quoted.
func (d *retrievingDeployment) run(t *testing.T, quote string, args ...string) (string, map[string]any) {
	t.Helper()
	agent := fakeagent.Wrapped(t, fakeagent.QuoteVar+"="+quote)
	got := bintest.Run(t, append([]string{"run", "disk-check", "--state-dir", d.state, "--agent", agent}, args...)...)
	if got.ExitCode != 0 {
		t.Fatalf("gronin run exited %d:\n%s\n%s", got.ExitCode, got.Stdout, got.Stderr)
	}
	fields := strings.Fields(got.Stdout)
	if len(fields) == 0 {
		t.Fatalf("gronin run printed no run identifier: %q", got.Stderr)
	}
	return fields[0], d.quoted(t, fields[0])
}

// quoted reads a run's report from the record and returns what its agent quoted.
func (d *retrievingDeployment) quoted(t *testing.T, runID string) map[string]any {
	t.Helper()
	store, err := record.Open(t.Context(), filepath.Join(d.state, "record"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	stored, err := store.GetRun(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := store.Blobs().Get(stored.ReportRef)
	if err != nil {
		t.Fatalf("run %s recorded no report (%s): %v", runID, stored.Error, err)
	}
	var report struct {
		Quoted map[string]any `json:"quoted"`
	}
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	return report.Quoted
}

var threeRunbooks = map[string]string{
	"disk-full.md":   "# Disk full\n\nWhen /var fills, rotate the journal with its vacuum option.\n",
	"cert-expiry.md": "# Certificate expired\n\nRenew the certificate and reload the proxy.\n",
	"swap.md":        "# Swap exhausted\n\nFind the process holding memory.\n",
}

const twoRetrievalsPlaybook = `
name: disk-check
trigger:
  type: manual
gather:
  - run: printf 'certificate expired'
    as: facts.txt
retrieve:
  - collection: runbooks
    as: runbooks.md
    query: ${trigger.symptom}
  - collection: runbooks
    as: by-facts.md
    query_from: facts.txt
agent:
  model: claude-sonnet-5
  prompt_file: prompt.md
  tools: [Read]
  output_schema:
    type: object
sinks:
  - discord:
      webhook: SINK_URL
`

var retrievalLine = regexp.MustCompile(
	`retrieved runbooks\.md from runbooks \(lexical\) generation [0-9a-f]{12} built \S+: found \d+`)

// SC-201, through the executable. The agent quotes what it read from its working
// directory, so a stage that wrote its results after the agent had gone fails here, where
// one asserting only that the file exists would pass. The second retrieval's query is a
// gather step's output, which is why retrieval runs after gather (FR-202).
func TestTheAgentReadsWhatWasRetrieved(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "runbooks")
	writeDocuments(t, directory, threeRunbooks)
	deployment := newRetrievingDeployment(t, directory, twoRetrievalsPlaybook)

	id, quoted := deployment.run(t, "runbooks.md,by-facts.md", "--trigger", "symptom=journal vacuum")

	fromTrigger, _ := quoted["runbooks.md"].(string)
	if !strings.Contains(fromTrigger, "rotate the journal") {
		t.Errorf("the agent did not read the passage the query matches:\n%q", fromTrigger)
	}
	if strings.Contains(fromTrigger, "Renew the certificate") || strings.Contains(fromTrigger, "holding memory") {
		t.Errorf("the agent read passages the query does not match:\n%q", fromTrigger)
	}
	fromGather, _ := quoted["by-facts.md"].(string)
	if !strings.Contains(fromGather, "Renew the certificate") {
		t.Errorf("the retrieval formed from gathered input did not find what that input names:\n%q", fromGather)
	}

	shown := bintest.Run(t, "show", id, "--state-dir", deployment.state)
	if shown.ExitCode != 0 {
		t.Fatalf("gronin show exited %d: %s", shown.ExitCode, shown.Stderr)
	}
	if !retrievalLine.MatchString(shown.Stdout) {
		t.Errorf("gronin show does not name the collection, the mode and a generation:\n%s", shown.Stdout)
	}
	if !strings.Contains(shown.Stdout, "disk-full.md#") {
		t.Errorf("gronin show does not name the passage's source:\n%s", shown.Stdout)
	}
}
