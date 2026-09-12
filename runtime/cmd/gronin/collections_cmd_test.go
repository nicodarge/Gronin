package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/bintest"
)

// deploymentWithACollection writes a state directory holding a catalogue and a playbooks
// directory holding one playbook, and returns both paths.
func deploymentWithACollection(t *testing.T, catalogue, book string) (state, playbooks string) {
	t.Helper()
	state = t.TempDir()
	if catalogue != "" {
		if err := os.WriteFile(filepath.Join(state, "collections.json"),
			[]byte(catalogue), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	playbooks = filepath.Join(state, "playbooks")
	if err := os.MkdirAll(playbooks, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(playbooks, "prompt.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if book != "" {
		if err := os.WriteFile(filepath.Join(playbooks, "disk-check.yaml"),
			[]byte(book), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return state, playbooks
}

// A playbook retrieving from a collection its deployment does not declare fails at the
// first trigger and nowhere earlier, unless the gate reads the catalogue. SC-203, through
// the executable, where the catalogue is read once per invocation and handed to the gate.
func TestValidateRefusesAnUndeclaredCollection(t *testing.T) {
	const book = `name: disk-check
trigger: {type: manual}
retrieve:
  - collection: incidents
    as: incidents.md
    query: disk full on /var
agent: {model: m, prompt_file: prompt.md, output_schema: {type: object}}
sinks: [{discord: {channel: "#ops"}}]
`
	state, playbooks := deploymentWithACollection(t,
		`{"runbooks": {"directory": "/srv/runbooks"}}`, book)

	got := bintest.Run(t, "--state-dir", state, "validate", playbooks)

	if got.ExitCode == 0 {
		t.Fatalf("a retrieval from an undeclared collection was accepted; stdout = %q", got.Stdout)
	}
	output := got.Stdout + got.Stderr
	if !strings.Contains(output, "retrieve[0].collection") {
		t.Errorf("the refusal does not name the field: %q", output)
	}
	if !strings.Contains(output, "runbooks") {
		t.Errorf("the refusal does not say what this deployment does declare: %q", output)
	}
}

// The rule openResolvableCatalog states for the MCP catalogue: a malformed catalogue
// refuses every command that loads playbooks, and none that only reads run history —
// which is what is most wanted right after something broke.
func TestAMalformedCollectionsFileRefusesTheDeployment(t *testing.T) {
	state, playbooks := deploymentWithACollection(t,
		`{"runbooks": {"directory": "/srv/runbooks", "tokenizer": "porter"}}`, "")

	refused := bintest.Run(t, "--state-dir", state, "validate", playbooks)
	if refused.ExitCode == 0 {
		t.Fatalf("a catalogue with an unknown key was accepted; stdout = %q", refused.Stdout)
	}
	if !strings.Contains(refused.Stderr, "tokenizer") {
		t.Errorf("the refusal does not name the key: %q", refused.Stderr)
	}

	history := bintest.Run(t, "--state-dir", state, "runs")
	if history.ExitCode != 0 {
		t.Fatalf("reading run history was refused for a catalogue it never needed: %q %q",
			history.Stdout, history.Stderr)
	}
}
