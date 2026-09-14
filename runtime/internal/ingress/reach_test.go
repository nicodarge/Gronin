package ingress_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/config"
	"github.com/nicodarge/Gronin/runtime/internal/fakeagent"
	"github.com/nicodarge/Gronin/runtime/internal/ingress"
	"github.com/nicodarge/Gronin/runtime/internal/playbook"
	"github.com/nicodarge/Gronin/runtime/internal/record"
	"github.com/nicodarge/Gronin/runtime/internal/run"
)

func TestMain(m *testing.M) {
	code := m.Run()
	fakeagent.Cleanup()
	os.Exit(code)
}

// sentinel is content no playbook here declares. Its whole purpose in this test is to be
// looked for and not found (SC-318).
const sentinel = "top-secret-do-not-leak"

// sinkCapture records every body a destination received.
type sinkCapture struct {
	mu     sync.Mutex
	bodies []string
}

func (c *sinkCapture) add(body string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bodies = append(c.bodies, body)
}

func (c *sinkCapture) all() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.bodies...)
}

// executorDispatcher is ingress.Dispatcher backed by a real run.Executor, the way
// cmd/gronin's dispatcher (T054) will be, minus the guard: no listener drives this test,
// so the delivery is handed to Execute directly.
type executorDispatcher struct {
	executor *run.Executor
}

func (d *executorDispatcher) Dispatch(
	ctx context.Context, book *playbook.Playbook, _ record.Delivery, values map[string]string,
) record.HandOffState {
	finished, err := d.executor.Execute(ctx, book, run.Trigger{
		Kind: record.TriggerWebhook, Values: values,
	})
	if err != nil || finished.Status != record.StatusSucceeded {
		return record.HandOffRefused
	}
	return record.HandOffHandedOff
}

// T021, SC-318. A delivery declares one value, "marker", and carries the sentinel in an
// undeclared field. The gather step writes its environment and a whole listing of the
// working directory into a gathered input, so anything that reached either would show up
// there. The sentinel is found in the delivery's own body blob — that is where FR-323
// says it belongs — and nowhere else a run can see; "marker-ok" is found in trigger.json,
// which is what makes the absence mean something rather than everything being empty.
func TestUndeclaredContentReachesNoRun(t *testing.T) {
	dir := t.TempDir()
	store, err := record.Open(t.Context(), filepath.Join(dir, "record"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	capture := &sinkCapture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		capture.add(string(data))
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

	playbooksDir := filepath.Join(dir, "playbooks")
	if err := os.MkdirAll(playbooksDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(playbooksDir, "prompt.md"),
		[]byte("Report on the gathered facts. Nothing here names the payload."), 0o600); err != nil {
		t.Fatal(err)
	}
	document := `
name: alert-triage
trigger:
  type: webhook
  source: alerts
  values:
    marker:
      at: /alert/marker
      pattern: "[a-z-]+"
      max_length: 20
gather:
  - run: env; echo ---; ls -la .
    as: env-and-files.txt
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
	path := filepath.Join(playbooksDir, "book.yaml")
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	book, err := playbook.ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}

	executor := &run.Executor{
		Manager:         run.NewManager(store, filepath.Join(dir, "work"), filepath.Join(dir, "locks")),
		Store:           store,
		Config:          cfg,
		AgentExecutable: fakeagent.Build(t),
		AgentEnv: []string{
			"PATH=/usr/bin:/bin",
			fakeagent.ModeVar + "=" + fakeagent.ModeSuccess,
			fakeagent.ResultVar + `={"findings":[]}`,
			fakeagent.QuoteVar + "=trigger.json,env-and-files.txt",
		},
		StepEnv: []string{"PATH=/usr/bin:/bin"},
		Client:  server.Client(),
	}

	body := []byte(`{"alert":{"marker":"marker-ok"},"secret_field":"` + sentinel + `"}`)
	delivery := seedDelivery(t, store, filepath.Join(dir, "record"), "delivery-1", "alerts", body,
		[]string{book.Name})

	loaded := playbook.Loaded{Playbooks: []*playbook.Playbook{book}}
	dispatcher := &executorDispatcher{executor: executor}
	if err := ingress.HandOff(t.Context(), store, dispatcher, loaded, delivery, time.Now); err != nil {
		t.Fatal(err)
	}

	handoffs, err := store.HandOffsOf(t.Context(), "delivery-1")
	if err != nil || len(handoffs) != 1 || handoffs[0].State != record.HandOffHandedOff {
		t.Fatalf("hand-offs = %+v, err = %v", handoffs, err)
	}

	runs, err := store.ListRuns(t.Context(), 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %+v, err = %v", runs, err)
	}
	recorded := runs[0]
	if recorded.Status != record.StatusSucceeded {
		t.Fatalf("status = %s, error = %s", recorded.Status, recorded.Error)
	}

	// The sentinel is in the delivery's own body blob — that is where it belongs.
	storedBody, err := store.Blobs().Get(delivery.BodyRef)
	if err != nil || !strings.Contains(string(storedBody), sentinel) {
		t.Fatalf("the delivery's own body does not hold the sentinel: %v, %v", string(storedBody), err)
	}

	// Nowhere else: the gathered inputs, the recorded prompt, the stub agent's own
	// report, or what the sink received.
	inputs, err := store.GatheredInputs(t.Context(), recorded.ID)
	if err != nil {
		t.Fatal(err)
	}
	var sawTriggerJSON bool
	for _, input := range inputs {
		if input.BlobRef == "" {
			continue
		}
		data, err := store.Blobs().Get(input.BlobRef)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(sentinel)) {
			t.Fatalf("the sentinel is in gathered input %q: %s", input.Name, data)
		}
		if input.Name == run.TriggerDataFile {
			sawTriggerJSON = true
			if !bytes.Contains(data, []byte("marker-ok")) {
				t.Fatalf("trigger.json does not hold the declared value: %s", data)
			}
		}
	}
	if !sawTriggerJSON {
		t.Fatal("no gathered input named trigger.json; FR-324's data file was not recorded")
	}

	prompt, err := store.Blobs().Get(recorded.PromptRef)
	if err != nil || bytes.Contains(prompt, []byte(sentinel)) {
		t.Fatalf("the recorded prompt holds the sentinel: %s, err = %v", prompt, err)
	}

	report, err := store.Blobs().Get(recorded.ReportRef)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(report, []byte(sentinel)) {
		t.Fatalf("the stub agent's own report holds the sentinel: %s", report)
	}
	var decoded struct {
		Quoted map[string]any `json:"quoted"`
	}
	if err := json.Unmarshal(report, &decoded); err != nil {
		t.Fatal(err)
	}
	triggerJSON, _ := decoded.Quoted["trigger.json"].(string)
	if !strings.Contains(triggerJSON, "marker-ok") {
		t.Fatalf("the agent's quoted trigger.json does not hold the declared value: %v", decoded.Quoted["trigger.json"])
	}
	envAndFiles, _ := decoded.Quoted["env-and-files.txt"].(string)
	if strings.Contains(envAndFiles, sentinel) {
		t.Fatalf("the agent quoted the sentinel out of its environment or a directory listing: %v", envAndFiles)
	}

	for _, delivered := range capture.all() {
		if strings.Contains(delivered, sentinel) {
			t.Fatalf("a sink received the sentinel: %s", delivered)
		}
	}
}
