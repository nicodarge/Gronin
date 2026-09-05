package main

import (
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
