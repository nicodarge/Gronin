package playbook_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/playbook"
)

// T012, SC-002's shape half, SC-109's since the guard block stopped being refused whole,
// and SC-203's since the retrieve block did. T014 added the webhook trigger's own
// fixtures. The corpus is here rather than in a probe script so it keeps running: a probe
// that passed once, on a machine that no longer exists, is a claim rather than a check.
//
// Every refusal here is one JSON Schema can express. The ones it cannot — a shell in a
// tool set, a path escaping the working directory, an MCP server this deployment does not
// provide — belong to the validator and have their own corpus.
func TestTheSchemaAcceptsAndRefusesTheProbeCorpus(t *testing.T) {
	accepted := documentsIn(t, "testdata/schema/accepted")
	refused := documentsIn(t, "testdata/schema/refused")

	if len(accepted)+len(refused) != 33 {
		t.Fatalf("the corpus holds %d documents, and it is meant to hold thirty-three",
			len(accepted)+len(refused))
	}
	if len(refused) != 27 {
		t.Fatalf("%d documents are meant to be refused, and twenty-seven are", len(refused))
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
		"agent-without-output-schema.yaml":         "agent: missing property 'output_schema'",
		"cron-without-schedule.yaml":               "trigger: missing property 'schedule'",
		"gather-step-without-a-name.yaml":          "gather/0: missing property 'as'",
		"guard-unknown-key.yaml":                   "guard: additional properties 'lock' not allowed",
		"guard-rate-per-seconds.yaml":              "guard/rate/per: '30s' does not match pattern",
		"guard-rate-zero-runs.yaml":                "guard/rate/runs: minimum: got 0, want 1",
		"name-not-a-slug.yaml":                     "does not match pattern",
		"no-name.yaml":                             "missing property 'name'",
		"no-sinks.yaml":                            "sinks: minItems: got 0, want 1",
		"output-schema-as-a-path.yaml":             "agent/output_schema: got string, want object",
		"sink-with-two-types.yaml":                 "sinks/0: maxProperties: got 2, want 1",
		"trigger-type-unknown.yaml":                "trigger/type: value must be one of 'cron', 'manual', 'webhook'",
		"unknown-top-level-key.yaml":               "additional properties 'on_failure' not allowed",
		"webhook-without-source.yaml":              "trigger: missing property 'source'",
		"webhook-with-schedule.yaml":               "trigger: 'not' failed",
		"cron-with-source.yaml":                    "trigger: 'not' failed",
		"webhook-value-without-pattern.yaml":       "trigger/values/alertname: missing property 'pattern'",
		"webhook-value-without-max-length.yaml":    "trigger/values/alertname: missing property 'max_length'",
		"webhook-value-pointer-without-slash.yaml": "trigger/values/alertname/at: 'alertname' does not match pattern",
	}
	// The retrieve block's own reasons live with the test that is about them, so the two
	// lists cannot come to disagree about why a fixture is refused.
	for name, says := range retrieveShapeReasons {
		because[name] = says
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

// SC-109, the shape half: the guard block is probed on what it must refuse, and each
// refusal is pinned to its reason so that one cannot pass on another's. What it must
// accept is probed too — a schema that refused the whole block again would pass every
// refusal here, which is exactly what the runtime core's schema did.
func TestGuardBlockShape(t *testing.T) {
	document := func(guard string) []byte {
		return []byte("name: drift-check\ntrigger: {type: manual}\n" + guard +
			"agent: {model: m, prompt_file: p.md, output_schema: {type: object}}\n" +
			"sinks: [{discord: {channel: \"#ops\"}}]\n")
	}

	for name, probe := range map[string]struct{ guard, because string }{
		"an unknown key":                          {"guard: {lock: drift}\n", "guard: additional properties 'lock' not allowed"},
		"no runs at all":                          {"guard: {rate: {runs: 0, per: 1h}}\n", "guard/rate/runs: minimum: got 0, want 1"},
		"a window in seconds":                     {"guard: {rate: {runs: 2, per: 30s}}\n", "guard/rate/per: '30s' does not match pattern"},
		"an empty window":                         {"guard: {rate: {runs: 2, per: 0m}}\n", "guard/rate/per: '0m' does not match pattern"},
		"a wait that is a number, not a duration": {"guard: {wait: 5}\n", "guard/wait: got number, want string"},
		"a rate without per":                      {"guard: {rate: {runs: 2}}\n", "guard/rate: missing property 'per'"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := playbook.Parse(name, document(probe.guard))
			if err == nil {
				t.Fatalf("%q was accepted", probe.guard)
			}
			if !strings.Contains(err.Error(), probe.because) {
				t.Fatalf("refused, but not for its reason.\n  want: %s\n  got:  %v", probe.because, err)
			}
		})
	}

	for name, probe := range map[string]struct {
		guard string
		want  playbook.Guard
	}{
		"a rate":            {"guard: {rate: {runs: 2, per: 1m}}\n", playbook.Guard{Rate: &playbook.Rate{Runs: 2, Per: "1m"}}},
		"a rate in hours":   {"guard: {rate: {runs: 1, per: 24h}}\n", playbook.Guard{Rate: &playbook.Rate{Runs: 1, Per: "24h"}}},
		"a wait":            {"guard: {wait: 30m}\n", playbook.Guard{Wait: "30m"}},
		"a wait of nothing": {"guard: {wait: 0s}\n", playbook.Guard{Wait: "0s"}},
		"both":              {"guard: {rate: {runs: 3, per: 2h}, wait: 90s}\n", playbook.Guard{Rate: &playbook.Rate{Runs: 3, Per: "2h"}, Wait: "90s"}},
	} {
		t.Run(name, func(t *testing.T) {
			book, err := playbook.Parse(name, document(probe.guard))
			if err != nil {
				t.Fatalf("%q was refused: %v", probe.guard, err)
			}
			if book.Guard == nil {
				t.Fatal("the guard block was not decoded")
			}
			got := *book.Guard
			if got.Wait != probe.want.Wait || (got.Rate == nil) != (probe.want.Rate == nil) ||
				(got.Rate != nil && *got.Rate != *probe.want.Rate) {
				t.Fatalf("decoded as %+v (rate %+v), want %+v (rate %+v)", got, got.Rate, probe.want, probe.want.Rate)
			}
		})
	}
}

// retrieveShapeReasons is why the published schema refuses each retrieve fixture. A mode,
// an endpoint, a model and a credential are each refused as a key the block does not
// have: they belong to the deployment's collection, and a playbook that could name one
// would stop running against a deployment that arranged retrieval differently.
var retrieveShapeReasons = map[string]string{
	"retrieve-mode.yaml":                  "retrieve/0: additional properties 'mode' not allowed",
	"retrieve-endpoint.yaml":              "retrieve/0: additional properties 'endpoint' not allowed",
	"retrieve-model.yaml":                 "retrieve/0: additional properties 'model' not allowed",
	"retrieve-credential.yaml":            "retrieve/0: additional properties 'credential' not allowed",
	"retrieve-unknown-key.yaml":           "retrieve/0: additional properties 'top_k' not allowed",
	"retrieve-results-above-ceiling.yaml": "retrieve/0/max_results: maximum: got 51, want 50",
	"retrieve-bytes-above-ceiling.yaml":   "retrieve/0/max_bytes: maximum: got 65,537, want 65,536",
	"retrieve-query-and-query-from.yaml":  "retrieve/0: 'oneOf' failed, subschemas 0, 1 matched",
}

// SC-203, the shape half: the retrieve block is probed on what it must refuse, each
// refusal pinned to its reason so one cannot pass on another's — and on what it must
// accept, because a schema that refused the whole block again, which is what the runtime
// core's did, would pass every refusal here.
func TestRetrieveBlockShape(t *testing.T) {
	for name, says := range retrieveShapeReasons {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join("testdata/schema/refused", name)
			_, err := playbook.ParseFile(path)
			if err == nil {
				t.Fatalf("%s was accepted", name)
			}
			if !strings.Contains(err.Error(), says) {
				t.Fatalf("refused, but not for its reason.\n  want: %s\n  got:  %v", says, err)
			}
		})
	}

	book, err := playbook.ParseFile("testdata/schema/accepted/retrieve.yaml")
	if err != nil {
		t.Fatalf("a well-formed retrieve block was refused: %v", err)
	}
	if len(book.Retrieve) != 2 {
		t.Fatalf("the block decoded as %+v", book.Retrieve)
	}
	first, second := book.Retrieve[0], book.Retrieve[1]
	if first.Collection != "runbooks" || first.As != "runbooks.md" ||
		first.Query != "disk full on ${trigger.mountpoint}" || first.QueryFrom != "" {
		t.Errorf("the first retrieval decoded as %+v", first)
	}
	// Declared nothing: the defaults, rather than zero.
	if first.ResultCount() != playbook.DefaultMaxResults || first.ByteBound() != playbook.DefaultMaxBytes {
		t.Errorf("a retrieval declaring no bounds returns %d results in %d bytes",
			first.ResultCount(), first.ByteBound())
	}
	if second.QueryFrom != "facts.txt" || second.Query != "" {
		t.Errorf("the second retrieval decoded as %+v", second)
	}
	if second.ResultCount() != 5 || second.ByteBound() != 4096 {
		t.Errorf("the declared bounds were lost: %d results in %d bytes",
			second.ResultCount(), second.ByteBound())
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
