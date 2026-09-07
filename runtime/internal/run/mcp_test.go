package run_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/fakeagent"
	"github.com/nicodarge/Gronin/runtime/internal/mcpcatalog"
	"github.com/nicodarge/Gronin/runtime/internal/record"
)

const mcpPlaybook = `
name: drift-check
trigger:
  type: manual
agent:
  model: claude-sonnet-5
  prompt_file: prompt.md
  tools: [Read]
  mcp: [grafana]
  output_schema:
    type: object
sinks:
  - discord:
      webhook: ${config.ops_webhook}
`

func writeCatalog(t *testing.T, document string) *mcpcatalog.Catalog {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp_servers.json")
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := mcpcatalog.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

// A catalogued server that no playbook names never reaches the child. serversOf iterates
// the playbook's own agent.mcp and nothing else, and this is the test that would fail if
// it iterated the catalogue instead: fakeagent reports back exactly what --mcp-config
// held, and the receipt check aborts a run whose child reports a server the playbook
// never declared.
func TestOnlyTheServersAPlaybookNamesReachTheChild(t *testing.T) {
	h := newHarness(t, fakeagent.ModeSuccess)
	h.executor.AgentEnv = append(h.executor.AgentEnv, fakeagent.ResultVar+`={"findings":[]}`)
	h.executor.Catalog = writeCatalog(t, `{
		"grafana": {"url": "https://grafana.example.com/mcp"},
		"unused": {"url": "https://unused.example.com/mcp"}
	}`)
	book := h.playbook(t, mcpPlaybook)

	got, err := h.executor.Execute(t.Context(), book, record.TriggerManual, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != record.StatusSucceeded {
		t.Fatalf("status = %q, error = %q; a catalogued-but-unnamed server reached the child",
			got.Status, got.Error)
	}
}

// A catalogue entry whose env value references a configuration key the deployment never
// set is refused, naming both the server and the key.
func TestAnUnresolvableCatalogueReferenceRefusesTheRun(t *testing.T) {
	h := newHarness(t, fakeagent.ModeSuccess)
	h.executor.Catalog = writeCatalog(t, `{
		"grafana": {"command": "grafana-mcp", "env": {"TOKEN": "${config.grafana_token}"}}
	}`)
	book := h.playbook(t, mcpPlaybook)

	got, err := h.executor.Execute(t.Context(), book, record.TriggerManual, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != record.StatusRefused {
		t.Fatalf("status = %q, want refused", got.Status)
	}
	if !strings.Contains(got.Error, `"grafana"`) || !strings.Contains(got.Error, "grafana_token") {
		t.Fatalf("the refusal does not name the server and the key: %q", got.Error)
	}
}
