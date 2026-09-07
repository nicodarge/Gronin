package mcpcatalog_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/mcpcatalog"
)

func write(t *testing.T, dir, document string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "mcp_servers.json"), []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A deployment with no catalogue file provides no server, which is today's behaviour —
// the same contract internal/config.Load holds for a missing config.json.
func TestAnAbsentCatalogueProvidesNothing(t *testing.T) {
	catalog, err := mcpcatalog.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if names := catalog.Names(); len(names) != 0 {
		t.Fatalf("Names() = %v, want none", names)
	}
}

func TestAStdioAndAnHTTPEntryAreBothLoaded(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, `{
		"netbox": {"command": "netbox-mcp", "args": ["--config", "/etc/netbox.json"],
			"env": {"NETBOX_TOKEN": "${config.netbox_token}"}},
		"grafana": {"url": "https://grafana.example.com/mcp",
			"headers": {"Authorization": "Bearer ${config.grafana_token}"}}
	}`)

	catalog, err := mcpcatalog.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := catalog.Names()
	want := []string{"grafana", "netbox"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
}

// SC-002's accepting half for this package: a denylist probed only on its refusals is an
// allowlist in disguise.
func TestAnEntryNamingExactlyOneTransportIsAccepted(t *testing.T) {
	for name, document := range map[string]string{
		"stdio, bare":       `{"s": {"command": "netbox-mcp"}}`,
		"stdio, with args":  `{"s": {"command": "netbox-mcp", "args": ["-x"]}}`,
		"stdio, with env":   `{"s": {"command": "netbox-mcp", "env": {"K": "${config.x}"}}}`,
		"http, bare":        `{"s": {"url": "https://example.com/mcp"}}`,
		"http, with header": `{"s": {"url": "https://example.com/mcp", "headers": {"H": "${config.x}"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, document)
			if _, err := mcpcatalog.Load(dir); err != nil {
				t.Fatalf("refused: %v", err)
			}
		})
	}
}

func TestAnEntryNamingBothTransportsIsRefused(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, `{"both": {"command": "netbox-mcp", "url": "https://example.com/mcp"}}`)

	_, err := mcpcatalog.Load(dir)
	if err == nil {
		t.Fatal("accepted")
	}
	if !strings.Contains(err.Error(), `"both"`) || !strings.Contains(err.Error(), "both a command and a url") {
		t.Fatalf("does not name the server or the reason: %v", err)
	}
}

func TestAnEntryNamingNeitherTransportIsRefused(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, `{"neither": {"args": ["-x"]}}`)

	_, err := mcpcatalog.Load(dir)
	if err == nil {
		t.Fatal("accepted")
	}
	if !strings.Contains(err.Error(), `"neither"`) || !strings.Contains(err.Error(), "names neither") {
		t.Fatalf("does not name the server or the reason: %v", err)
	}
}

func TestResolveInterpolatesEnvAndHeaderValues(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, `{
		"netbox": {"command": "netbox-mcp", "args": ["--config", "/etc/netbox.json"],
			"env": {"NETBOX_TOKEN": "${config.netbox_token}"}}
	}`)
	catalog, err := mcpcatalog.Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	interpolate := func(text string) (string, error) {
		if text == "${config.netbox_token}" {
			return "REPLACE_ME", nil
		}
		return text, nil
	}
	resolved, err := catalog.Resolve("netbox", interpolate)
	if err != nil {
		t.Fatal(err)
	}
	if resolved["command"] != "netbox-mcp" {
		t.Errorf("command = %v", resolved["command"])
	}
	env, ok := resolved["env"].(map[string]string)
	if !ok || env["NETBOX_TOKEN"] != "REPLACE_ME" {
		t.Fatalf("env = %v", resolved["env"])
	}
}

func TestResolveOfAnHTTPEntryNamesTheTransport(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, `{"grafana": {"url": "https://grafana.example.com/mcp"}}`)
	catalog, err := mcpcatalog.Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	resolved, err := catalog.Resolve("grafana", func(text string) (string, error) { return text, nil })
	if err != nil {
		t.Fatal(err)
	}
	if resolved["type"] != "http" || resolved["url"] != "https://grafana.example.com/mcp" {
		t.Fatalf("resolved = %v", resolved)
	}
}

// A reference that resolves to nothing is refused naming both the server and the missing
// key, not just what internal/config.Interpolate says on its own.
func TestResolveRefusesAnUnresolvableReference(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, `{"netbox": {"command": "netbox-mcp", "env": {"NETBOX_TOKEN": "${config.missing}"}}}`)
	catalog, err := mcpcatalog.Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	_, err = catalog.Resolve("netbox", func(text string) (string, error) {
		return "", errUnresolved{text}
	})
	if err == nil {
		t.Fatal("resolved")
	}
	if !strings.Contains(err.Error(), `"netbox"`) || !strings.Contains(err.Error(), `"NETBOX_TOKEN"`) {
		t.Fatalf("does not name the server and the key: %v", err)
	}
}

type errUnresolved struct{ reference string }

func (e errUnresolved) Error() string { return e.reference + " is not configured" }

func TestResolveRefusesAServerNotInTheCatalogue(t *testing.T) {
	catalog, err := mcpcatalog.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Resolve("nope", func(text string) (string, error) { return text, nil }); err == nil {
		t.Fatal("resolved a server the catalogue does not hold")
	}
}

// Summary never resolves a reference, so it can never print a value Resolve would have
// produced from the deployment configuration.
func TestSummaryNeverPrintsAResolvedValueOrTheRawReference(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, `{"netbox": {"command": "netbox-mcp",
		"env": {"NETBOX_TOKEN": "${config.netbox_token}"}}}`)
	catalog, err := mcpcatalog.Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	summary := catalog.Summary("netbox")
	if !strings.Contains(summary, "NETBOX_TOKEN") {
		t.Errorf("summary does not name the key: %q", summary)
	}
	if strings.Contains(summary, "${config.netbox_token}") {
		t.Errorf("summary prints the raw reference: %q", summary)
	}
	if strings.Contains(summary, "REPLACE_ME") {
		t.Errorf("summary prints a resolved value: %q", summary)
	}
}

// The gate already refuses a playbook's own ${config.x} that resolves to nothing. A
// catalogue reference is the same kind of reference, and without this it would be the one
// class that surfaces at the first run instead — possibly a scheduled one, at night.
func TestAReferenceToAnUnconfiguredKeyIsRefusedAtLoad(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, `{
		"grafana": {"url": "https://grafana.example.com/mcp",
			"headers": {"Authorization": "Bearer ${config.grafana_token}"}}
	}`)
	catalog, err := mcpcatalog.Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	if err := catalog.CheckReferences([]string{"grafana_token"}); err != nil {
		t.Fatalf("a configured key was refused: %v", err)
	}

	err = catalog.CheckReferences([]string{"something_else"})
	if err == nil {
		t.Fatal("a reference to an unconfigured key was accepted")
	}
	for _, want := range []string{"grafana", "Authorization", "grafana_token"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
}

func TestAReferenceNamingNoSourceOrTheWrongOneIsRefused(t *testing.T) {
	for name, value := range map[string]string{
		"no source":    "Bearer ${grafana_token}",
		"wrong source": "Bearer ${trigger.grafana_token}",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, `{"grafana": {"url": "https://grafana.example.com/mcp",
				"headers": {"Authorization": "`+value+`"}}}`)
			catalog, err := mcpcatalog.Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := catalog.CheckReferences([]string{"grafana_token"}); err == nil {
				t.Fatalf("%s was accepted", name)
			}
		})
	}
}

// The other half of json.Unmarshal's error path: a file that is not JSON at all.
func TestACatalogueThatIsNotJSONIsRefused(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "this is not json")
	if _, err := mcpcatalog.Load(dir); err == nil {
		t.Fatal("a catalogue that is not JSON was accepted")
	}
}
