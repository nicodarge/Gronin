package playbook_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/playbook"
	"github.com/nicodarge/Gronin/runtime/internal/sink"
	"github.com/nicodarge/Gronin/runtime/internal/stage/agent"
)

// repoRoot is where the documents and the shipped examples live, relative to this
// package. A checkout that does not carry them skips rather than fails, the way
// TestTheEmbeddedSchemaIsTheContract does.
const repoRoot = "../../.."

// fence is the opt-in an example uses to say it is a whole playbook rather than a
// fragment. It is the one check-doc-examples.py reads, deliberately, so an example is
// swept by both or by neither.
var fence = regexp.MustCompile("(?ms)^```yaml playbook\\s*$(.*?)^```\\s*$")

// TestDocumentedExamplesAreAccepted drives every documented example through the loader
// that arms a playbook, and compiles the output schema the run would be checked against.
//
// check-doc-examples.py validates the same examples against the published JSON Schema,
// which is the shape layer and is explicitly not the gate. Three defects shipped in the
// one example the quickstart tells a reader to copy, and the shape layer accepted all
// three: a sink type this deployment does not implement, a `label` field the GitHub sink
// never reads, and an `output_schema` that no run could ever compile. Only the first was
// reachable by the loader. This is the check the other two needed.
func TestDocumentedExamplesAreAccepted(t *testing.T) {
	documents := documentation(t)
	if len(documents) == 0 {
		t.Skipf("no documentation beside this module")
	}

	found := 0
	for _, path := range documents {
		text, err := os.ReadFile(path) //nolint:gosec // a document in this repository
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range fence.FindAllStringSubmatch(string(text), -1) {
			found++
			where := filepath.Base(path)
			t.Run(where, func(t *testing.T) {
				book := loadOne(t, where, match[1])
				compiles(t, where, book)
				matchesShipped(t, where, book.Name, match[1])
			})
		}
	}
	if found == 0 {
		t.Fatal("no documented example found; is the fence tagged '```yaml playbook'?")
	}
}

// TestTheShippedExampleIsAccepted is SC-001's first step. quickstart.md tells a stranger
// to copy examples/ into their playbook directory, so what is shipped there has to pass
// the same gate `gronin validate` applies, prompt file included.
func TestTheShippedExampleIsAccepted(t *testing.T) {
	dir := filepath.Join(repoRoot, "examples")
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("no examples directory beside this module: %v", err)
	}

	loaded, err := playbook.Load(dir, shippedDeployment(filesIn(t, dir)...))
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.OK() {
		t.Fatalf("the shipped examples are refused by the gate:\n%v", loaded.Err())
	}
	if len(loaded.Playbooks) == 0 {
		t.Fatal("the examples directory holds no playbook, and quickstart.md copies one out of it")
	}
	for _, book := range loaded.Playbooks {
		compiles(t, book.Name, book)
	}
}

// loadOne writes the example where the gate can read it. A playbook is a YAML file plus
// a prompt file beside it, and the gate refuses one whose prompt is missing — so the
// prompt is created rather than the check being skipped.
func loadOne(t *testing.T, where, document string) *playbook.Playbook {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "example.yaml")
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}

	book, err := playbook.ParseFile(path)
	if err != nil {
		t.Fatalf("%s: the documented example does not parse: %v", where, err)
	}
	if name := book.Agent.PromptFile; name != "" {
		prompt := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(prompt), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(prompt, []byte("a prompt.\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	loaded, err := playbook.Load(dir, shippedDeployment(document))
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.OK() {
		t.Fatalf("%s: the documented example is refused by the gate:\n%v", where, loaded.Err())
	}
	return loaded.Playbooks[0]
}

// compiles asserts the declared output schema is one the run could check an answer
// against. The load gate does not reach this: it reads the playbook's shape, and a schema
// that only fails when it is compiled fails after the agent has been paid for.
func compiles(t *testing.T, where string, book *playbook.Playbook) {
	t.Helper()
	err := agent.ValidateReport([]byte(`{}`), book.Agent.OutputSchema)
	if err != nil && strings.Contains(err.Error(), "does not compile") {
		t.Errorf("%s: output_schema never compiles, so every run fails after paying for "+
			"the agent: %v", where, err)
	}
}

// shippedDeployment is what the gate is applied with — the real one, not the corpus's.
// Derived from the sink package rather than listed here: a copy of that list drifts, and
// the copy that drifts is this one.
//
// The configuration keys come from the example itself. This check asks whether the
// example is one the gate accepts, not which values an operator happens to have set —
// quickstart.md is what tells them to set these. Every other refusal still applies.
func shippedDeployment(references ...string) playbook.Deployment {
	keys := []string{}
	for _, text := range references {
		for _, match := range configReference.FindAllStringSubmatch(text, -1) {
			keys = append(keys, match[1])
		}
	}
	return playbook.Deployment{
		MCPServers:    nil,
		SinkTypes:     sink.Types(),
		CreatingSinks: sink.CreatingTypes(),
		ConfigKeys:    keys,
	}
}

var configReference = regexp.MustCompile(`\$\{config\.([^}]*)\}`)

// filesIn is every file in a directory, read. The gate resolves references in a prompt
// body as well as in the playbook, so both have to be looked at to know which keys the
// example needs.
func filesIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name())) //nolint:gosec // a shipped example
		if err != nil {
			t.Fatal(err)
		}
		texts = append(texts, string(data))
	}
	return texts
}

func documentation(t *testing.T) []string {
	t.Helper()
	var paths []string
	for _, pattern := range []string{"*.md", "docs/*.md", "docs/**/*.md", "specs/**/*.md"} {
		matches, err := filepath.Glob(filepath.Join(repoRoot, pattern))
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, matches...)
	}
	return paths
}

// matchesShipped pins a documented example to the file it documents. A reader copies the
// shipped file and reads the document, so the two saying different things is a defect
// neither one can show on its own — and correcting one of them is exactly how they came
// apart. Only an example naming a shipped playbook is held to this.
func matchesShipped(t *testing.T, where, name, documented string) {
	t.Helper()
	path := filepath.Join(repoRoot, "examples", name+".yaml")
	shipped, err := os.ReadFile(path) //nolint:gosec // a shipped example in this repository
	if err != nil {
		return
	}
	// The capture group opens with the newline that ended the fence line. Exactly one is
	// removed, so a genuine difference in leading whitespace still shows.
	if string(shipped) != strings.TrimPrefix(documented, "\n") {
		t.Errorf("%s documents %q and examples/%s.yaml ships something else; the reader "+
			"copies the file and reads the document", where, name, name)
	}
}
