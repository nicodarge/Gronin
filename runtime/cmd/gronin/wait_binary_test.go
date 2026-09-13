package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/bintest"
	"github.com/nicodarge/Gronin/runtime/internal/fakeagent"
	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// gatedPlaybook appends a line when its gather step starts, then holds while the gate file
// exists, so a run is in flight for exactly as long as the test keeps it so. The gathered
// input names the version of the file that ran, which is how an edit is seen in the run's
// own output rather than in a field nothing reads.
const gatedPlaybook = `
name: %s
trigger:
  type: manual
gather:
  - run: printf 'ran\n' >> %s; while [ -e %s ]; do sleep 0.1; done; echo '{"version":"%s"}'
    as: facts.json
agent:
  model: claude-sonnet-5
  prompt_file: prompt.md
  tools: [Read]
  output_schema:
    type: object
sinks:
  - discord:
      webhook: %s
`

// gatedHost is one single-host deployment of a gated playbook, and a destination its sink
// delivers to so that its runs succeed.
type gatedHost struct {
	stateDir, trace, gate, book, webhook string
}

func newGatedHost(t *testing.T) *gatedHost {
	t.Helper()
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(destination.Close)

	stateDir := t.TempDir()
	books := filepath.Join(stateDir, "playbooks")
	if err := os.MkdirAll(books, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(books, "prompt.md"), []byte("report"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &gatedHost{
		stateDir: stateDir, trace: filepath.Join(stateDir, "trace"), gate: filepath.Join(stateDir, "gate"),
		book: filepath.Join(books, "book.yaml"), webhook: destination.URL,
	}
	h.write(t, "drift-check", "original")
	return h
}

// write lays the playbook file down, declaring name and producing version.
func (h *gatedHost) write(t *testing.T, name, version string) {
	t.Helper()
	document := fmt.Sprintf(gatedPlaybook, name, h.trace, h.gate, version, h.webhook)
	if err := os.WriteFile(h.book, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (h *gatedHost) hold(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(h.gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (h *gatedHost) release(t *testing.T) {
	t.Helper()
	if err := os.Remove(h.gate); err != nil {
		t.Fatal(err)
	}
}

// ran is how many runs have reached their gather step.
func (h *gatedHost) ran(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile(h.trace)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "ran\n")
}

func (h *gatedHost) waitForRuns(t *testing.T, want int) {
	t.Helper()
	waitFor(t, time.Minute, func() bool { return h.ran(t) >= want }, func() string {
		return fmt.Sprintf("%d run(s) reached their gather step, want %d", h.ran(t), want)
	})
	if ran := h.ran(t); ran != want {
		t.Fatalf("%d run(s) reached their gather step, want %d", ran, want)
	}
}

func (h *gatedHost) run(t *testing.T) *bintest.Process {
	t.Helper()
	return bintest.Start(t, "run", "drift-check", "--state-dir", h.stateDir, "--agent", fakeagent.Build(t))
}

func (h *gatedHost) refusals(t *testing.T) string {
	t.Helper()
	got := bintest.Run(t, "refusals", "--state-dir", h.stateDir)
	if got.ExitCode != 0 {
		t.Fatalf("gronin refusals exited %d: %q", got.ExitCode, got.Stderr)
	}
	return got.Stdout
}

func (h *gatedHost) store(t *testing.T) *record.Store {
	t.Helper()
	store, err := record.Open(t.Context(), filepath.Join(h.stateDir, "record"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// SC-113 and SC-106's restart half, FR-113 and FR-127, single-host. The waiting process is
// killed with SIGKILL rather than stopped: a record written on the way out would pass a
// graceful stop, and that is the implementation FR-127 refuses. What is asserted is the
// drop, read through the operator's surface — the absence of a run alone is satisfied by a
// runtime that never started.
func TestAKilledWaitIsDropped(t *testing.T) {
	t.Setenv(fakeagent.ModeVar, fakeagent.ModeSuccess)
	h := newGatedHost(t)

	h.hold(t)
	first := h.run(t)
	h.waitForRuns(t, 1)

	acceptedAfter := time.Now().UTC().Truncate(time.Second)
	second := h.run(t)
	second.Expect(t, "waiting up to", time.Minute)

	// A trigger still waiting is not dropped, whoever reads the record.
	if refused := h.refusals(t); strings.Contains(refused, string(record.MechanismDropped)) {
		t.Fatalf("a live waiting trigger was read as dropped:\n%s", refused)
	}

	if err := second.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Wait(30 * time.Second); err != nil {
		t.Fatal(err)
	}

	refused := h.refusals(t)
	var dropped string
	for _, line := range strings.Split(refused, "\n") {
		if strings.Contains(line, string(record.MechanismDropped)) {
			dropped = line
		}
	}
	if dropped == "" {
		t.Fatalf("gronin refusals does not show the killed wait as dropped:\n%s", refused)
	}
	recorded, err := h.store(t).ListRefusals(t.Context(), 10)
	if err != nil || len(recorded) != 1 || recorded[0].WaitingTriggerID == "" {
		t.Fatalf("refusals = %+v, err = %v, want the drop naming its waiting trigger", recorded, err)
	}
	trigger, err := h.store(t).GetWaitingTrigger(t.Context(), recorded[0].WaitingTriggerID)
	if err != nil {
		t.Fatal(err)
	}
	if trigger.AcceptedAt.Before(acceptedAfter) {
		t.Fatalf("the trigger reads as accepted at %s, before it was invoked", trigger.AcceptedAt)
	}
	for _, named := range []string{
		"drift-check", string(record.TriggerManual), trigger.ID, trigger.AcceptedAt.Format(time.RFC3339),
	} {
		if !strings.Contains(dropped, named) {
			t.Fatalf("the drop does not name %q: %q", named, dropped)
		}
	}

	h.release(t)
	if code, err := first.Wait(2 * time.Minute); err != nil || code != 0 {
		t.Fatalf("the first run exited %d, err %v: %s", code, err, first.Stderr())
	}

	// The runtime starts again: nothing runs from the trigger that was dropped.
	serving := bintest.Start(t, "serve", "--state-dir", h.stateDir,
		"--agent", fakeagent.Build(t), "--api-address", "127.0.0.1:0")
	serving.Expect(t, "API on", time.Minute)
	if err := serving.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if _, err := serving.Wait(30 * time.Second); err != nil {
		t.Fatal(err)
	}
	if ran := h.ran(t); ran != 1 {
		t.Fatalf("%d runs, want only the first: the dropped trigger ran", ran)
	}

	// And the slot is free: a further invocation during a run waits, rather than being
	// refused behind a trigger whose process is gone.
	h.hold(t)
	third := h.run(t)
	h.waitForRuns(t, 2)
	fourth := h.run(t)
	fourth.Expect(t, "waiting up to", time.Minute)
	h.release(t)
	if code, err := third.Wait(2 * time.Minute); err != nil || code != 0 {
		t.Fatalf("the third run exited %d, err %v: %s", code, err, third.Stderr())
	}
	fourth.Expect(t, "(waited", 2*time.Minute)
	if code, err := fourth.Wait(time.Minute); err != nil || code != 0 {
		t.Fatalf("the waiting invocation exited %d, err %v: %s", code, err, fourth.Stderr())
	}
	h.waitForRuns(t, 3)
}
