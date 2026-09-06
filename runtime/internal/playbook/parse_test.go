package playbook_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/playbook"
)

// T012, SC-002's shape half. Fifteen documents, eleven of which the published schema must
// refuse. The corpus is here rather than in a probe script so it keeps running: a probe
// that passed once, on a machine that no longer exists, is a claim rather than a check.
//
// Every refusal here is one JSON Schema can express. The ones it cannot — a shell in a
// tool set, a path escaping the working directory, an MCP server this deployment does not
// provide — belong to the validator and have their own corpus.
func TestTheSchemaAcceptsAndRefusesTheProbeCorpus(t *testing.T) {
	accepted := documentsIn(t, "testdata/schema/accepted")
	refused := documentsIn(t, "testdata/schema/refused")

	if len(accepted)+len(refused) != 15 {
		t.Fatalf("the corpus holds %d documents, and it is meant to hold fifteen",
			len(accepted)+len(refused))
	}
	if len(refused) != 11 {
		t.Fatalf("%d documents are meant to be refused, and eleven are", len(refused))
	}

	for name, document := range accepted {
		if _, err := playbook.Parse(name, document); err != nil {
			t.Errorf("%s was refused: %v", name, err)
		}
	}
	// Each document is pinned to the reason it exists for. Asserting only that it was
	// refused would let one drift into being refused by accident — a typo in a field the
	// case does not care about — and the corpus would stay green while covering nothing.
	because := map[string]string{
		"agent-without-output-schema.yaml": "agent: missing property 'output_schema'",
		"cron-without-schedule.yaml":       "trigger: missing property 'schedule'",
		"gather-step-without-a-name.yaml":  "gather/0: missing property 'as'",
		"guard-block.yaml":                 "guard: 'not' failed",
		"name-not-a-slug.yaml":             "does not match pattern",
		"no-name.yaml":                     "missing property 'name'",
		"no-sinks.yaml":                    "sinks: minItems: got 0, want 1",
		"output-schema-as-a-path.yaml":     "agent/output_schema: got string, want object",
		"sink-with-two-types.yaml":         "sinks/0: maxProperties: got 2, want 1",
		"trigger-type-unknown.yaml":        "trigger/type: value must be one of 'cron', 'manual'",
		"unknown-top-level-key.yaml":       "additional properties 'on_failure' not allowed",
	}
	for path, document := range refused {
		_, err := playbook.Parse(path, document)
		if err == nil {
			t.Errorf("%s was accepted, and the schema is meant to refuse it", path)
			continue
		}
		want, ok := because[filepath.Base(path)]
		if !ok {
			t.Errorf("%s has no pinned reason; add one so it cannot pass by accident", path)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%s was refused, but not for its reason.\n  want: %s\n  got:  %v", path, want, err)
		}
	}
}

func documentsIn(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	documents := map[string][]byte{}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path) //nolint:gosec // a fixture directory in this package
		if err != nil {
			t.Fatal(err)
		}
		documents[path] = data
	}
	return documents
}

// The schema is published from the contract and embedded for the executable. Two copies
// of one document drift, and the copy that drifts is the one nobody reads — so the drift
// is what is asserted, not either copy.
func TestTheEmbeddedSchemaIsTheContract(t *testing.T) {
	contract, err := os.ReadFile("../../../specs/001-runtime-core/contracts/playbook.schema.json")
	if err != nil {
		t.Skipf("the specification is not beside this module: %v", err)
	}
	if string(contract) != string(playbook.Schema) {
		t.Fatal("the embedded schema and specs/001-runtime-core/contracts/playbook.schema.json " +
			"have diverged; the contract is the source, copy it over")
	}
}

func TestParseDecodesWhatTheRuntimeReads(t *testing.T) {
	got, err := playbook.ParseFile("testdata/schema/accepted/manual-with-gather.yaml")
	if err != nil {
		t.Fatal(err)
	}

	if got.Name != "cert-expiry" || got.Trigger.Type != "manual" {
		t.Fatalf("parsed as %+v", got)
	}
	if len(got.Gather) != 1 || got.Gather[0].As != "inventory.json" {
		t.Fatalf("gather = %+v", got.Gather)
	}
	if !got.Agent.IsRestricted() {
		t.Fatal("restricted defaulted to false; a playbook that says nothing gets the bound")
	}
	timeout, err := got.Agent.StageTimeout()
	if err != nil || timeout.Minutes() != 10 {
		t.Fatalf("timeout = %v, err = %v", timeout, err)
	}
	if want := filepath.Join("testdata/schema/accepted", "prompts/cert-expiry.md"); got.PromptPath() != want {
		t.Fatalf("prompt path = %q, want %q", got.PromptPath(), want)
	}
}

func TestRestrictedIsOnlyOffWhenSaidSo(t *testing.T) {
	off, err := playbook.ParseFile("testdata/schema/accepted/unrestricted-with-a-reason.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if off.Agent.IsRestricted() {
		t.Fatal("restricted: false did not read as false")
	}
	if off.Description == "" {
		t.Fatal("the fixture is meant to carry the reason the bound is off")
	}

	on, err := playbook.ParseFile("testdata/schema/accepted/minimal-cron.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !on.Agent.IsRestricted() {
		t.Fatal("a playbook that declares nothing lost the default bound")
	}
	if timeout, err := on.Agent.StageTimeout(); err != nil || timeout != playbook.DefaultTimeout {
		t.Fatalf("timeout = %v, err = %v", timeout, err)
	}
}

func TestParseRefusesSomethingThatIsNotAPlaybook(t *testing.T) {
	for name, document := range map[string]string{
		"empty":       "",
		"a list":      "- not a playbook\n",
		"a scalar":    "42\n",
		"broken yaml": "name: [unclosed\n",
	} {
		if _, err := playbook.Parse(name, []byte(document)); err == nil {
			t.Errorf("%s was accepted as a playbook", name)
		}
	}
}

func TestLoadReadsADirectoryAndCollectsEveryRefusal(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.WriteFile(filepath.Join(dir, "p.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	good := `
name: %s
trigger: {type: manual}
agent: {model: m, prompt_file: p.md, output_schema: {type: object}}
sinks: [{discord: {webhook: "${config.w}"}}]
`
	write("one.yaml", fmt.Sprintf(good, "one"))
	write("two.yaml", fmt.Sprintf(good, "two"))
	write("broken.yaml", "name: [unclosed\n")
	write("no-sinks.yaml", "name: three\ntrigger: {type: manual}\nagent: {model: m, prompt_file: p.md, output_schema: {type: object}}\nsinks: []\n")
	write("notes.md", "not a playbook, and not read")

	loaded, err := playbook.Load(dir, deployment())
	if err != nil {
		t.Fatal(err)
	}

	if len(loaded.Playbooks) != 2 {
		t.Fatalf("accepted %v", loaded.Names())
	}
	if len(loaded.Refusals) != 2 {
		t.Fatalf("%d refusals, want 2: %v", len(loaded.Refusals), loaded.Refusals)
	}
	if loaded.OK() {
		t.Fatal("a set with refusals reported itself ready to arm")
	}
	if _, found := loaded.Find("one"); !found {
		t.Fatal("a loaded playbook cannot be found by name")
	}
}

// FR-042. Two playbooks with one name make every record ambiguous, and the single-flight
// guard would treat them as one playbook.
func TestLoadRefusesTwoPlaybooksWithOneName(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "p.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := `
name: drift-check
trigger: {type: manual}
agent: {model: m, prompt_file: p.md, output_schema: {type: object}}
sinks: [{discord: {webhook: "${config.w}"}}]
`
	for _, name := range []string{"a.yaml", "b.yaml"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	loaded, err := playbook.Load(dir, deployment())
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Refusals) != 1 {
		t.Fatalf("refusals = %v", loaded.Refusals)
	}
	if !strings.Contains(loaded.Refusals[0].Error(), "already declared by a.yaml") {
		t.Fatalf("the refusal does not name the other file: %v", loaded.Refusals[0])
	}
}

// A sink's value has to be a mapping. Reporting a scalar as "a sink that configured
// nothing" sends the author looking for a missing field rather than at the shape.
func TestASinkValueThatIsNotAMappingIsMalformedRatherThanEmpty(t *testing.T) {
	book, err := playbook.Parse("x.yaml", []byte(
		"name: n\ntrigger: {type: manual}\n"+
			"agent: {model: m, prompt_file: p.md, output_schema: {type: object}}\n"+
			"sinks:\n  - discord: true\n"))
	if err != nil {
		t.Fatal(err)
	}

	name, config, ok := book.Sinks[0].Type()
	if ok {
		t.Fatalf("a scalar under a sink key read as a configuration: %v", config)
	}
	if name != "discord" {
		t.Fatalf("the type name was lost: %q", name)
	}

	// And a mapping still reads as one.
	book, err = playbook.Parse("y.yaml", []byte(
		"name: n\ntrigger: {type: manual}\n"+
			"agent: {model: m, prompt_file: p.md, output_schema: {type: object}}\n"+
			"sinks:\n  - discord:\n      webhook: \"${config.w}\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	name, config, ok = book.Sinks[0].Type()
	if !ok || name != "discord" || config["webhook"] != "${config.w}" {
		t.Fatalf("a well-formed sink read as %q %v %v", name, config, ok)
	}
}
