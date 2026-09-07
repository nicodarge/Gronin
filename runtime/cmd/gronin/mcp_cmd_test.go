package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/bintest"
)

func TestMCPListSaysWhenNothingIsProvided(t *testing.T) {
	got := bintest.Run(t, "--state-dir", t.TempDir(), "mcp", "list")

	if got.ExitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q", got.ExitCode, got.Stderr)
	}
	if !strings.Contains(got.Stdout, "no MCP servers") {
		t.Fatalf("stdout = %q", got.Stdout)
	}
}

// The listing names what the catalogue provides without ever printing a resolved value:
// there is nothing here for it to resolve against, and it must not try.
func TestMCPListNamesTheProvidedServersWithoutResolvingReferences(t *testing.T) {
	state := t.TempDir()
	document := `{
		"grafana": {"url": "https://grafana.example.com/mcp",
			"headers": {"Authorization": "Bearer ${config.grafana_token}"}},
		"netbox": {"command": "netbox-mcp", "args": ["--config", "/etc/netbox.json"],
			"env": {"NETBOX_TOKEN": "${config.netbox_token}"}}
	}`
	if err := os.WriteFile(filepath.Join(state, "mcp_servers.json"), []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}

	got := bintest.Run(t, "--state-dir", state, "mcp", "list")

	if got.ExitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q", got.ExitCode, got.Stderr)
	}
	for _, want := range []string{"grafana", "netbox", "NETBOX_TOKEN", "Authorization", "netbox-mcp"} {
		if !strings.Contains(got.Stdout, want) {
			t.Errorf("stdout does not mention %q: %q", want, got.Stdout)
		}
	}
	if strings.Contains(got.Stdout, "config.grafana_token") || strings.Contains(got.Stdout, "config.netbox_token") {
		t.Errorf("stdout prints the raw reference rather than just the key: %q", got.Stdout)
	}
}

// A malformed catalogue is refused wherever it is loaded, naming the server, rather than
// silently providing an empty one.
func TestAMalformedCatalogueRefusesTheDeployment(t *testing.T) {
	state := t.TempDir()
	document := `{"both": {"command": "netbox-mcp", "url": "https://example.com/mcp"}}`
	if err := os.WriteFile(filepath.Join(state, "mcp_servers.json"), []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}

	got := bintest.Run(t, "--state-dir", state, "mcp", "list")

	if got.ExitCode == 0 {
		t.Fatalf("exit code = 0 on a catalogue naming both transports; stdout = %q", got.Stdout)
	}
	if !strings.Contains(got.Stderr, "both") {
		t.Fatalf("the refusal does not say why: %q", got.Stderr)
	}
}

// End to end: a playbook naming a catalogued server is accepted by the same gate that
// used to refuse every playbook naming any server at all.
func TestValidateAcceptsAPlaybookNamingACataloguedServer(t *testing.T) {
	state := t.TempDir()
	if err := os.WriteFile(filepath.Join(state, "mcp_servers.json"),
		[]byte(`{"grafana": {"url": "https://grafana.example.com/mcp"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	playbooks := filepath.Join(state, "playbooks")
	if err := os.MkdirAll(playbooks, 0o750); err != nil {
		t.Fatal(err)
	}
	document := `
name: drift-check
trigger: {type: manual}
agent:
  model: claude-sonnet-5
  prompt_file: prompt.md
  mcp: [grafana]
  output_schema: {type: object}
sinks:
  - discord: {webhook: "https://example.com/hook"}
`
	if err := os.WriteFile(filepath.Join(playbooks, "book.yaml"), []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(playbooks, "prompt.md"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := bintest.Run(t, "--state-dir", state, "validate")

	if got.ExitCode != 0 {
		t.Fatalf("exit code = %d, stdout = %q, stderr = %q", got.ExitCode, got.Stdout, got.Stderr)
	}
	if !strings.Contains(got.Stdout, "1 playbook(s) accepted") {
		t.Fatalf("stdout = %q", got.Stdout)
	}
}
