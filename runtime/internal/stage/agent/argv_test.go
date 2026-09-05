package agent_test

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/stage/agent"
)

func argIndex(args []string, want string) int { return slices.Index(args, want) }

func mustBuild(t *testing.T, decl agent.Declaration, mcpConfigPath string) []string {
	t.Helper()
	args, err := agent.BuildArgs(decl, mcpConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	return args
}

func valueAfter(t *testing.T, args []string, flag string) string {
	t.Helper()
	at := argIndex(args, flag)
	if at < 0 {
		t.Fatalf("%s is not in the argument vector: %v", flag, args)
	}
	if at+1 >= len(args) {
		t.Fatalf("%s has no value: %v", flag, args)
	}
	return args[at+1]
}

// The design in one test: a playbook that declares nothing still gets every bound. An
// absent flag is not a neutral default — it is the machine's own configuration.
func TestAPlaybookThatDeclaresNothingStillGetsEveryBound(t *testing.T) {
	args := mustBuild(t, agent.Declaration{Restricted: true}, "/run/mcp.json")

	if got := valueAfter(t, args, "--tools"); got != "" {
		t.Errorf(`--tools = %q, want "" — omitting it leaves the built-in set`, got)
	}
	if argIndex(args, "--strict-mcp-config") < 0 {
		t.Error("--strict-mcp-config is absent; the machine's MCP configuration is then inherited")
	}
	if got := valueAfter(t, args, "--mcp-config"); got != "/run/mcp.json" {
		t.Errorf("--mcp-config = %q", got)
	}
	if got := valueAfter(t, args, "--setting-sources"); got != "" {
		t.Errorf(`--setting-sources = %q, want ""`, got)
	}
	if argIndex(args, "--restricted") < 0 {
		t.Error("--restricted is absent")
	}
	if got := valueAfter(t, args, "--output-format"); got != "stream-json" {
		t.Errorf("--output-format = %q", got)
	}
	if argIndex(args, "--print") < 0 {
		t.Error("--print is absent; the process would wait for a terminal")
	}
}

func TestRestrictedIsOffOnlyWhenTheDeclarationSaysSo(t *testing.T) {
	off := mustBuild(t, agent.Declaration{Restricted: false}, "/run/mcp.json")
	if argIndex(off, "--restricted") >= 0 {
		t.Error("--restricted was passed for a declaration that turned it off")
	}
	// And the settings are still not inherited, which is the half `--restricted` would
	// otherwise have been carrying on its own.
	if got := valueAfter(t, off, "--setting-sources"); got != "" {
		t.Errorf(`--setting-sources = %q with restricted off`, got)
	}
}

func TestTheDeclaredToolSetAndAllowlistReachTheVector(t *testing.T) {
	args := mustBuild(t, agent.Declaration{
		Restricted: true,
		Tools:      []string{"Read", "Grep"},
		Allow:      []string{"Read(./**)", "mcp__grafana__query_prometheus"},
		Model:      "claude-sonnet-5",
	}, "/run/mcp.json")

	if got := valueAfter(t, args, "--tools"); got != "Read,Grep" {
		t.Errorf("--tools = %q", got)
	}
	if got := valueAfter(t, args, "--allowedTools"); got != "Read(./**),mcp__grafana__query_prometheus" {
		t.Errorf("--allowedTools = %q", got)
	}
	if got := valueAfter(t, args, "--model"); got != "claude-sonnet-5" {
		t.Errorf("--model = %q", got)
	}
}

// The constitution's Secrets constraint. Interpolation can put a configured value into a
// prompt, and a command line is journalled.
func TestThePromptIsNotInTheArgumentVector(t *testing.T) {
	args := mustBuild(t, agent.Declaration{Restricted: true}, "/run/mcp.json")

	for _, arg := range args {
		if strings.Contains(arg, "the prompt text") {
			t.Fatalf("the prompt is in the vector: %v", args)
		}
	}
	// Nor is there anywhere for it: the vector ends with flags and their values.
	if len(args)%2 == 1 && !strings.HasPrefix(args[len(args)-1], "--") {
		t.Fatalf("the vector ends with a bare value: %v", args)
	}
}

func TestTheOutputSchemaIsPassedThroughAsJSON(t *testing.T) {
	schema := map[string]any{"type": "object", "required": []any{"findings"}}
	args := mustBuild(t, agent.Declaration{Restricted: true, OutputSchema: schema}, "/run/mcp.json")

	var decoded map[string]any
	if err := json.Unmarshal([]byte(valueAfter(t, args, "--json-schema")), &decoded); err != nil {
		t.Fatalf("--json-schema is not JSON: %v", err)
	}
	if decoded["type"] != "object" {
		t.Fatalf("--json-schema = %v", decoded)
	}
}

// An empty configuration is written rather than skipped: the file existing is what makes
// --strict-mcp-config mean "these and no others" instead of "no constraint".
func TestAnEmptyMCPConfigurationIsStillWritten(t *testing.T) {
	dir := t.TempDir()

	path, err := agent.WriteMCPConfig(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		MCPServers map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if config.MCPServers == nil {
		t.Fatal("mcpServers is absent; an empty object is what bounds the run")
	}
	if len(config.MCPServers) != 0 {
		t.Fatalf("mcpServers = %v", config.MCPServers)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		t.Fatalf("mode %o; a server entry can carry a credential", mode)
	}
}

// One entry holding the separator becomes two entries on the command line, which is how
// a tool nobody declared reaches the process while every entry passes a per-entry check.
func TestAnEntryHoldingTheSeparatorIsRefused(t *testing.T) {
	for _, decl := range []agent.Declaration{
		{Restricted: true, Tools: []string{"Read,Bash"}},
		{Restricted: true, Allow: []string{"Read(./**),Bash(rm *)"}},
		{Restricted: true, Tools: []string{"Read", "Grep,Bash"}},
	} {
		args, err := agent.BuildArgs(decl, "/run/mcp.json")
		if !errors.Is(err, agent.ErrSeparatorInEntry) {
			t.Errorf("%+v was accepted and produced %v", decl, args)
		}
	}
}

func TestAnEntryWithoutTheSeparatorIsFine(t *testing.T) {
	if _, err := agent.BuildArgs(agent.Declaration{
		Restricted: true, Tools: []string{"Read", "Grep"},
		Allow: []string{"Read(./**)", "mcp__grafana__query_prometheus"},
	}, "/run/mcp.json"); err != nil {
		t.Fatal(err)
	}
}
