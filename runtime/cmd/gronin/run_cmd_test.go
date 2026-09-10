package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/bintest"
	"github.com/nicodarge/Gronin/runtime/internal/fakeagent"
)

// A deployment on disk, driven through the built executable — the operator's own surface
// rather than the packages behind it.
func deploymentOnDisk(t *testing.T, playbookBody string) (stateDir, playbooksDir string) {
	t.Helper()
	stateDir = t.TempDir()
	playbooksDir = filepath.Join(stateDir, "playbooks")
	if err := os.MkdirAll(playbooksDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(playbooksDir, "book.yaml"),
		[]byte(playbookBody), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(playbooksDir, "prompt.md"),
		[]byte("report on the gathered facts"), 0o600); err != nil {
		t.Fatal(err)
	}
	return stateDir, playbooksDir
}

const cliPlaybook = `
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
sinks:
  - discord:
      webhook: http://127.0.0.1:9/unreachable
`

func TestRunInvokesAPlaybookByName(t *testing.T) {
	stateDir, _ := deploymentOnDisk(t, cliPlaybook)
	t.Setenv(fakeagent.ModeVar, fakeagent.ModeSuccess)

	got := bintest.Run(t, "run", "drift-check",
		"--state-dir", stateDir, "--agent", fakeagent.Build(t))

	// The sink is a closed port, so the run is recorded failed — but it ran, and that is
	// what this asserts: the identifier and the status reach the operator.
	if !strings.Contains(got.Stdout, "drift-check") && !strings.Contains(got.Stdout, "Z-") {
		t.Fatalf("stdout = %q, stderr = %q", got.Stdout, got.Stderr)
	}
	if got.ExitCode == 0 {
		t.Fatalf("a run whose sink could not be reached exited 0: %q", got.Stdout)
	}
}

func TestRunRefusesAPlaybookThatIsNotThere(t *testing.T) {
	stateDir, _ := deploymentOnDisk(t, cliPlaybook)

	got := bintest.Run(t, "run", "no-such-playbook",
		"--state-dir", stateDir, "--agent", fakeagent.Build(t))

	if got.ExitCode == 0 {
		t.Fatal("exit code 0 for a playbook that does not exist")
	}
	if !strings.Contains(got.Stderr, "no-such-playbook") {
		t.Fatalf("the refusal does not name it: %q", got.Stderr)
	}
	// It says what there is, so the operator does not have to guess.
	if !strings.Contains(got.Stderr, "drift-check") {
		t.Fatalf("the refusal does not list what is loaded: %q", got.Stderr)
	}
}

// The load gate's headline: a refusal anywhere means nothing is armed, and the output
// says so rather than leaving the operator to infer it.
func TestOneRefusedPlaybookStopsEverything(t *testing.T) {
	stateDir, playbooksDir := deploymentOnDisk(t, cliPlaybook)
	if err := os.WriteFile(filepath.Join(playbooksDir, "broken.yaml"),
		[]byte("name: [unclosed\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := bintest.Run(t, "run", "drift-check",
		"--state-dir", stateDir, "--agent", fakeagent.Build(t))

	if got.ExitCode == 0 {
		t.Fatal("a valid playbook ran while another was refused")
	}
	if !strings.Contains(got.Stderr, "Nothing was armed") {
		t.Fatalf("the output does not say nothing was armed: %q", got.Stderr)
	}
	if !strings.Contains(got.Stderr, "broken.yaml") {
		t.Fatalf("the refusal does not name the file: %q", got.Stderr)
	}
}

func TestServeRefusesToArmWhenAPlaybookIsRefused(t *testing.T) {
	stateDir, playbooksDir := deploymentOnDisk(t, cliPlaybook)
	if err := os.WriteFile(filepath.Join(playbooksDir, "broken.yaml"),
		[]byte("name: [unclosed\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := bintest.Run(t, "serve", "--state-dir", stateDir, "--agent", fakeagent.Build(t))

	if got.ExitCode == 0 {
		t.Fatal("serve armed a deployment holding a refused playbook")
	}
	if strings.Contains(got.Stdout, "armed") {
		t.Fatalf("it reported arming something: %q", got.Stdout)
	}
}

// FR-040. A playbook holds the reference and the deployment holds the value, which is
// what lets a playbook be committed and shared at all.
func TestConfigSetsAValueAndRedactsASecretWhenListing(t *testing.T) {
	stateDir := t.TempDir()

	if got := bintest.RunWithStdin(t, "#ops", "config", "set", "ops_channel",
		"--state-dir", stateDir); got.ExitCode != 0 {
		t.Fatalf("set failed: %q %q", got.Stdout, got.Stderr)
	}
	if got := bintest.RunWithStdin(t, "https://example.com/hook/t0ken",
		"config", "set", "ops_webhook",
		"--secret", "--state-dir", stateDir); got.ExitCode != 0 {
		t.Fatalf("set --secret failed: %q %q", got.Stdout, got.Stderr)
	}

	got := bintest.Run(t, "config", "list", "--state-dir", stateDir)
	if got.ExitCode != 0 {
		t.Fatalf("list failed: %q", got.Stderr)
	}
	if !strings.Contains(got.Stdout, "#ops") {
		t.Fatalf("a value that is not a secret was not shown: %q", got.Stdout)
	}
	if strings.Contains(got.Stdout, "t0ken") {
		t.Fatalf("a secret was printed: %q", got.Stdout)
	}
	if !strings.Contains(got.Stdout, "[redacted]") {
		t.Fatalf("the secret is not shown as redacted: %q", got.Stdout)
	}
}

// Setting a secret must not echo it: this runs in a shell whose history keeps what it
// is told, and the value has just been marked as one worth hiding.
func TestSettingASecretDoesNotEchoIt(t *testing.T) {
	stateDir := t.TempDir()

	got := bintest.RunWithStdin(t, "https://example.com/hook/t0ken",
		"config", "set", "ops_webhook", "--secret", "--state-dir", stateDir)

	if strings.Contains(got.Stdout, "t0ken") || strings.Contains(got.Stderr, "t0ken") {
		t.Fatalf("the value was echoed: %q %q", got.Stdout, got.Stderr)
	}
	if !strings.Contains(got.Stdout, "ops_webhook") {
		t.Fatalf("it does not say what was set: %q", got.Stdout)
	}
}

// The constitution's Secrets constraint: a secret MUST NOT appear on a command line,
// because commands are journalled and shipped to log aggregation, where the value then
// sits for the whole retention window.
func TestConfigSetRefusesAValueOnTheCommandLine(t *testing.T) {
	stateDir := t.TempDir()

	got := bintest.Run(t, "config", "set", "ops_webhook", "https://example.com/hook/t0ken",
		"--state-dir", stateDir)

	if got.ExitCode == 0 {
		t.Fatalf("a value given as a second argument was accepted: %q", got.Stdout)
	}
	if strings.Contains(got.Stdout, "t0ken") || strings.Contains(got.Stderr, "t0ken") {
		t.Fatalf("the refused value was echoed back: %q %q", got.Stdout, got.Stderr)
	}
	if !strings.Contains(got.Stderr, "standard input") && !strings.Contains(got.Stderr, "config set") {
		t.Fatalf("the refusal does not point at the stdin form: %q", got.Stderr)
	}

	data, err := os.ReadFile(filepath.Join(stateDir, "config.json"))
	if err == nil {
		t.Fatalf("the refused value was stored anyway: %q", data)
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

// The value round-trips through standard input, and exactly one trailing newline —
// the one a shell redirection or a heredoc leaves behind — is stripped, not every one.
func TestConfigSetReadsTheValueFromStandardInput(t *testing.T) {
	stateDir := t.TempDir()

	got := bintest.RunWithStdin(t, "line one\nline two\n\n",
		"config", "set", "multiline", "--state-dir", stateDir)
	if got.ExitCode != 0 {
		t.Fatalf("set failed: %q %q", got.Stdout, got.Stderr)
	}

	stored := readStoredConfig(t, stateDir)
	if got, want := stored["multiline"].Value, "line one\nline two\n"; got != want {
		t.Fatalf("stored value = %q, want %q", got, want)
	}
}

// FR-040's sink/build.go depends on an explicitly empty value being distinct from one
// never configured at all — `printf ” | gronin config set key` must still declare the
// key, just with nothing in it.
func TestConfigSetAcceptsAnExplicitlyEmptyValue(t *testing.T) {
	stateDir := t.TempDir()

	got := bintest.RunWithStdin(t, "", "config", "set", "empty_key", "--state-dir", stateDir)
	if got.ExitCode != 0 {
		t.Fatalf("set failed: %q %q", got.Stdout, got.Stderr)
	}

	stored := readStoredConfig(t, stateDir)
	value, present := stored["empty_key"]
	if !present {
		t.Fatal("declared-and-empty was not stored; it must be distinct from never configured")
	}
	if value.Value != "" {
		t.Fatalf("value = %q, want empty", value.Value)
	}
}

func readStoredConfig(t *testing.T, stateDir string) map[string]struct {
	Value  string `json:"value"`
	Secret bool   `json:"secret"`
} {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(stateDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]struct {
		Value  string `json:"value"`
		Secret bool   `json:"secret"`
	}
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	return stored
}

// FR-021 through the operator's own surface: run it, list it, read it back, and act on
// the record without stopping anything.
func TestARunCanBeListedAndReadBack(t *testing.T) {
	stateDir, _ := deploymentOnDisk(t, cliPlaybook)
	t.Setenv(fakeagent.ModeVar, fakeagent.ModeSuccess)
	agentPath := fakeagent.Build(t)

	invoked := bintest.Run(t, "run", "drift-check", "--state-dir", stateDir, "--agent", agentPath)
	if !strings.Contains(invoked.Stdout, "Z-") {
		t.Fatalf("the run printed no identifier: %q %q", invoked.Stdout, invoked.Stderr)
	}
	id := strings.Fields(invoked.Stdout)[0]

	listed := bintest.Run(t, "runs", "--state-dir", stateDir, "--agent", agentPath)
	if listed.ExitCode != 0 || !strings.Contains(listed.Stdout, id) {
		t.Fatalf("the run is not in the list: %q", listed.Stdout)
	}
	if !strings.Contains(listed.Stdout, "drift-check") {
		t.Fatalf("the list does not name the playbook: %q", listed.Stdout)
	}

	shown := bintest.Run(t, "show", id, "--state-dir", stateDir, "--agent", agentPath)
	if shown.ExitCode != 0 {
		t.Fatalf("show failed: %q", shown.Stderr)
	}
	// SC-003: what it was asked, what each sink did, and what its bounds refused.
	for _, want := range []string{"playbook", "status", "gathered", "sink", "refused", "prompt"} {
		if !strings.Contains(shown.Stdout, want) {
			t.Errorf("the record does not show %q: %q", want, shown.Stdout)
		}
	}

	unknown := bintest.Run(t, "show", "no-such-run", "--state-dir", stateDir, "--agent", agentPath)
	if unknown.ExitCode == 0 {
		t.Fatal("show exited 0 for a run that does not exist")
	}
}
