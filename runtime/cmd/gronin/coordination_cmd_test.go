package main

import (
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

// tracedPlaybook leaves a mark outside the run's working directory, so a refusal that
// happened before gather is distinguishable from a run that was tidied up after.
const tracedPlaybook = `
name: drift-check
trigger:
  type: manual
gather:
  - run: printf 'ran\n' >> %s; echo '{"drift":0}'
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

// oneHost lays out a deployment whose playbook leaves a trace, and returns its state
// directory and that trace's path.
func oneHost(t *testing.T, webhook string) (stateDir, trace string) {
	t.Helper()
	stateDir = t.TempDir()
	trace = filepath.Join(stateDir, "trace")
	books := filepath.Join(stateDir, "playbooks")
	if err := os.MkdirAll(books, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(books, "book.yaml"),
		[]byte(fmt.Sprintf(tracedPlaybook, trace, webhook)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(books, "prompt.md"), []byte("report"), 0o600); err != nil {
		t.Fatal(err)
	}
	return stateDir, trace
}

// SC-104's first half, FR-107: a backend that is configured and cannot be reached refuses
// every trigger naming it, rather than falling back to the single-host mechanism.
func TestAnUnreachableBackendRefuses(t *testing.T) {
	t.Setenv(fakeagent.ModeVar, fakeagent.ModeSuccess)
	stateDir, trace := oneHost(t, "http://127.0.0.1:9/unreachable")
	nothing := "unix://" + filepath.Join(t.TempDir(), "nothing.sock")
	writeCoordinationFile(t, stateDir, map[string]any{
		"etcd":           map[string]any{"endpoints": []string{nothing}, "prefix": "gronin/"},
		"decision_bound": "1s",
	})

	started := time.Now()
	got := bintest.Run(t, "run", "drift-check", "--state-dir", stateDir, "--agent", fakeagent.Build(t))
	if took := time.Since(started); took > 10*time.Second {
		t.Fatalf("the decision took %s against a backend that is not there", took)
	}
	if got.ExitCode == 0 {
		t.Fatalf("a run against an unreachable backend succeeded: %q", got.Stdout)
	}
	if _, err := os.Stat(trace); !os.IsNotExist(err) {
		t.Fatalf("the gather step ran for a refused trigger: %v", err)
	}

	refusals := bintest.Run(t, "refusals", "--state-dir", stateDir)
	if !strings.Contains(refusals.Stdout, string(record.MechanismBackendUnavailable)) {
		t.Fatalf("no backend_unavailable refusal was recorded:\n%s", refusals.Stdout)
	}
	if !strings.Contains(refusals.Stdout, nothing) {
		t.Fatalf("the refusal does not name the backend %s:\n%s", nothing, refusals.Stdout)
	}

	// `serve` says the backend is not reachable and stays up: it may be back before the
	// first trigger.
	serving := bintest.Start(t, "serve", "--state-dir", stateDir,
		"--agent", fakeagent.Build(t), "--api-address", "127.0.0.1:0")
	serving.Expect(t, "NOT reachable at startup", 60*time.Second)
	serving.Expect(t, "API on", 60*time.Second)
	if code, err := serving.Wait(2 * time.Second); err == nil {
		t.Fatalf("serve exited %d rather than staying up with the backend unreachable", code)
	}
}

// SC-104's second half, FR-109: a deployment with no backend says which guarantee it has,
// and its runs record it.
func TestTheReachIsStated(t *testing.T) {
	t.Setenv(fakeagent.ModeVar, fakeagent.ModeSuccess)
	delivered := make(chan struct{}, 4)
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		delivered <- struct{}{}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(destination.Close)

	stateDir, trace := oneHost(t, destination.URL)

	serving := bintest.Start(t, "serve", "--state-dir", stateDir,
		"--agent", fakeagent.Build(t), "--api-address", "127.0.0.1:0")
	// The reach is the first thing said about the guard, before anything is armed.
	guardLine := serving.Expect(t, "guard:", 60*time.Second)
	if !strings.Contains(guardLine, "single-host") {
		t.Fatalf("serve did not state the single-host reach: %q", guardLine)
	}
	if err := serving.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if _, err := serving.Wait(30 * time.Second); err != nil {
		t.Fatal(err)
	}

	got := bintest.Run(t, "run", "drift-check", "--state-dir", stateDir, "--agent", fakeagent.Build(t))
	if got.ExitCode != 0 {
		t.Fatalf("a single-host run exited %d: %q / %q", got.ExitCode, got.Stdout, got.Stderr)
	}
	if _, err := os.Stat(trace); err != nil {
		t.Fatalf("the run left no trace: %v", err)
	}
	select {
	case <-delivered:
	default:
		t.Fatal("the sink delivered nothing, so the run did not get that far")
	}

	runID := strings.Fields(got.Stdout)[0]
	shown := bintest.Run(t, "show", runID, "--state-dir", stateDir)
	if !strings.Contains(shown.Stdout, "single-host") {
		t.Fatalf("gronin show does not say which guarantee the run ran under:\n%s", shown.Stdout)
	}
}
