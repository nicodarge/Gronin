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
	"github.com/nicodarge/Gronin/runtime/internal/guard"
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
%sgather:
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
	stateDir, trace, gate, books, book, webhook string
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
		books: books, book: filepath.Join(books, "book.yaml"), webhook: destination.URL,
	}
	h.write(t, "drift-check", "original")
	return h
}

// document is the playbook declaring name, producing version, with guard its guard block.
func (h *gatedHost) document(name, version, guard string) string {
	return fmt.Sprintf(gatedPlaybook, name, guard, h.trace, h.gate, version, h.webhook)
}

// write lays the playbook file down, declaring name and producing version.
func (h *gatedHost) write(t *testing.T, name, version string) {
	t.Helper()
	h.writeDocument(t, h.book, h.document(name, version, ""))
}

func (h *gatedHost) writeDocument(t *testing.T, path, document string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
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
// ended by a signal rather than asked to stop through anything it could act on: a record
// written on the way out would pass a graceful stop, and that is the implementation FR-127
// refuses. What is asserted is the drop, read through the operator's surface — the absence
// of a run alone is satisfied by a runtime that never started. The specification's scenario
// names both a kill and a graceful stop, so both are sent.
func TestAKilledWaitIsDropped(t *testing.T) {
	t.Setenv(fakeagent.ModeVar, fakeagent.ModeSuccess)
	for _, stop := range []syscall.Signal{syscall.SIGKILL, syscall.SIGTERM} {
		t.Run(stop.String(), func(t *testing.T) { droppedWhenStopped(t, stop) })
	}
}

func droppedWhenStopped(t *testing.T, stop syscall.Signal) {
	h := newGatedHost(t)

	h.hold(t)
	first := h.run(t)
	h.waitForRuns(t, 1)
	running, err := h.store(t).ListRuns(t.Context(), 10)
	if err != nil || len(running) != 1 {
		t.Fatalf("runs = %+v, err = %v, want the one in flight", running, err)
	}

	second := h.run(t)
	// T079: for whom it waits, and for how long at most, in contracts/cli.md's words.
	waitingLine := second.Expect(t, "waiting up to", time.Minute)
	wantHolder := fmt.Sprintf("drift-check is running (run %s on %s, process ", running[0].ID, hostName())
	if !strings.HasPrefix(waitingLine, wantHolder) || !strings.HasSuffix(waitingLine, "); waiting up to 30m") {
		t.Fatalf("the waiting line is %q, want %q…); waiting up to 30m", waitingLine, wantHolder)
	}

	if stop == syscall.SIGKILL {
		// FR-111 and FR-102: a further invocation is refused for the slot, and gathers nothing.
		// The trace is read while the invocation may still be going, before anything that
		// depends on it having printed or ended: a gather step it ran would hold on the gate,
		// and the refusal would then never be printed at all.
		third := h.run(t)
		waitFor(t, time.Minute, func() bool {
			_, err := third.Wait(50 * time.Millisecond)
			return err == nil || h.ran(t) > 1
		}, func() string { return "the invocation refused for the slot neither ended nor gathered" })
		if ran := h.ran(t); ran != 1 {
			t.Fatalf("%d gather step(s) started, want only the first run's", ran)
		}
		third.Expect(t, string(record.MechanismWaitingSlotFull), time.Minute)
		if code, err := third.Wait(time.Minute); err != nil || code == 0 {
			t.Fatalf("the invocation refused for the slot exited %d, err %v", code, err)
		}
	}

	// A trigger still waiting is not dropped, whoever reads the record.
	if refused := h.refusals(t); strings.Contains(refused, string(record.MechanismDropped)) {
		t.Fatalf("a live waiting trigger was read as dropped:\n%s", refused)
	}

	if err := second.Signal(stop); err != nil {
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
		t.Fatalf("gronin refusals does not show the stopped wait as dropped:\n%s", refused)
	}
	recorded, err := h.store(t).ListRefusals(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	var drop record.Refusal
	for _, refusal := range recorded {
		if refusal.Mechanism == record.MechanismDropped {
			drop = refusal
		}
	}
	trigger, err := h.store(t).GetWaitingTrigger(t.Context(), drop.WaitingTriggerID)
	if err != nil {
		t.Fatalf("the drop names no waiting trigger that was recorded: %v", err)
	}
	// SC-113: the drop names the trigger and when it arrived, in full — it is read after the
	// fact, possibly days later.
	for _, named := range []string{
		"drift-check", string(record.TriggerManual),
		fmt.Sprintf("trigger %s accepted at %s; process %s ended before it ran",
			trigger.ID, trigger.AcceptedAt.UTC().Format(time.RFC3339), trigger.Instance),
	} {
		if !strings.Contains(dropped, named) {
			t.Fatalf("the drop does not say %q: %q", named, dropped)
		}
	}
	lockFile := filepath.Join(h.stateDir, guard.InstancesDir, trigger.Instance+".lock")
	if _, err := os.Stat(lockFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the stopped process's instance lock file was left behind: %v", err)
	}

	h.release(t)
	if code, err := first.Wait(2 * time.Minute); err != nil || code != 0 {
		t.Fatalf("the first run exited %d, err %v: %s", code, err, first.Stderr())
	}
	if stop != syscall.SIGKILL {
		return
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
	busy := h.run(t)
	h.waitForRuns(t, 2)
	later := h.run(t)
	later.Expect(t, "waiting up to", time.Minute)
	h.release(t)
	if code, err := busy.Wait(2 * time.Minute); err != nil || code != 0 {
		t.Fatalf("the run it waited behind exited %d, err %v: %s", code, err, busy.Stderr())
	}
	finished := later.Expect(t, "(waited ", 2*time.Minute)
	waited := strings.TrimSuffix(finished[strings.Index(finished, "(waited ")+len("(waited "):], ")")
	if parsed, err := time.ParseDuration(waited); err != nil || guard.HumanDuration(parsed) != waited {
		t.Fatalf("the time waited is not written in whole seconds as the contract writes it: %q", finished)
	}
	if code, err := later.Wait(time.Minute); err != nil || code != 0 {
		t.Fatalf("the waiting invocation exited %d, err %v: %s", code, err, later.Stderr())
	}
	h.waitForRuns(t, 3)
}

// FR-112 in the shipped binary, whose clock is the system's rather than one a test injects:
// a trigger whose declared wait passes while the run ahead of it holds the claim ends as
// wait_expired, and runs nothing.
func TestAWaitExpiresOnTheSystemClock(t *testing.T) {
	t.Setenv(fakeagent.ModeVar, fakeagent.ModeSuccess)
	h := newGatedHost(t)
	h.writeDocument(t, h.book, h.document("drift-check", "original", "guard:\n  wait: 1s\n"))

	h.hold(t)
	first := h.run(t)
	h.waitForRuns(t, 1)
	second := h.run(t)
	second.Expect(t, "waiting up to 1s", time.Minute)
	second.Expect(t, string(record.MechanismWaitExpired), time.Minute)
	if code, err := second.Wait(time.Minute); err != nil || code == 0 {
		t.Fatalf("a trigger whose wait expired exited %d, err %v", code, err)
	}
	if refused := h.refusals(t); !strings.Contains(refused, string(record.MechanismWaitExpired)) {
		t.Fatalf("gronin refusals does not show the expired wait:\n%s", refused)
	}

	h.release(t)
	if code, err := first.Wait(2 * time.Minute); err != nil || code != 0 {
		t.Fatalf("the first run exited %d, err %v: %s", code, err, first.Stderr())
	}
	if ran := h.ran(t); ran != 1 {
		t.Fatalf("%d runs, want only the first: the expired trigger ran", ran)
	}
}
