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

// valuesAfter returns every argument a variadic flag consumed, which is up to the next
// flag. The entries are separate arguments precisely so nothing can rejoin them.
func valuesAfter(t *testing.T, args []string, flag string) []string {
	t.Helper()
	at := argIndex(args, flag)
	if at < 0 {
		t.Fatalf("%s is not in the argument vector: %v", flag, args)
	}
	var values []string
	for _, arg := range args[at+1:] {
		if strings.HasPrefix(arg, "--") {
			break
		}
		values = append(values, arg)
	}
	return values
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

	if got := valuesAfter(t, args, "--tools"); !slices.Equal(got, []string{""}) {
		t.Errorf(`--tools = %v, want [""] — omitting it leaves the built-in set`, got)
	}
	// The executable refuses stream-json under --print without it, so it is a bound on
	// whether the stage runs at all rather than a preference.
	if argIndex(args, "--verbose") < 0 {
		t.Error("--verbose is absent; the executable refuses stream-json under --print without it")
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

	if got := valuesAfter(t, args, "--tools"); !slices.Equal(got, []string{"Read", "Grep"}) {
		t.Errorf("--tools = %v", got)
	}
	if got := valuesAfter(t, args, "--allowedTools"); !slices.Equal(got,
		[]string{"Read(./**)", "mcp__grafana__query_prometheus"}) {
		t.Errorf("--allowedTools = %v", got)
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

// A space is a legitimate part of one entry — `Bash(git *)` is the executable's own
// example — and each entry is its own argument, so nothing rejoins them.
func TestAnEntryMayHoldASpace(t *testing.T) {
	args := mustBuild(t, agent.Declaration{
		Restricted: true, Allow: []string{"Bash(git *)", "Read(./**)"},
	}, "/run/mcp.json")

	got := valuesAfter(t, args, "--allowedTools")
	if !slices.Equal(got, []string{"Bash(git *)", "Read(./**)"}) {
		t.Fatalf("--allowedTools = %v", got)
	}
}

func TestAnEntryThatWouldBeReadAsAFlagIsRefused(t *testing.T) {
	for _, decl := range []agent.Declaration{
		{Restricted: true, Tools: []string{"--dangerously-skip-permissions"}},
		{Restricted: true, Allow: []string{"-p"}},
	} {
		if args, err := agent.BuildArgs(decl, "/run/mcp.json"); !errors.Is(err, agent.ErrEntryLooksLikeAFlag) {
			t.Errorf("%+v was accepted and produced %v", decl, args)
		}
	}
}
