package fakeagent_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/fakeagent"
)

func TestMain(m *testing.M) {
	os.Exit(runAndCleanUp(m))
}

// runAndCleanUp: see bintest.Main's doc comment for why this defers Cleanup around
// m.Run() in a helper rather than placing it after m.Run() in TestMain itself.
func runAndCleanUp(m *testing.M) (code int) {
	defer fakeagent.Cleanup()
	return m.Run()
}

type event map[string]any

func runStub(t *testing.T, mode string, extraEnv []string, args ...string) ([]event, string, int) {
	t.Helper()
	return runStubIn(t, fakeagent.Build(t), "", mode, extraEnv, args...)
}

// runStubIn runs an executable standing in for the agent — the stub, or a script around
// it — from a working directory of the caller's choosing, which is what the stub reads
// the files it quotes from.
func runStubIn(
	t *testing.T, executable, dir, mode string, extraEnv []string, args ...string,
) ([]event, string, int) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), executable, args...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), fakeagent.ModeVar+"="+mode), extraEnv...)
	output, err := cmd.Output()

	code := 0
	var exit *exec.ExitError
	if err != nil {
		if !asExitError(err, &exit) {
			t.Fatalf("running the stub: %v", err)
		}
		code = exit.ExitCode()
	}

	var events []event
	var leftovers []string
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == "" {
			continue
		}
		var decoded event
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			leftovers = append(leftovers, line)
			continue
		}
		events = append(events, decoded)
	}
	return events, strings.Join(leftovers, "\n"), code
}

func asExitError(err error, target **exec.ExitError) bool {
	exit, ok := err.(*exec.ExitError) //nolint:errorlint // the stub returns this directly
	if ok {
		*target = exit
	}
	return ok
}

func TestTheReceiptReportsWhatTheProcessWasGiven(t *testing.T) {
	events, _, code := runStub(t, fakeagent.ModeSuccess, nil, "--tools", "Read,Grep", "--model", "m")
	if code != 0 {
		t.Fatalf("exit code %d", code)
	}
	if len(events) < 2 {
		t.Fatalf("only %d events", len(events))
	}

	init := events[0]
	if init["type"] != "system" || init["subtype"] != "init" {
		t.Fatalf("the first event is %v", init)
	}
	tools, _ := init["tools"].([]any)
	if len(tools) != 2 || tools[0] != "Read" || tools[1] != "Grep" {
		t.Fatalf("the receipt reports %v, and it was given Read,Grep", init["tools"])
	}

	last := events[len(events)-1]
	if last["type"] != "result" || last["subtype"] != "success" {
		t.Fatalf("the last event is %v", last)
	}
	for _, field := range []string{"total_cost_usd", "usage", "num_turns", "stop_reason", "permission_denials"} {
		if _, ok := last[field]; !ok {
			t.Errorf("the result carries no %s, and the record needs it", field)
		}
	}
}

// The mode that makes the receipt check testable: a wider tool set than it was handed.
func TestTheMismatchModeReportsAWiderToolSet(t *testing.T) {
	events, _, _ := runStub(t, fakeagent.ModeMismatch, nil, "--tools", "Read")

	tools, _ := events[0]["tools"].([]any)
	var names []string
	for _, tool := range tools {
		names = append(names, tool.(string))
	}
	if len(names) <= 1 {
		t.Fatalf("the receipt reports %v, which is not wider than what it was given", names)
	}
	if !contains(names, "Bash") {
		t.Fatalf("the receipt reports %v; the widening should be unmistakable", names)
	}
}

func TestTheEmptyToolSetIsReportedAsEmptyRatherThanAbsent(t *testing.T) {
	events, _, _ := runStub(t, fakeagent.ModeSuccess, nil, "--tools", "")

	tools, ok := events[0]["tools"].([]any)
	if !ok {
		t.Fatalf("the receipt has no tools field: %v", events[0])
	}
	if len(tools) != 0 {
		t.Fatalf("tools = %v, want empty", tools)
	}
}

func TestTheMalformedModeEmitsSomethingThatIsNotJSON(t *testing.T) {
	events, leftovers, _ := runStub(t, fakeagent.ModeMalformed, nil, "--tools", "Read")

	if leftovers == "" {
		t.Fatal("nothing unparseable was emitted")
	}
	if len(events) < 2 {
		t.Fatal("the stub stopped at the malformed line; the decoder is meant to survive it")
	}
}

func TestTheErrorModeFailsAndSaysSo(t *testing.T) {
	events, _, code := runStub(t, fakeagent.ModeExitError, nil, "--tools", "Read")

	if code == 0 {
		t.Fatal("exit code 0")
	}
	last := events[len(events)-1]
	if last["is_error"] != true || last["subtype"] != "error" {
		t.Fatalf("the result does not report the failure: %v", last)
	}
}

func TestTheTimeoutModeDoesNotTerminate(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), fakeagent.Build(t), "--tools", "Read")
	cmd.Env = append(os.Environ(), fakeagent.ModeVar+"="+fakeagent.ModeTimeout)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		t.Fatalf("the stub terminated on its own: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestTheResultCanBeHandedOver(t *testing.T) {
	events, _, _ := runStub(t, fakeagent.ModeSuccess,
		[]string{fakeagent.ResultVar + `={"findings":[{"id":"one"}]}`}, "--tools", "Read")

	last := events[len(events)-1]
	report, ok := last["result"].(map[string]any)
	if !ok {
		t.Fatalf("result = %v", last["result"])
	}
	findings, _ := report["findings"].([]any)
	if len(findings) != 1 {
		t.Fatalf("the handed-over report did not come back: %v", report)
	}
}

func TestTheReceiptNamesTheConfiguredServersAndNothingElse(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(config, []byte(`{"mcpServers":{"grafana":{}}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	events, _, _ := runStub(t, fakeagent.ModeSuccess, nil,
		"--tools", "Read", "--mcp-config", config, "--strict-mcp-config")

	servers, _ := events[0]["mcp_servers"].([]any)
	if len(servers) != 1 {
		t.Fatalf("mcp_servers = %v", servers)
	}
	named := servers[0].(map[string]any)
	if named["name"] != "grafana" {
		t.Fatalf("the receipt names %v", named)
	}

	// A playbook declaring no servers still gets a strict configuration, and the receipt
	// says empty rather than absent.
	events, _, _ = runStub(t, fakeagent.ModeSuccess, nil, "--tools", "Read")
	if servers, ok := events[0]["mcp_servers"].([]any); !ok || len(servers) != 0 {
		t.Fatalf("mcp_servers = %v, want empty", events[0]["mcp_servers"])
	}
}

func TestTheVersionIsReadable(t *testing.T) {
	out, err := exec.CommandContext(t.Context(), fakeagent.Build(t), "--version").Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), ".") {
		t.Fatalf("version output = %q", out)
	}
}

// SC-201 needs the agent to show it read the passage: a results file that exists after a
// run passes against a stage that wrote it once the agent had gone. What this pins is the
// quoting the effect is counted by — verbatim for a file that is there, null for one that
// is not, and the handed-over report kept beside it.
func TestTheStubQuotesWhatItFound(t *testing.T) {
	dir := t.TempDir()
	const body = "# Retrieved from runbooks\n\nWhen /var fills, read the journal's size.\n"
	if err := os.WriteFile(filepath.Join(dir, "runbooks.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	events, _, code := runStubIn(t, fakeagent.Build(t), dir, fakeagent.ModeSuccess,
		[]string{
			fakeagent.QuoteVar + "=runbooks.md,absent.md",
			fakeagent.ResultVar + `={"findings":[{"id":"one"}]}`,
		}, "--tools", "Read")
	if code != 0 {
		t.Fatalf("exit code %d", code)
	}

	report := reportOf(t, events)
	quoted, ok := report["quoted"].(map[string]any)
	if !ok {
		t.Fatalf("the report carries no quoted files: %v", report)
	}
	if quoted["runbooks.md"] != body {
		t.Fatalf("runbooks.md was quoted as %q, want %q", quoted["runbooks.md"], body)
	}
	if value, named := quoted["absent.md"]; !named || value != nil {
		t.Fatalf("absent.md is %v (named: %v), and a file that is not there is null",
			value, named)
	}
	findings, _ := report["findings"].([]any)
	if len(findings) != 1 {
		t.Fatalf("the handed-over report was lost: %v", report)
	}
}

// The stub is the agent child of the built executable in a binary test, and that child
// inherits a fixed list of variables — none of them a FAKECLAUDE_ name. The wrapper is
// how a test reaches it.
func TestTheWrappedStubCarriesItsVariables(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "runbooks.md"), []byte("quoted"), 0o600); err != nil {
		t.Fatal(err)
	}
	wrapper := fakeagent.Wrapped(t, fakeagent.QuoteVar+"=runbooks.md")

	// No FAKECLAUDE_QUOTE in the environment handed over: the wrapper is the only source.
	events, _, code := runStubIn(t, wrapper, dir, fakeagent.ModeSuccess, nil, "--tools", "Read")
	if code != 0 {
		t.Fatalf("exit code %d", code)
	}
	quoted, ok := reportOf(t, events)["quoted"].(map[string]any)
	if !ok || quoted["runbooks.md"] != "quoted" {
		t.Fatalf("the wrapper's variable did not reach the stub: %v", quoted)
	}
}

func reportOf(t *testing.T, events []event) map[string]any {
	t.Helper()
	if len(events) == 0 {
		t.Fatal("the stub emitted nothing")
	}
	last := events[len(events)-1]
	report, ok := last["result"].(map[string]any)
	if !ok {
		t.Fatalf("the result is not a report: %v", last["result"])
	}
	return report
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
