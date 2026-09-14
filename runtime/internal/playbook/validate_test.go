package playbook_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/playbook"
)

// deployment is what the corpora are judged against: what a deployment provides is part
// of whether a playbook is safe, not a property of the playbook alone.
func deployment() playbook.Deployment {
	return playbook.Deployment{
		MCPServers:    []string{"grafana"},
		SinkTypes:     []string{"discord", "slack", "github"},
		CreatingSinks: []string{"github"},
		// One collection, for the same reason: a retrieval naming anything else is
		// refused, so a corpus that assumes a collection exists should have to say so.
		Collections: []string{"runbooks"},
		// The keys the corpora reference. Listed rather than derived: what a deployment
		// holds is half of whether a playbook is accepted, so a corpus that assumes a key
		// exists should have to say so.
		ConfigKeys: []string{
			"audit_repo", "github_token", "kb_collection", "loft", "ops_channel",
			"ops_webhook", "fleet", "w",
		},
		// T017's corpus is judged against one configured source, so the
		// unconfigured-source fixture can be pinned to naming it.
		Sources: []string{"alerts"},
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
		"missing-prompt-file.yaml":           {"agent.prompt_file", "is not there"},
		"quoted-reference-in-gather.yaml":    {"gather[0].run", "sits inside quotes"},
		"label-with-a-comma.yaml":            {"sinks[0].github.label", "holds a comma"},
		"empty-label.yaml":                   {"sinks[0].github.label", "is empty"},
		"label-from-the-trigger.yaml":        {"sinks[0].github.label", "resolves through the trigger"},
		"unconfigured-reference.yaml":        {"sinks[0].discord.webhook", "is not configured"},
		"retrieve-undeclared-collection.yaml": {
			"retrieve[0].collection", "not a collection this deployment declares"},
		"retrieve-undeclared-gathered-input.yaml": {
			"retrieve[0].query_from", "not the name of a gather step's output"},
		"retrieve-colliding-name.yaml": {
			"retrieve[0].as", "already written by a gather step"},
		// Refused by the published schema before the semantic gate sees it, which is the
		// same rule at an earlier layer. The gate's own version is tested below, against
		// a document the schema never reads.
		"guard-unknown-key.yaml": {"guard", "additional properties 'lock' not allowed"},

		// T017's webhook corpus. Each is refused for its own rule, wherever the payload
		// could otherwise steer the run.
		"webhook-prompt-reference.yaml": {
			"agent.prompt_file", "references ${trigger."},
		"webhook-sink-reference.yaml": {
			"sinks[0].discord.webhook", "references ${trigger."},
		"webhook-unconfigured-source.yaml": {
			"trigger.source", "not a source this deployment configures"},
		"webhook-undeclared-gather-reference.yaml": {
			"gather[0].run", "is not a declared value"},
		"webhook-pattern-does-not-compile.yaml": {
			"trigger.values.alertname.pattern", "does not compile"},
		"webhook-data-file-name.yaml": {
			"gather[0].as", "is the data file"},
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
			// The unconfigured-source refusal names what IS configured, not only what
			// is not — an author fixing it needs to know which source names to pick
			// from.
			if name == "webhook-unconfigured-source.yaml" {
				var namesConfigured bool
				for _, problem := range problems {
					if problem.Field == want.field && strings.Contains(problem.Accepted, "alerts") {
						namesConfigured = true
					}
				}
				if !namesConfigured {
					t.Errorf("the refusal does not name the configured source: %v", problems)
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

// SC-203, the gate's half. FR-204's refusals are the ones JSON Schema cannot express —
// whether a collection is declared, whether a query_from names a gather step, whether an
// `as` collides — so each is probed here, by constructing the playbook, against a
// deployment declaring one collection.
//
// The last row is the block that is well-formed and refused all the same, because no
// stage applies it yet: an accepted block would arm a playbook whose retrieval never
// happens, and the agent would run without the context its prompt was written around.
// Lifting that is one line of this table.
func TestRetrieveBlockRefusals(t *testing.T) {
	gather := []playbook.Step{{Run: "df -h /var", As: "facts.txt"}}
	for name, probe := range map[string]struct {
		retrieve []playbook.Retrieval
		field    string
		says     string
	}{
		"a collection this deployment does not declare": {
			retrieve: []playbook.Retrieval{{Collection: "incidents", As: "out.md", Query: "disk"}},
			field:    "retrieve[0].collection",
			says:     `"incidents" is not a collection this deployment declares`,
		},
		"a query_from no gather step writes": {
			retrieve: []playbook.Retrieval{{Collection: "runbooks", As: "out.md", QueryFrom: "inventory.json"}},
			field:    "retrieve[0].query_from",
			says:     `"inventory.json" is not the name of a gather step's output`,
		},
		"a results name a gather step writes": {
			retrieve: []playbook.Retrieval{{Collection: "runbooks", As: "facts.txt", Query: "disk"}},
			field:    "retrieve[0].as",
			says:     `"facts.txt" is already written by a gather step`,
		},
		"a results name another retrieval writes": {
			retrieve: []playbook.Retrieval{
				{Collection: "runbooks", As: "out.md", Query: "disk"},
				{Collection: "runbooks", As: "out.md", Query: "cert"},
			},
			field: "retrieve[1].as",
			says:  `"out.md" is already written by retrieve[0]`,
		},
		"a result count above the ceiling": {
			retrieve: []playbook.Retrieval{{Collection: "runbooks", As: "out.md", Query: "disk", MaxResults: 51}},
			field:    "retrieve[0].max_results",
			says:     "a retrieval returns at most 50",
		},
		"a byte bound above the ceiling": {
			retrieve: []playbook.Retrieval{{Collection: "runbooks", As: "out.md", Query: "disk", MaxBytes: 65537}},
			field:    "retrieve[0].max_bytes",
			says:     "a results file is at most 65536",
		},
		"a bare reference in the query": {
			retrieve: []playbook.Retrieval{{Collection: "runbooks", As: "out.md", Query: "disk on ${host}"}},
			field:    "retrieve[0].query",
			says:     "${host} does not name its source",
		},
		"a ${config.x} the deployment does not hold": {
			retrieve: []playbook.Retrieval{{Collection: "runbooks", As: "out.md", Query: "${config.nowhere}"}},
			field:    "retrieve[0].query",
			says:     "${config.nowhere} is not configured",
		},
	} {
		t.Run(name, func(t *testing.T) {
			problems := playbook.Validate(
				&playbook.Playbook{Name: "p", Gather: gather, Retrieve: probe.retrieve}, deployment())
			if !refusedAs(problems, probe.field, probe.says) {
				t.Fatalf("not refused for its reason.\n  want %s to say %q\n  got:  %v",
					probe.field, probe.says, problems)
			}
		})
	}

	// The block with nothing wrong with it, accepted with no problem at all. Before the
	// stage existed every retrieve block was refused, so this is the case that can fail.
	valid := []playbook.Retrieval{
		{Collection: "runbooks", As: "runbooks.md", Query: "disk full on ${trigger.mountpoint}"},
		{Collection: "runbooks", As: "by-facts.md", QueryFrom: "facts.txt", MaxResults: 5, MaxBytes: 4096},
	}
	problems := playbook.Validate(
		&playbook.Playbook{Name: "p", Gather: gather, Retrieve: valid}, deployment())
	for _, problem := range problems {
		t.Errorf("a well-formed retrieve block was refused for %s", problem.Error())
	}
}

func refusedAs(problems []playbook.Problem, field, says string) bool {
	for _, problem := range problems {
		if problem.Field == field && strings.Contains(problem.Found, says) {
			return true
		}
	}
	return false
}

// SC-109, the gate's half. The schema accepts the shape of every key the guard contract
// defines; a key whose mechanism has not landed is refused here, by name. The table says
// which are applied, so lifting one is a change to one line of it — and a refusal of the
// block as a whole, which the runtime core already produced, cannot pass for this.
func TestGuardBlockKeysAreRefusedUntilApplied(t *testing.T) {
	applied := map[string]bool{
		"guard.rate": true,
		"guard.wait": true,
	}
	declaring := map[string]*playbook.Guard{
		"guard.rate": {Rate: &playbook.Rate{Runs: 2, Per: "1h"}},
		"guard.wait": {Wait: "0s"},
	}
	if len(declaring) != len(applied) {
		t.Fatalf("%d keys are declared and %d are in the table", len(declaring), len(applied))
	}

	notApplied := func(problems []playbook.Problem, field string) bool {
		for _, problem := range problems {
			if problem.Field == field && strings.Contains(problem.Found, "does not apply it yet") {
				return true
			}
		}
		return false
	}

	for field, guard := range declaring {
		t.Run(field, func(t *testing.T) {
			problems := playbook.Validate(&playbook.Playbook{Name: "p", Guard: guard}, deployment())
			refused := notApplied(problems, field)
			switch {
			case applied[field] && refused:
				t.Fatalf("%s is applied and was refused: %v", field, problems)
			case !applied[field] && !refused:
				t.Fatalf("%s is not applied yet and was accepted: %v", field, problems)
			}
			for other := range applied {
				if other != field && notApplied(problems, other) {
					t.Fatalf("declaring %s alone was refused as %s", field, other)
				}
			}
		})
	}

	// The block with nothing in it declares no bound, and is not refused for being there.
	problems := playbook.Validate(&playbook.Playbook{Name: "p", Guard: &playbook.Guard{}}, deployment())
	for _, problem := range problems {
		if strings.HasPrefix(problem.Field, "guard") {
			t.Fatalf("an empty guard block was refused: %v", problem)
		}
	}
}

// T031 lifted the hold T014 put on a webhook trigger: a well-formed one, naming a source
// this deployment configures and declaring no values, is now accepted rather than held.
// T016's mutant "the gate lets a webhook trigger through before its rules exist" lost its
// target when the hold came off, and is replaced here by its opposite: a mutant
// reinstating the hold would refuse this book, which this test would then catch.
func TestAWebhookTriggerIsHeld(t *testing.T) {
	held := func(problems []playbook.Problem) bool {
		for _, problem := range problems {
			if problem.Field == "trigger.type" && strings.Contains(problem.Found, "does not apply its rules yet") {
				return true
			}
		}
		return false
	}

	book := &playbook.Playbook{Name: "p", Trigger: playbook.Trigger{Type: "webhook", Source: "alerts"}}
	if problems := playbook.Validate(book, deployment()); held(problems) || len(problems) != 0 {
		t.Fatalf("a webhook trigger that passes every rule was refused: %v", problems)
	}

	manual := &playbook.Playbook{Name: "p", Trigger: playbook.Trigger{Type: "manual"}}
	if problems := playbook.Validate(manual, deployment()); held(problems) {
		t.Fatalf("a manual trigger was held as if it were a webhook: %v", problems)
	}
}

// SC-315. The schema already refuses a value missing its pattern or its length
// (webhook-value-without-pattern.yaml, webhook-value-without-max-length.yaml under
// testdata/schema/refused); this is the gate's own copy of the same rule, on typed
// playbooks the schema never reads — as TestGuardBlockKeysAreRefusedUntilApplied is for
// the guard block's own keys — so a playbook built directly rather than parsed is held
// to it too.
func TestTheGateRefusesAWebhookValueTheSchemaWouldHave(t *testing.T) {
	for name, value := range map[string]playbook.TriggerValue{
		"no pattern":    {At: "/alertname", MaxLength: 80},
		"no max_length": {At: "/alertname", Pattern: ".+"},
	} {
		t.Run(name, func(t *testing.T) {
			book := &playbook.Playbook{
				Name: "p",
				Trigger: playbook.Trigger{
					Type: "webhook", Source: "alerts",
					Values: map[string]playbook.TriggerValue{"alertname": value},
				},
			}
			problems := playbook.Validate(book, deployment())
			if len(problems) == 0 {
				t.Fatal("a value missing a required field was accepted")
			}
			var found bool
			for _, problem := range problems {
				if strings.HasPrefix(problem.Field, "trigger.values.alertname") &&
					strings.Contains(problem.Found, "missing") {
					found = true
				}
			}
			if !found {
				t.Errorf("not refused for its reason: %v", problems)
			}
		})
	}
}

// SC-315: the prompt and the sink checks report every offending reference, the way the
// gather check already does — not only the first — so an author fixing one does not get
// sent back for a second round trip over one this gate already saw.
func TestEveryTriggerReferenceInAPromptOrSinkIsReported(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "prompt.md"),
		[]byte("Report on ${trigger.alertname} and ${trigger.severity}."), 0o600); err != nil {
		t.Fatal(err)
	}
	book := &playbook.Playbook{
		Name: "p", Path: filepath.Join(dir, "p.yaml"),
		Trigger: playbook.Trigger{Type: "webhook", Source: "alerts"},
		Agent: playbook.Agent{
			PromptFile: "prompt.md", Model: "m", OutputSchema: map[string]any{"type": "object"},
		},
		Sinks: []playbook.Sink{
			{"discord": map[string]any{"webhook": "${trigger.a} and ${trigger.b}"}},
		},
	}

	problems := playbook.Validate(book, deployment())

	var promptRefs, sinkRefs int
	for _, problem := range problems {
		switch problem.Field {
		case "agent.prompt_file":
			promptRefs++
		case "sinks[0].discord.webhook":
			sinkRefs++
		}
	}
	if promptRefs != 2 {
		t.Fatalf("the prompt's two references produced %d problems, want 2: %v", promptRefs, problems)
	}
	if sinkRefs != 2 {
		t.Fatalf("the sink's two references produced %d problems, want 2: %v", sinkRefs, problems)
	}
}

// SC-109 for wait, through both layers a loaded playbook passes: the schema and the gate.
// A key the runtime implements loads; one it does not is still refused beside it. Observing
// a refusal alone would pass against the runtime core, which refused the whole block.
func TestGuardBlockWaitLoadsAndAnUnknownKeyDoesNot(t *testing.T) {
	document := func(guard string) []byte {
		return []byte("name: drift-check\ntrigger: {type: manual}\n" + guard +
			"agent: {model: m, prompt_file: p.md, output_schema: {type: object}}\n" +
			"sinks: [{discord: {channel: \"#ops\"}}]\n")
	}

	book, err := playbook.Parse("wait.yaml", document("guard: {wait: 5m}\n"))
	if err != nil {
		t.Fatalf("a guard block declaring wait was refused by the schema: %v", err)
	}
	for _, problem := range playbook.Validate(book, deployment()) {
		if strings.HasPrefix(problem.Field, "guard") {
			t.Fatalf("a guard block declaring wait was refused by the gate: %v", problem)
		}
	}
	if waited, err := book.WaitFor(); err != nil || waited != 5*time.Minute {
		t.Fatalf("wait = %s, err = %v, want the 5m declared", waited, err)
	}

	if _, err := playbook.Parse("unknown.yaml", document("guard: {wait: 5m, queue: 3}\n")); err == nil ||
		!strings.Contains(err.Error(), "additional properties 'queue' not allowed") {
		t.Fatalf("an unknown guard key beside wait was not refused for itself: %v", err)
	}

	// No block, or a block that does not say, waits for research.md §3's default.
	for name, guard := range map[string]*playbook.Guard{"no block": nil, "no wait": {}} {
		if waited, err := (&playbook.Playbook{Guard: guard}).WaitFor(); err != nil || waited != 30*time.Minute {
			t.Fatalf("%s: wait = %s, err = %v, want 30m", name, waited, err)
		}
	}
}

// SC-109 for rate, through both layers: the schema and the gate. Loading is not the same
// as accepting the shape — `per` in seconds is refused by the schema whether or not the
// gate has lifted the key, so lifting `guard.rate` here must not have moved that refusal.
func TestGuardBlockRateLoadsAndAPerInSecondsDoesNot(t *testing.T) {
	document := func(guard string) []byte {
		return []byte("name: drift-check\ntrigger: {type: manual}\n" + guard +
			"agent: {model: m, prompt_file: p.md, output_schema: {type: object}}\n" +
			"sinks: [{discord: {channel: \"#ops\"}}]\n")
	}

	book, err := playbook.Parse("rate.yaml", document("guard: {rate: {runs: 2, per: 1h}}\n"))
	if err != nil {
		t.Fatalf("a guard block declaring rate was refused by the schema: %v", err)
	}
	for _, problem := range playbook.Validate(book, deployment()) {
		if strings.HasPrefix(problem.Field, "guard") {
			t.Fatalf("a guard block declaring rate was refused by the gate: %v", problem)
		}
	}
	if book.Guard.Rate.Runs != 2 || book.Guard.Rate.Per != "1h" {
		t.Fatalf("rate = %+v, want the 2 runs per 1h declared", book.Guard.Rate)
	}

	if _, err := playbook.Parse("seconds.yaml", document("guard: {rate: {runs: 2, per: 30s}}\n")); err == nil {
		t.Fatal("guard.rate.per in seconds was accepted by the schema")
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

// The form that got past the rule: a trailing separator names a server with nothing
// inside it, and splitting on the separator gives three parts, which read as a tool.
func TestAnEntryNamingAServerWithNothingInsideItIsRefused(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "prompt.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	refusals := func(t *testing.T, field, entry string) []playbook.Problem {
		t.Helper()
		path := filepath.Join(dir, "mcp.yaml")
		document := "name: mcp\ntrigger: {type: manual}\nagent:\n  model: m\n  prompt_file: prompt.md\n" +
			"  mcp: [grafana]\n  " + field + ": [\"" + entry + "\"]\n  output_schema: {type: object}\n" +
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

	for _, field := range []string{"allow", "tools"} {
		for _, entry := range []string{"mcp__grafana", "mcp__grafana__", "mcp__grafana____"} {
			problems := refusals(t, field, entry)
			if len(problems) == 0 {
				t.Errorf("agent.%s: %q was accepted", field, entry)
				continue
			}
			if !strings.Contains(problems[0].Found, "whole MCP server") {
				t.Errorf("agent.%s: %q was refused for another reason: %s",
					field, entry, problems[0].Error())
			}
		}
	}
}

// The prompt body is the string the runtime actually interpolates against the trigger
// payload. The walk covered the prompt's path and not its content, so a bare reference
// there was caught at run time — after the gather steps had cost money.
func TestABareReferenceInThePromptIsRefusedAtTheGate(t *testing.T) {
	dir := t.TempDir()
	write := func(t *testing.T, prompt string) []playbook.Problem {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "prompt.md"), []byte(prompt), 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "p.yaml")
		document := "name: p\ntrigger: {type: manual}\nagent:\n  model: m\n  prompt_file: prompt.md\n" +
			"  tools: [Read]\n  output_schema: {type: object}\n" +
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

	problems := write(t, "Report on ${alert_name} and say what changed.")
	if len(problems) == 0 {
		t.Fatal("a bare reference in the prompt was accepted")
	}
	if !strings.Contains(problems[0].Found, "does not name its source") {
		t.Fatalf("refused for another reason: %s", problems[0].Error())
	}
	if !strings.Contains(problems[0].Field, "prompt") {
		t.Fatalf("the refusal does not point at the prompt: %s", problems[0].Field)
	}

	if problems := write(t, "Report on ${trigger.alert_name} and ${config.fleet}."); len(problems) != 0 {
		t.Fatalf("a namespaced reference was refused: %s", problems[0].Error())
	}
}

// A prompt that is too large, or is not a file at all, is a different mistake from one
// that is not there — and a refusal saying only "not readable" hides which.
func TestAPromptIsBoundedAndTheRefusalSaysWhy(t *testing.T) {
	dir := t.TempDir()
	write := func(t *testing.T, prepare func(path string)) []playbook.Problem {
		t.Helper()
		prepare(filepath.Join(dir, "prompt.md"))
		path := filepath.Join(dir, "p.yaml")
		document := "name: p\ntrigger: {type: manual}\nagent:\n  model: m\n  prompt_file: prompt.md\n" +
			"  tools: [Read]\n  output_schema: {type: object}\n" +
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

	problems := write(t, func(path string) {
		big := make([]byte, playbook.MaxPromptBytes+1)
		for at := range big {
			big[at] = 'x'
		}
		if err := os.WriteFile(path, big, 0o600); err != nil {
			t.Fatal(err)
		}
	})
	if len(problems) == 0 {
		t.Fatal("a prompt past the bound was accepted")
	}
	if !strings.Contains(problems[0].Found, "at most") {
		t.Fatalf("the refusal does not say it is too large: %s", problems[0].Error())
	}

	if problems := write(t, func(path string) {
		_ = os.Remove(path)
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}); len(problems) == 0 || !strings.Contains(problems[0].Found, "regular file") {
		t.Fatalf("a directory as a prompt was accepted or misdescribed: %v", problems)
	}
}
