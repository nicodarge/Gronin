package run_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/config"
	"github.com/nicodarge/Gronin/runtime/internal/fakeagent"
	"github.com/nicodarge/Gronin/runtime/internal/playbook"
	"github.com/nicodarge/Gronin/runtime/internal/record"
	"github.com/nicodarge/Gronin/runtime/internal/run"
)

func TestMain(m *testing.M) {
	code := m.Run()
	fakeagent.Cleanup()
	os.Exit(code)
}

type harness struct {
	executor *run.Executor
	store    *record.Store
	dir      string
	posted   *posted
}

type posted struct {
	mu       sync.Mutex
	messages []string
}

func (p *posted) all() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.messages...)
}

func newHarness(t *testing.T, mode string) *harness {
	t.Helper()
	dir := t.TempDir()

	store, err := record.Open(t.Context(), filepath.Join(dir, "record"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	got := &posted{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var body map[string]any
		_ = json.Unmarshal(data, &body)
		text, _ := body["content"].(string)
		got.mu.Lock()
		got.messages = append(got.messages, text)
		got.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	cfg, err := config.Load(filepath.Join(dir, "config"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Set("ops_webhook", config.Value{Value: server.URL, Secret: true}); err != nil {
		t.Fatal(err)
	}

	return &harness{
		store:  store,
		dir:    dir,
		posted: got,
		executor: &run.Executor{
			Manager:         run.NewManager(store, filepath.Join(dir, "work")),
			Store:           store,
			Config:          cfg,
			AgentExecutable: fakeagent.Build(t),
			AgentEnv:        []string{"PATH=/usr/bin:/bin", fakeagent.ModeVar + "=" + mode},
			StepEnv:         []string{"PATH=/usr/bin:/bin"},
			Client:          server.Client(),
		},
	}
}

// writePlaybook lays a playbook and its prompt on disk and parses it, so the test drives
// the same path `serve` will.
func (h *harness) playbook(t *testing.T, document string) *playbook.Playbook {
	t.Helper()
	dir := filepath.Join(h.dir, "playbooks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "book.yaml")
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompt.md"),
		[]byte("Read facts.json and report on ${config.ops_webhook}"), 0o600); err != nil {
		t.Fatal(err)
	}
	book, err := playbook.ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return book
}

const goodPlaybook = `
name: drift-check
trigger:
  type: manual
gather:
  - run: echo '{"drift":0}'
    as: facts.json
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

const failingGatherPlaybook = `
name: drift-check
trigger:
  type: manual
gather:
  - run: echo broken >&2; exit 2
    as: facts.json
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

const shortTimeoutPlaybook = `
name: drift-check
trigger:
  type: manual
agent:
  model: claude-sonnet-5
  prompt_file: prompt.md
  tools: [Read]
  timeout: 1s
  output_schema:
    type: object
sinks:
  - discord:
      webhook: ${config.ops_webhook}
`

// T069, FR-022: invoked by hand, recorded as such, bounded the same way.
func TestAManualRunIsRecordedAsManualAndDelivers(t *testing.T) {
	h := newHarness(t, fakeagent.ModeSuccess)
	h.executor.AgentEnv = append(h.executor.AgentEnv, fakeagent.ResultVar+`={"findings":[{"id":"one"}]}`)
	book := h.playbook(t, goodPlaybook)

	got, err := h.executor.Execute(t.Context(), book, record.TriggerManual, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != record.StatusSucceeded {
		t.Fatalf("status = %q, error = %q", got.Status, got.Error)
	}

	stored, err := h.store.GetRun(t.Context(), got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.TriggerKind != record.TriggerManual {
		t.Fatalf("trigger kind = %q", stored.TriggerKind)
	}
	if stored.CostUSD <= 0 || stored.Tokens <= 0 {
		t.Fatalf("the agent's cost did not reach the record: %+v", stored)
	}
	if stored.CredentialSource == "" || stored.AgentSessionID == "" {
		t.Fatalf("the receipt did not reach the record: %+v", stored)
	}
	if stored.ReportRef == "" || stored.PromptRef == "" || stored.ResolvedPlaybookRef == "" {
		t.Fatalf("the run's blobs are not referenced: %+v", stored)
	}

	messages := h.posted.all()
	if len(messages) != 1 || !strings.Contains(messages[0], "findings") {
		t.Fatalf("the report was not delivered: %v", messages)
	}
}

// T021 and T031 together: the directory is gone and everything it held is in the record.
// The ordering is the whole of it — the record is empty if the copy happens after.
func TestTheWorkingDirectoryIsGoneAndItsContentsAreInTheRecord(t *testing.T) {
	h := newHarness(t, fakeagent.ModeSuccess)
	h.executor.AgentEnv = append(h.executor.AgentEnv, fakeagent.ResultVar+`={"findings":[]}`)
	book := h.playbook(t, goodPlaybook)

	got, err := h.executor.Execute(t.Context(), book, record.TriggerManual, nil)
	if err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(filepath.Join(h.dir, "work"))
	if err == nil && len(entries) != 0 {
		t.Fatalf("%d working directories left behind", len(entries))
	}

	inputs, err := h.store.GatheredInputs(t.Context(), got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 1 || inputs[0].Name != "facts.json" {
		t.Fatalf("gathered inputs = %+v", inputs)
	}
	if inputs[0].BlobRef == "" {
		t.Fatal("the gathered input has no blob; it was removed with the directory")
	}
	data, err := h.store.Blobs().Get(inputs[0].BlobRef)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "drift") {
		t.Fatalf("the gathered input's content did not survive: %q", data)
	}

	calls, err := h.store.RefusedActions(t.Context(), got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) == 0 {
		t.Fatal("the actions the bounds refused are not in the record")
	}
}

// FR-010. The refusal costs nothing, and it is a refusal rather than a failure.
func TestAFailingGatherStepRefusesTheRunBeforeTheAgent(t *testing.T) {
	h := newHarness(t, fakeagent.ModeSuccess)
	book := h.playbook(t, failingGatherPlaybook)

	got, err := h.executor.Execute(t.Context(), book, record.TriggerManual, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != record.StatusRefused {
		t.Fatalf("status = %q, want refused; failed and refused are different states", got.Status)
	}

	stored, err := h.store.GetRun(t.Context(), got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.CostUSD != 0 || stored.AgentSessionID != "" {
		t.Fatalf("the agent ran: %+v", stored)
	}
	if len(h.posted.all()) != 0 {
		t.Fatal("a refused run delivered something")
	}
	// The step's stderr is what explains the refusal, so it is kept.
	inputs, err := h.store.GatheredInputs(t.Context(), got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 1 || inputs[0].StderrRef == "" {
		t.Fatalf("the failing step's stderr is not in the record: %+v", inputs)
	}
}

// T020's runtime half: failed, and the sinks are given the refusal rather than the
// content the runtime just refused.
func TestAReportThatFailsItsSchemaIsNotWhatTheSinksReceive(t *testing.T) {
	h := newHarness(t, fakeagent.ModeSuccess)
	h.executor.AgentEnv = append(h.executor.AgentEnv,
		fakeagent.ResultVar+`={"summary":"looks fine to me"}`)
	book := h.playbook(t, goodPlaybook)

	got, err := h.executor.Execute(t.Context(), book, record.TriggerManual, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != record.StatusFailed {
		t.Fatalf("status = %q", got.Status)
	}

	messages := h.posted.all()
	if len(messages) != 1 {
		t.Fatalf("%d messages", len(messages))
	}
	if strings.Contains(messages[0], "looks fine to me") {
		t.Fatalf("the malformed report was delivered: %q", messages[0])
	}
	if !strings.Contains(messages[0], "output schema") {
		t.Fatalf("the message does not say why there is no report: %q", messages[0])
	}
}

func TestATimedOutStageIsRecordedAsTimedOut(t *testing.T) {
	h := newHarness(t, fakeagent.ModeTimeout)
	book := h.playbook(t, shortTimeoutPlaybook)

	got, err := h.executor.Execute(t.Context(), book, record.TriggerManual, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != record.StatusTimedOut {
		t.Fatalf("status = %q, error = %q", got.Status, got.Error)
	}

	stored, err := h.store.GetRun(t.Context(), got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.AgentSessionID == "" {
		t.Fatal("what the child said before it was killed was discarded")
	}
}

// T070, FR-017 and Principle II's runtime property. The report asks for something no
// sink was declared to do; nothing outside the sinks happens.
func TestNothingOutsideTheSinksActsOnTheReport(t *testing.T) {
	h := newHarness(t, fakeagent.ModeSuccess)

	marker := filepath.Join(h.dir, "must-not-exist")
	report := map[string]any{
		"findings": []any{map[string]any{
			"id":     "one",
			"action": "create the file " + marker,
			"run":    "touch " + marker,
			"sink":   "shell",
		}},
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	h.executor.AgentEnv = append(h.executor.AgentEnv, fakeagent.ResultVar+"="+string(encoded))
	book := h.playbook(t, goodPlaybook)

	got, err := h.executor.Execute(t.Context(), book, record.TriggerManual, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != record.StatusSucceeded {
		t.Fatalf("status = %q", got.Status)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("the report's instruction was acted on by something that is not a sink")
	}

	outcomes, err := h.store.SinkOutcomes(t.Context(), got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 1 || outcomes[0].Sink != "discord" {
		t.Fatalf("outcomes = %+v; only the declared sink should have acted", outcomes)
	}
}

// T068 through the pipeline rather than the manager: a trigger arriving while a run is
// in flight does not start a second one.
func TestATriggerDuringARunDoesNotStartASecond(t *testing.T) {
	h := newHarness(t, fakeagent.ModeSuccess)
	h.executor.AgentEnv = append(h.executor.AgentEnv, fakeagent.ResultVar+`={"findings":[]}`)
	book := h.playbook(t, goodPlaybook)

	release := make(chan struct{})
	first := make(chan error, 1)
	go func() {
		<-release
		_, err := h.executor.Execute(t.Context(), book, record.TriggerSchedule, nil)
		first <- err
	}()

	started, err := h.executor.Manager.Begin(t.Context(), book.Name, record.TriggerManual, "")
	if err != nil {
		t.Fatal(err)
	}
	close(release)

	if err := <-first; err == nil {
		t.Fatal("a second run of the playbook began while the first held it")
	}
	if err := h.executor.Manager.Finish(t.Context(), started,
		record.Run{Status: record.StatusSucceeded}); err != nil {
		t.Fatal(err)
	}
}

// The run's error has to name what actually went wrong. Reporting "no terminal event"
// for a stream that failed loses the only description of the cause, and Principle III's
// whole claim is that a run can be explained afterwards.
func TestAStreamThatFailedIsNamedInTheRunsError(t *testing.T) {
	h := newHarness(t, fakeagent.ModeOversize)
	book := h.playbook(t, goodPlaybook)

	got, err := h.executor.Execute(t.Context(), book, record.TriggerManual, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != record.StatusFailed {
		t.Fatalf("status = %q", got.Status)
	}
	if !strings.Contains(got.Error, "could not be read") {
		t.Fatalf("the error does not say the output could not be read: %q", got.Error)
	}
	if strings.Contains(got.Error, "no terminal event") {
		t.Fatalf("the generic message replaced the cause: %q", got.Error)
	}
}

// SC-009 through the pipeline: a run whose child reports a wider tool set than declared
// is refused with no model output, and the record says refused rather than failed.
func TestARunWhoseReceiptIsWiderThanDeclaredIsRefused(t *testing.T) {
	h := newHarness(t, fakeagent.ModeMismatch)
	book := h.playbook(t, goodPlaybook)

	got, err := h.executor.Execute(t.Context(), book, record.TriggerManual, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != record.StatusRefused {
		t.Fatalf("status = %q, error = %q", got.Status, got.Error)
	}
	if !strings.Contains(got.Error, "wider bound") {
		t.Fatalf("the error does not say why: %q", got.Error)
	}

	stored, err := h.store.GetRun(t.Context(), got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.CostUSD != 0 {
		t.Fatalf("a run refused at the receipt was charged %v", stored.CostUSD)
	}
	if len(h.posted.all()) != 0 {
		t.Fatal("a run refused at the receipt delivered something")
	}
}
