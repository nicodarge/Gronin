package run_test

import (
	"encoding/json"
	"errors"
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
	os.Exit(runAndCleanUp(m))
}

// runAndCleanUp defers fakeagent.Cleanup around m.Run() rather than calling it as a
// plain statement after: os.Exit never returns, so a cleanup placed after m.Run() in
// TestMain itself would never run at all. Deferring it here still runs it if m.Run()
// itself panics before returning; it does not reach a panic from inside an actual Test
// function, since each runs in its own goroutine and an unrecovered panic there
// crashes the whole process before any other goroutine's defers get to run — see
// bintest.Main's doc comment, which this mirrors.
func runAndCleanUp(m *testing.M) (code int) {
	defer fakeagent.Cleanup()
	return m.Run()
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
			Manager:         run.NewManager(store, filepath.Join(dir, "work"), filepath.Join(dir, "locks")),
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

	got, err := h.executor.Execute(t.Context(), book, run.Trigger{Kind: record.TriggerManual})
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

	got, err := h.executor.Execute(t.Context(), book, run.Trigger{Kind: record.TriggerManual})
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

	got, err := h.executor.Execute(t.Context(), book, run.Trigger{Kind: record.TriggerManual})
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

	got, err := h.executor.Execute(t.Context(), book, run.Trigger{Kind: record.TriggerManual})
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

	got, err := h.executor.Execute(t.Context(), book, run.Trigger{Kind: record.TriggerManual})
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

	got, err := h.executor.Execute(t.Context(), book, run.Trigger{Kind: record.TriggerManual})
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
		_, err := h.executor.Execute(t.Context(), book, run.Trigger{Kind: record.TriggerSchedule})
		first <- err
	}()

	started, err := begin(t, h.executor.Manager, book.Name, record.TriggerManual)
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

	got, err := h.executor.Execute(t.Context(), book, run.Trigger{Kind: record.TriggerManual})
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

	got, err := h.executor.Execute(t.Context(), book, run.Trigger{Kind: record.TriggerManual})
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

// FR-027. A replay re-runs the agent against the SAME inputs: re-gathering would change
// the question being asked, and firing the trigger would make a diagnostic tool a cause
// of load.
func TestAReplayReusesTheRecordedInputsAndDoesNotGatherAgain(t *testing.T) {
	h := newHarness(t, fakeagent.ModeSuccess)
	h.executor.AgentEnv = append(h.executor.AgentEnv, fakeagent.ResultVar+`={"findings":[]}`)

	// A gather step that appends every time it runs, so a second execution is visible.
	marker := filepath.Join(h.dir, "gathers")
	book := h.playbook(t, strings.Replace(goodPlaybook,
		"    as: facts.json", "    as: facts.json", 1))
	book.Gather[0].Run = "echo ran >> " + marker + "; echo '{\"drift\":0}'"

	first, err := h.executor.Execute(t.Context(), book, run.Trigger{Kind: record.TriggerManual})
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != record.StatusSucceeded {
		t.Fatalf("the first run did not succeed: %q %q", first.Status, first.Error)
	}

	replayed, err := h.executor.Replay(t.Context(), first.ID, book)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Status != record.StatusSucceeded {
		t.Fatalf("the replay did not succeed: %q %q", replayed.Status, replayed.Error)
	}
	if replayed.ID == first.ID {
		t.Fatal("the replay reused the run it derives from")
	}

	// The gather step ran once, for the original.
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(string(data), "ran"); lines != 1 {
		t.Fatalf("gather ran %d times; a replay does not re-gather", lines)
	}

	stored, err := h.store.GetRun(t.Context(), replayed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.TriggerKind != record.TriggerReplay {
		t.Fatalf("trigger kind = %q", stored.TriggerKind)
	}
	if stored.ParentRunID != first.ID {
		t.Fatalf("the replay is not linked to its parent: %q", stored.ParentRunID)
	}
	// The inputs are in the replay's own record too, or the replay is unreadable on its
	// own terms.
	inputs, err := h.store.GatheredInputs(t.Context(), replayed.ID)
	if err != nil || len(inputs) != 1 {
		t.Fatalf("inputs = %+v, err = %v", inputs, err)
	}
	// And it cost money, which a record that hid it would understate.
	if stored.CostUSD <= 0 {
		t.Fatalf("the replay recorded no cost: %+v", stored)
	}
}

// FR-028, SC-004. A sink that was down should not cost a second agent run.
func TestAResumeDeliversAgainWithoutRunningTheAgent(t *testing.T) {
	h := newHarness(t, fakeagent.ModeSuccess)
	h.executor.AgentEnv = append(h.executor.AgentEnv, fakeagent.ResultVar+`={"findings":[{"id":"one"}]}`)
	book := h.playbook(t, goodPlaybook)

	first, err := h.executor.Execute(t.Context(), book, run.Trigger{Kind: record.TriggerManual})
	if err != nil {
		t.Fatal(err)
	}
	delivered := len(h.posted.all())
	if delivered != 1 {
		t.Fatalf("%d messages from the first run", delivered)
	}

	// The agent is made unusable, so a resume that ran it would fail loudly rather than
	// silently costing money.
	h.executor.AgentExecutable = "/nonexistent/claude"

	resumed, err := h.executor.Resume(t.Context(), first.ID, book)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != record.StatusSucceeded {
		t.Fatalf("status = %q, error = %q", resumed.Status, resumed.Error)
	}

	if got := len(h.posted.all()); got != delivered+1 {
		t.Fatalf("%d messages after the resume, want %d", got, delivered+1)
	}

	stored, err := h.store.GetRun(t.Context(), resumed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.TriggerKind != record.TriggerResume || stored.ParentRunID != first.ID {
		t.Fatalf("recorded as %+v", stored)
	}
	// SC-004: zero additional token cost. The agent did not run.
	if stored.CostUSD != 0 || stored.Tokens != 0 {
		t.Fatalf("a resume reported cost %v and %d tokens", stored.CostUSD, stored.Tokens)
	}
	if stored.AgentSessionID != "" {
		t.Fatalf("a resume recorded an agent session: %q", stored.AgentSessionID)
	}
}

func TestARunWithNoReportCannotBeResumed(t *testing.T) {
	h := newHarness(t, fakeagent.ModeSuccess)
	h.executor.AgentEnv = append(h.executor.AgentEnv, fakeagent.ResultVar+`={"summary":"no findings key"}`)
	book := h.playbook(t, goodPlaybook)

	failed, err := h.executor.Execute(t.Context(), book, run.Trigger{Kind: record.TriggerManual})
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != record.StatusFailed {
		t.Fatalf("status = %q", failed.Status)
	}

	if _, err := h.executor.Resume(t.Context(), failed.ID, book); !errors.Is(err, run.ErrNotResumable) {
		t.Fatalf("err = %v, want ErrNotResumable", err)
	}
}

// T049, SC-003. From the record alone: what the agent was asked, what it answered, and
// what each sink did.
func TestACompletedRunCanBeExplainedFromItsRecordAlone(t *testing.T) {
	h := newHarness(t, fakeagent.ModeSuccess)
	h.executor.AgentEnv = append(h.executor.AgentEnv, fakeagent.ResultVar+`={"findings":[{"id":"one"}]}`)
	book := h.playbook(t, goodPlaybook)

	got, err := h.executor.Execute(t.Context(), book, run.Trigger{Kind: record.TriggerManual})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	stored, err := h.store.GetRun(ctx, got.ID)
	if err != nil {
		t.Fatal(err)
	}

	// What it was asked.
	prompt, err := h.store.Blobs().Get(stored.PromptRef)
	if err != nil || len(prompt) == 0 {
		t.Fatalf("the prompt as sent is not readable: %v", err)
	}
	// What it answered.
	report, err := h.store.Blobs().Get(stored.ReportRef)
	if err != nil || !strings.Contains(string(report), "findings") {
		t.Fatalf("the report is not readable: %q %v", report, err)
	}
	// The playbook as it actually ran, so the record survives the file changing.
	if _, err := h.store.Blobs().Get(stored.ResolvedPlaybookRef); err != nil {
		t.Fatalf("the resolved playbook is not readable: %v", err)
	}
	// What each sink did.
	outcomes, err := h.store.SinkOutcomes(ctx, got.ID)
	if err != nil || len(outcomes) == 0 {
		t.Fatalf("no sink outcome was recorded: %v", err)
	}
	// Every tool call, with its input and its answer.
	calls, err := h.store.ToolCalls(ctx, got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) == 0 {
		t.Fatal("no tool call was recorded")
	}
	if calls[0].InputRef == "" || calls[0].OutputRef == "" {
		t.Fatalf("a tool call has no input or no output: %+v", calls[0])
	}
	// And every action the bounds refused.
	refused, err := h.store.RefusedActions(ctx, got.ID)
	if err != nil || len(refused) == 0 {
		t.Fatalf("the refused actions are not in the record: %v", err)
	}
}

// The record keeps the playbook as it actually ran so it stays readable after the file
// changes. Rebuilding the bound from disk would let a replay run under a wider
// declaration than the one whose inputs it reuses — silently, which is the part that
// matters.
func TestAReplayRefusesAPlaybookThatHasChanged(t *testing.T) {
	h := newHarness(t, fakeagent.ModeSuccess)
	h.executor.AgentEnv = append(h.executor.AgentEnv, fakeagent.ResultVar+`={"findings":[]}`)
	book := h.playbook(t, goodPlaybook)

	first, err := h.executor.Execute(t.Context(), book, run.Trigger{Kind: record.TriggerManual})
	if err != nil {
		t.Fatal(err)
	}

	// The same playbook, widened the way an edit between the run and the replay would.
	widened := h.playbook(t, strings.Replace(goodPlaybook, "  tools: [Read]", "  tools: [Read, Grep]", 1))

	if _, err := h.executor.Replay(t.Context(), first.ID, widened); !errors.Is(err, run.ErrPlaybookChanged) {
		t.Fatalf("err = %v, want ErrPlaybookChanged", err)
	}
	if _, err := h.executor.Resume(t.Context(), first.ID, widened); !errors.Is(err, run.ErrPlaybookChanged) {
		t.Fatalf("resume err = %v, want ErrPlaybookChanged", err)
	}

	// And the unchanged one still replays.
	if _, err := h.executor.Replay(t.Context(), first.ID, book); err != nil {
		t.Fatalf("an unchanged playbook was refused: %v", err)
	}
}

// The record keeps the playbook's content, not where it was read from. Moving the
// directory, or replaying with a differently-spelled --playbooks flag than the cron job
// used, must not read as a playbook that changed.
func TestAReplayIsNotRefusedBecauseThePlaybookMoved(t *testing.T) {
	h := newHarness(t, fakeagent.ModeSuccess)
	h.executor.AgentEnv = append(h.executor.AgentEnv, fakeagent.ResultVar+`={"findings":[]}`)
	book := h.playbook(t, goodPlaybook)

	first, err := h.executor.Execute(t.Context(), book, run.Trigger{Kind: record.TriggerManual})
	if err != nil {
		t.Fatal(err)
	}

	// The same content, read from somewhere else entirely.
	elsewhere := t.TempDir()
	for name, body := range map[string]string{"book.yaml": goodPlaybook, "prompt.md": "x"} {
		if err := os.WriteFile(filepath.Join(elsewhere, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	moved, err := playbook.ParseFile(filepath.Join(elsewhere, "book.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if moved.Path == book.Path {
		t.Fatal("the fixture did not actually move the playbook")
	}

	if _, err := h.executor.Replay(t.Context(), first.ID, moved); err != nil {
		t.Fatalf("a playbook that only moved was refused: %v", err)
	}
}
