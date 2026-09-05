package playbook_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/playbook"
)

// deployment is what the corpora are judged against: what a deployment provides is part
// of whether a playbook is safe, not a property of the playbook alone.
func deployment() playbook.Deployment {
	return playbook.Deployment{
		MCPServers:    []string{"grafana"},
		SinkTypes:     []string{"discord", "slack", "github"},
		CreatingSinks: []string{"github"},
	}
}

// SC-002's accepting half. A denylist probed only on its refusals is an allowlist in
// disguise, and its false positives are what get a gate switched off rather than fixed.
func TestTheValidCorpusIsAccepted(t *testing.T) {
	for _, path := range corpus(t, "../../testdata/playbooks/valid") {
		t.Run(filepath.Base(path), func(t *testing.T) {
			book, err := playbook.ParseFile(path)
			if err != nil {
				t.Fatalf("the shape layer refused it: %v", err)
			}
			if problems := playbook.Validate(book, deployment()); len(problems) != 0 {
				for _, problem := range problems {
					t.Errorf("refused: %s", problem.Error())
				}
			}
		})
	}
}

// SC-002's refusing half, one playbook per rule, each pinned to the field it is about.
// Asserting only that a playbook was refused would let one drift into being refused for
// an unrelated reason while the corpus stayed green.
func TestTheHostileCorpusIsRefusedForItsOwnReason(t *testing.T) {
	because := map[string]struct{ field, says string }{
		"bare-shell.yaml":                    {"agent.tools[1]", "unrestricted shell"},
		"writing-tool.yaml":                  {"agent.tools[1]", "can write outside"},
		"allow-whole-mcp-server.yaml":        {"agent.allow[0]", "whole MCP server"},
		"scope-absolute.yaml":                {"agent.allow[0]", "outside the run's working directory"},
		"scope-traversal.yaml":               {"agent.allow[0]", "outside the run's working directory"},
		"unknown-mcp-server.yaml":            {"agent.mcp[0]", "not a server this deployment provides"},
		"unknown-sink-type.yaml":             {"sinks[0].carrier-pigeon", "not a sink this deployment implements"},
		"creating-sink-without-a-cap.yaml":   {"sinks[0].github.cap", "missing"},
		"bare-interpolation.yaml":            {"sinks[0].discord.webhook", "does not name its source"},
		"unrestricted-without-a-reason.yaml": {"agent.restricted", "no description saying why"},
		"missing-prompt-file.yaml":           {"agent.prompt_file", "not readable"},
		// Refused by the published schema before the semantic gate sees them, which is
		// the same rule at an earlier layer. The gate's own version is tested below,
		// against a document the schema never reads.
		"guard-block.yaml":    {"guard", "'not' failed"},
		"retrieve-block.yaml": {"retrieve", "'not' failed"},
	}

	paths := corpus(t, "../../testdata/playbooks/hostile")
	if len(paths) != len(because) {
		t.Fatalf("the corpus holds %d playbooks and %d are pinned to a reason",
			len(paths), len(because))
	}

	for _, path := range paths {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			want, pinned := because[name]
			if !pinned {
				t.Fatalf("%s has no pinned reason; add one so it cannot pass by accident", name)
			}

			book, err := playbook.ParseFile(path)
			if err != nil {
				// The shape layer refusing it is a refusal too, but a different one, and
				// the corpus is for the semantic gate.
				if !strings.Contains(err.Error(), want.says) {
					t.Fatalf("the shape layer refused it for another reason: %v", err)
				}
				return
			}

			problems := playbook.Validate(book, deployment())
			if len(problems) == 0 {
				t.Fatalf("%s was accepted", name)
			}
			var found bool
			for _, problem := range problems {
				if problem.Field == want.field && strings.Contains(problem.Found, want.says) {
					found = true
				}
			}
			if !found {
				t.Errorf("refused, but not for its reason.\n  want %s to say %q\n  got:", want.field, want.says)
				for _, problem := range problems {
					t.Errorf("    %s", problem.Error())
				}
			}
		})
	}
}

// A gate that stops at the first refusal costs an author one round trip per mistake,
// which is how a gate gets switched off.
func TestEveryRefusalIsReportedAtOnce(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "prompt.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "everything.yaml")
	if err := os.WriteFile(path, []byte(`
name: everything-at-once
trigger: {type: manual}
agent:
  model: m
  prompt_file: prompt.md
  tools: [Bash, Write, mcp__nope__thing]
  mcp: [nope]
  allow: ["Read(/etc/**)", mcp__nope]
  output_schema: {type: object}
sinks:
  - carrier-pigeon: {loft: "${loft}"}
`), 0o600); err != nil {
		t.Fatal(err)
	}

	book, err := playbook.ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	problems := playbook.Validate(book, deployment())

	fields := map[string]bool{}
	for _, problem := range problems {
		fields[problem.Field] = true
	}
	for _, want := range []string{
		"agent.tools[0]", "agent.tools[1]", "agent.tools[2]",
		"agent.mcp[0]", "agent.allow[0]", "agent.allow[1]",
		"sinks[0].carrier-pigeon",
	} {
		if !fields[want] {
			t.Errorf("%s was not reported; the gate stopped short", want)
		}
	}
}

// Every refusal says what would be accepted. A gate that says only what is wrong makes
// the author guess, and a gate that is guessed at gets switched off.
func TestEveryRefusalSaysWhatWouldBeAccepted(t *testing.T) {
	for _, path := range corpus(t, "../../testdata/playbooks/hostile") {
		book, err := playbook.ParseFile(path)
		if err != nil {
			continue
		}
		for _, problem := range playbook.Validate(book, deployment()) {
			if strings.TrimSpace(problem.Accepted) == "" {
				t.Errorf("%s: %s says nothing about what would be accepted",
					filepath.Base(path), problem.Field)
			}
			if problem.Field == "" {
				t.Errorf("%s: a refusal names no field", filepath.Base(path))
			}
		}
	}
}

// A scope is resolved before it is judged. "./inputs/../.." reads as relative and is not.
func TestAScopeIsResolvedBeforeItIsJudged(t *testing.T) {
	inside := []string{"./**", ".", "./inputs/**", "inputs/deep/../**", "./a/b/../c/**"}
	outside := []string{"/etc/**", "../**", "./inputs/../../etc/**", "~/**", "/**"}

	for _, scope := range inside {
		if problems := scopeOf(t, scope); len(problems) != 0 {
			t.Errorf("%q was refused: %s", scope, problems[0].Error())
		}
	}
	for _, scope := range outside {
		if problems := scopeOf(t, scope); len(problems) == 0 {
			t.Errorf("%q was accepted", scope)
		}
	}
}

func scopeOf(t *testing.T, scope string) []playbook.Problem {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "prompt.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "scope.yaml")
	document := "name: scope\ntrigger: {type: manual}\nagent:\n  model: m\n  prompt_file: prompt.md\n" +
		"  tools: [Read]\n  allow: [\"Read(" + scope + ")\"]\n  output_schema: {type: object}\n" +
		"sinks: [{discord: {webhook: \"${config.w}\"}}]\n"
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	book, err := playbook.ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return playbook.Validate(book, deployment())
}

func corpus(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".yaml" {
			paths = append(paths, filepath.Join(dir, entry.Name()))
		}
	}
	return paths
}

// FR-034 at the semantic layer. The schema refuses these first for a document read from
// disk, so this is the only place the gate's own rule runs — and a rule nothing reaches
// is a comment. Reached here by constructing the playbook rather than parsing one.
func TestTheGateRefusesAReservedBlockThatReachesIt(t *testing.T) {
	for name, book := range map[string]*playbook.Playbook{
		"guard":    {Name: "p", Guard: &playbook.Unknown{}},
		"retrieve": {Name: "p", Retrieve: &playbook.Unknown{}},
	} {
		problems := playbook.Validate(book, deployment())
		var found bool
		for _, problem := range problems {
			if problem.Field == name && strings.Contains(problem.Found, "does not apply it") {
				found = true
			}
		}
		if !found {
			t.Errorf("a %s block reached the gate and was not refused: %v", name, problems)
		}
	}
}

// The regex this replaced had the separator inside its character class, so it read a
// fully-qualified tool as naming a whole server. Both directions are pinned.
func TestAnMCPEntryIsReadByItsShape(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "prompt.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	build := func(t *testing.T, entry string) []playbook.Problem {
		t.Helper()
		path := filepath.Join(dir, "mcp.yaml")
		document := "name: mcp\ntrigger: {type: manual}\nagent:\n  model: m\n  prompt_file: prompt.md\n" +
			"  mcp: [grafana]\n  allow: [" + entry + "]\n  output_schema: {type: object}\n" +
			"sinks: [{discord: {webhook: \"${config.w}\"}}]\n"
		if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
			t.Fatal(err)
		}
		book, err := playbook.ParseFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return playbook.Validate(book, deployment())
	}

	if problems := build(t, "mcp__grafana__query_prometheus"); len(problems) != 0 {
		t.Errorf("a fully-qualified tool was refused: %s", problems[0].Error())
	}
	if problems := build(t, "mcp__grafana__deep__nested__tool"); len(problems) != 0 {
		t.Errorf("a tool with underscores in its name was refused: %s", problems[0].Error())
	}
	problems := build(t, "mcp__grafana")
	if len(problems) == 0 {
		t.Fatal("an entry naming a whole server was accepted")
	}
	if !strings.Contains(problems[0].Found, "whole MCP server") {
		t.Errorf("refused for another reason: %s", problems[0].Error())
	}
	if problems := build(t, "mcp__not_provided__thing"); len(problems) == 0 {
		t.Error("a tool on a server this deployment does not provide was accepted")
	}
}
