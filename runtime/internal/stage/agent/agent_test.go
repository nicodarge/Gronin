package agent_test

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/fakeagent"
	"github.com/nicodarge/Gronin/runtime/internal/stage/agent"
)

func TestMain(m *testing.M) {
	os.Exit(runAndCleanUp(m))
}

// runAndCleanUp: see bintest.Main's doc comment for why this defers Cleanup around
// m.Run() in a helper rather than placing it after m.Run() in TestMain itself.
func runAndCleanUp(m *testing.M) (code int) {
	defer fakeagent.Cleanup()
	return m.Run()
}

func options(t *testing.T, mode string) agent.Options {
	t.Helper()
	return agent.Options{
		Executable: fakeagent.Build(t),
		WorkDir:    t.TempDir(),
		Prompt:     "look at the gathered facts",
		Timeout:    20 * time.Second,
		Env:        []string{"PATH=/usr/bin:/bin", fakeagent.ModeVar + "=" + mode},
	}
}

// T018, FR-026: what the terminal event says has to reach the record, or a run's cost is
// unknowable afterwards.
func TestTheTerminalEventCarriesWhatTheRecordNeeds(t *testing.T) {
	outcome, err := agent.Run(t.Context(), agent.Declaration{
		Restricted: true, Tools: []string{"Read"}, Model: "claude-sonnet-5",
	}, options(t, fakeagent.ModeSuccess))
	if err != nil {
		t.Fatal(err)
	}

	if outcome.TimedOut {
		t.Fatal("a successful run was reported as timed out")
	}
	result := outcome.Stream.Result
	if result == nil {
		t.Fatal("no terminal event")
	}
	if result.TotalCostUSD <= 0 {
		t.Errorf("cost = %v", result.TotalCostUSD)
	}
	if result.Usage.Total() <= 0 {
		t.Errorf("usage = %+v", result.Usage)
	}
	if result.NumTurns <= 0 {
		t.Errorf("turns = %d", result.NumTurns)
	}
	if result.StopReason == "" {
		t.Error("no stop reason")
	}
	if len(result.PermissionDenials) == 0 {
		t.Error("no refused actions; the stub reports one and the record wants them")
	}

	init := outcome.Stream.Init
	if init == nil {
		t.Fatal("no receipt")
	}
	if init.SessionID == "" || init.ClaudeCodeVersion == "" {
		t.Errorf("the receipt carries no session or version: %+v", init)
	}
	if init.APIKeySource == "" {
		t.Error("the receipt names no credential source; FR-032 reads it rather than asserting one")
	}
}

// T019, FR-015. The partial transcript is the point: it is the only evidence of what the
// run was doing when it stopped.
func TestATimeoutKillsTheStageAndKeepsWhatItSaid(t *testing.T) {
	opts := options(t, fakeagent.ModeTimeout)
	opts.Timeout = 400 * time.Millisecond

	started := time.Now()
	outcome, err := agent.Run(t.Context(), agent.Declaration{Restricted: true}, opts)
	elapsed := time.Since(started)

	if err != nil {
		t.Fatalf("a timeout is an outcome, not an error: %v", err)
	}
	if !outcome.TimedOut {
		t.Fatal("the run was not marked timed out")
	}
	if elapsed > 20*time.Second {
		t.Fatalf("the stage ran for %v; the timeout did not apply", elapsed)
	}
	if outcome.Stream == nil || outcome.Stream.Init == nil {
		t.Fatal("the receipt the child sent before it was killed was discarded")
	}
	if len(outcome.Stream.Raw) == 0 {
		t.Fatal("no partial transcript survived")
	}
	if outcome.Stream.Result != nil {
		t.Fatal("a killed run reported a terminal event")
	}
}

func TestAFailingRunIsReportedRatherThanRaised(t *testing.T) {
	outcome, err := agent.Run(t.Context(), agent.Declaration{Restricted: true},
		options(t, fakeagent.ModeExitError))
	if err != nil {
		t.Fatalf("a non-zero exit is an outcome: %v", err)
	}
	if outcome.ExitCode == 0 {
		t.Fatal("exit code 0")
	}
	if outcome.Stream.Result == nil || !outcome.Stream.Result.IsError {
		t.Fatal("the failure is not in the terminal event")
	}
}

// research.md §2: the decoder ignores what it has not seen, so a CLI update degrades
// rather than breaks.
func TestTheDecoderSurvivesWhatItHasNotSeen(t *testing.T) {
	for _, mode := range []string{fakeagent.ModeUnknown, fakeagent.ModeMalformed} {
		t.Run(mode, func(t *testing.T) {
			outcome, err := agent.Run(t.Context(), agent.Declaration{Restricted: true},
				options(t, mode))
			if err != nil {
				t.Fatalf("the decoder failed on %s: %v", mode, err)
			}
			if outcome.Stream.Init == nil || outcome.Stream.Result == nil {
				t.Fatalf("%s lost the events the runtime depends on", mode)
			}
		})
	}
}

func TestAMalformedLineIsCountedRatherThanIgnoredSilently(t *testing.T) {
	outcome, err := agent.Run(t.Context(), agent.Declaration{Restricted: true},
		options(t, fakeagent.ModeMalformed))
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Stream.Undecoded == 0 {
		t.Fatal("a line that was not JSON went unnoticed")
	}
}

// The hook the receipt check uses: a refusal from OnEvent stops the run where it stands.
func TestAnEventCallbackCanAbortTheRun(t *testing.T) {
	refusal := errors.New("the receipt does not match the declaration")

	opts := options(t, fakeagent.ModeSuccess)
	opts.OnEvent = func(event agent.Event) error {
		if event.Type == "system" && event.Subtype == "init" {
			return refusal
		}
		return nil
	}

	outcome, err := agent.Run(t.Context(), agent.Declaration{Restricted: true}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(outcome.Aborted, refusal) {
		t.Fatalf("aborted = %v, want the refusal", outcome.Aborted)
	}
	if outcome.Stream.Result != nil {
		t.Fatal("the run reached its terminal event after the abort")
	}
}

// The credential travels in the environment. A command line is journalled and shipped to
// log aggregation, where the value sits for the whole retention window.
func TestTheCredentialIsNotInTheArgumentVector(t *testing.T) {
	const credential = "sk-ant-not-a-real-key-0123456789"

	opts := options(t, fakeagent.ModeSuccess)
	opts.Env = append(opts.Env, "ANTHROPIC_API_KEY="+credential)

	outcome, err := agent.Run(t.Context(), agent.Declaration{Restricted: true}, opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, arg := range outcome.ArgvUsed {
		if strings.Contains(arg, credential) {
			t.Fatalf("the credential is in the argument vector: %v", outcome.ArgvUsed)
		}
	}
	// And it reached the child, which is the half that makes the absence meaningful.
	if outcome.Stream.Init.APIKeySource != "ANTHROPIC_API_KEY" {
		t.Fatalf("the child reports %q as its source", outcome.Stream.Init.APIKeySource)
	}
}

// T020, FR-014.
func TestAReportIsCheckedAgainstTheDeclaredSchema(t *testing.T) {
	schema := map[string]any{
		"type":     "object",
		"required": []any{"findings"},
		"properties": map[string]any{
			"findings": map[string]any{"type": "array"},
		},
	}

	t.Run("a conforming report passes", func(t *testing.T) {
		opts := options(t, fakeagent.ModeSuccess)
		opts.Env = append(opts.Env, fakeagent.ResultVar+`={"findings":[{"id":"one"}]}`)

		outcome, err := agent.Run(t.Context(), agent.Declaration{Restricted: true}, opts)
		if err != nil {
			t.Fatal(err)
		}
		report, err := outcome.Report()
		if err != nil {
			t.Fatal(err)
		}
		if err := agent.ValidateReport(report, schema); err != nil {
			t.Fatalf("a conforming report was refused: %v", err)
		}
	})

	t.Run("a report missing a required field is refused, by name", func(t *testing.T) {
		opts := options(t, fakeagent.ModeSuccess)
		opts.Env = append(opts.Env, fakeagent.ResultVar+`={"summary":"all fine"}`)

		outcome, err := agent.Run(t.Context(), agent.Declaration{Restricted: true}, opts)
		if err != nil {
			t.Fatal(err)
		}
		report, err := outcome.Report()
		if err != nil {
			t.Fatal(err)
		}
		err = agent.ValidateReport(report, schema)
		if !errors.Is(err, agent.ErrReportInvalid) {
			t.Fatalf("err = %v, want ErrReportInvalid", err)
		}
		if !strings.Contains(err.Error(), "findings") {
			t.Fatalf("the refusal does not name the field: %v", err)
		}
	})

	t.Run("an answer that is not JSON at all", func(t *testing.T) {
		err := agent.ValidateReport([]byte(`"just a sentence"`), schema)
		if !errors.Is(err, agent.ErrReportInvalid) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("no declared schema means nothing to check against", func(t *testing.T) {
		if err := agent.ValidateReport([]byte(`{"anything":true}`), nil); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestTheReceiptReportsTheToolSetTheChildReceived(t *testing.T) {
	outcome, err := agent.Run(t.Context(), agent.Declaration{
		Restricted: true, Tools: []string{"Read", "Grep"},
	}, options(t, fakeagent.ModeSuccess))
	if err != nil {
		t.Fatal(err)
	}

	got, err := json.Marshal(outcome.Stream.Init.Tools)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `["Read","Grep"]` {
		t.Fatalf("the receipt reports %s", got)
	}
}

// FR-026 wants every tool call with its input and output. The stream carries them as
// content blocks inside assistant and user messages, matched by identifier.
func TestToolCallsArePairedWithTheirResults(t *testing.T) {
	outcome, err := agent.Run(t.Context(), agent.Declaration{Restricted: true, Tools: []string{"Read"}},
		options(t, fakeagent.ModeSuccess))
	if err != nil {
		t.Fatal(err)
	}

	calls := outcome.Stream.ToolCalls
	if len(calls) != 1 {
		t.Fatalf("%d tool calls recorded: %+v", len(calls), calls)
	}
	if calls[0].Name != "Read" {
		t.Fatalf("name = %q", calls[0].Name)
	}
	if !strings.Contains(string(calls[0].Input), "facts.json") {
		t.Fatalf("input = %s", calls[0].Input)
	}
	if len(calls[0].Output) == 0 {
		t.Fatal("the call has no result; a call without its answer explains nothing")
	}
	if calls[0].Outcome != "ok" {
		t.Fatalf("outcome = %q", calls[0].Outcome)
	}
	if calls[0].StartedAt.IsZero() {
		t.Fatal("the call has no time")
	}
}

// A stream that fails is not a report that fails, and not "no terminal event" either.
// The distinction had no test until a reviewer reverted the code that makes it and
// watched the whole suite stay green.
func TestAStreamThatFailsIsReportedAsSuchRatherThanAsAMissingResult(t *testing.T) {
	outcome, err := agent.Run(t.Context(), agent.Declaration{Restricted: true},
		options(t, fakeagent.ModeOversize))
	if err != nil {
		t.Fatal(err)
	}

	if outcome.DecodeErr == nil {
		t.Fatal("a line past the decoder's bound did not surface as a decode failure")
	}
	if outcome.Aborted != nil {
		t.Fatalf("a stream failure was reported as the bounds refusing the run: %v", outcome.Aborted)
	}
	// The receipt was already out, which is what makes the two distinguishable at all.
	if outcome.Stream.Init == nil {
		t.Fatal("what the child said before the stream failed was discarded")
	}
	if outcome.Stream.Result != nil {
		t.Fatal("the fixture is meant to fail before any terminal event")
	}
}

// FR-018, SC-009. The bound is verified rather than asserted: the child reports what it
// actually received, and a wider set aborts the run before any model output.
func TestAWiderReceiptAbortsTheRunBeforeAnyOutput(t *testing.T) {
	decl := agent.Declaration{Restricted: true, Tools: []string{"Read"}}

	opts := options(t, fakeagent.ModeMismatch)
	opts.OnEvent = agent.CheckReceipt(decl)

	outcome, err := agent.Run(t.Context(), decl, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(outcome.Aborted, agent.ErrReceiptMismatch) {
		t.Fatalf("aborted = %v, want a receipt mismatch", outcome.Aborted)
	}
	if !strings.Contains(outcome.Aborted.Error(), "Bash") {
		t.Fatalf("the refusal does not name what was received: %v", outcome.Aborted)
	}
	if outcome.Stream.Result != nil {
		t.Fatal("the run reached its terminal event; it was meant to stop at the receipt")
	}
}

func TestAMatchingReceiptDoesNotAbort(t *testing.T) {
	decl := agent.Declaration{Restricted: true, Tools: []string{"Read", "Grep"}}

	opts := options(t, fakeagent.ModeSuccess)
	opts.OnEvent = agent.CheckReceipt(decl)

	outcome, err := agent.Run(t.Context(), decl, opts)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Aborted != nil {
		t.Fatalf("a matching receipt aborted the run: %v", outcome.Aborted)
	}
	if outcome.Stream.Result == nil {
		t.Fatal("the run did not finish")
	}
}

// Narrower is not wider. A process that ended up with fewer tools than the playbook
// asked for cannot exceed the declaration, and refusing it would turn a harmless
// difference into an outage.
func TestANarrowerReceiptIsAccepted(t *testing.T) {
	check := agent.CheckReceipt(agent.Declaration{Tools: []string{"Read", "Grep", "Glob"}})

	if err := check(agent.Event{Type: "system", Subtype: "init", Tools: []string{"Read"}}); err != nil {
		t.Fatalf("a narrower receipt was refused: %v", err)
	}
	if err := check(agent.Event{Type: "system", Subtype: "init"}); err != nil {
		t.Fatalf("an empty receipt was refused: %v", err)
	}
}

func TestAnUndeclaredServerInTheReceiptIsRefused(t *testing.T) {
	check := agent.CheckReceipt(agent.Declaration{MCPServers: []string{"grafana"}})

	err := check(agent.Event{
		Type: "system", Subtype: "init",
		MCPServers: []agent.MCPServer{{Name: "grafana"}, {Name: "filesystem"}},
	})
	if !errors.Is(err, agent.ErrReceiptMismatch) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "filesystem") {
		t.Fatalf("the refusal does not name the server: %v", err)
	}
}

// FR-019. A flag an older executable does not recognise is ignored rather than refused,
// so a bound expressed as a flag fails open — which is why the floor exists at all.
func TestTheVersionFloorIsComparedNumerically(t *testing.T) {
	version, err := agent.CheckVersion(t.Context(), fakeagent.Build(t))
	if err != nil {
		t.Fatalf("the stub reports %q and was refused: %v", version, err)
	}
	if version == "" {
		t.Fatal("no version was read")
	}
}

func TestVersionComparisonIsNotLexical(t *testing.T) {
	// The trap: "2.1.9" sorts after "2.1.10" as a string, and is older as a version.
	for _, probe := range []struct {
		version string
		refused bool
	}{
		{"2.1.261", false},
		{"2.1.262", false},
		{"2.2.0", false},
		{"3.0.0", false},
		{"2.1.260", true},
		{"2.1.9", true},
		{"2.0.999", true},
		{"1.9.9", true},
	} {
		err := agent.RefuseBelowFloor(probe.version)
		if probe.refused && err == nil {
			t.Errorf("%s was accepted, and the floor is %s", probe.version, agent.VersionFloor)
		}
		if !probe.refused && err != nil {
			t.Errorf("%s was refused: %v", probe.version, err)
		}
	}
}

// FR-032, FR-033: the source is read off the process's own report rather than asserted,
// and a process that found none refuses to start with the places it looked.
func TestTheCredentialSourceIsReadFromTheProcess(t *testing.T) {
	t.Run("a configured source is reported", func(t *testing.T) {
		source, err := agent.VerifyCredential(t.Context(), fakeagent.Build(t),
			[]string{"PATH=/usr/bin:/bin", "ANTHROPIC_API_KEY=not-a-real-key"}, t.TempDir(), 0)
		if err != nil {
			t.Fatal(err)
		}
		if source != "ANTHROPIC_API_KEY" {
			t.Fatalf("source = %q", source)
		}
	})

	t.Run("another configured source is reported as itself", func(t *testing.T) {
		source, err := agent.VerifyCredential(t.Context(), fakeagent.Build(t),
			[]string{"PATH=/usr/bin:/bin", fakeagent.KeySourceVar + "=keychain"}, t.TempDir(), 0)
		if err != nil {
			t.Fatal(err)
		}
		if source != "keychain" {
			t.Fatalf("source = %q", source)
		}
	})

	// An OAuth session is the ordinary case and the executable names no source for it —
	// measured, apiKeySource reads "none" for that and for no credential alike, so the
	// run authenticating is what separates them.
	t.Run("an unnamed source is reported as such, not refused", func(t *testing.T) {
		source, err := agent.VerifyCredential(t.Context(), fakeagent.Build(t),
			[]string{"PATH=/usr/bin:/bin"}, t.TempDir(), 0)
		if err != nil {
			t.Fatalf("an authorised run with no named source was refused: %v", err)
		}
		if source != agent.SourceNotNamed {
			t.Fatalf("source = %q", source)
		}
	})

	t.Run("nothing logged in refuses and names where it looked", func(t *testing.T) {
		_, err := agent.VerifyCredential(t.Context(), fakeagent.Build(t),
			[]string{"PATH=/usr/bin:/bin", fakeagent.NoCredentialVar + "=1"}, t.TempDir(), 0)
		if !errors.Is(err, agent.ErrNoCredential) {
			t.Fatalf("err = %v, want ErrNoCredential", err)
		}
		for _, where := range []string{"ANTHROPIC_API_KEY", "apiKeyHelper", "keychain"} {
			if !strings.Contains(err.Error(), where) {
				t.Errorf("the refusal does not mention %s: %v", where, err)
			}
		}
	})
}

// The receipt covers the allowlist, which is where an individually named MCP tool is
// declared. A check that read only the built-in names was blind to a child that received
// a different tool inside a server the playbook did allow.
func TestTheReceiptCoversTheAllowlist(t *testing.T) {
	decl := agent.Declaration{
		Restricted: true,
		Tools:      []string{"Read"},
		MCPServers: []string{"grafana"},
		Allow:      []string{"mcp__grafana__query_prometheus", "Read(./**)"},
	}
	check := agent.CheckReceipt(decl)

	if err := check(agent.Event{
		Type: "system", Subtype: "init",
		Tools:      []string{"Read", "mcp__grafana__query_prometheus"},
		MCPServers: []agent.MCPServer{{Name: "grafana"}},
	}); err != nil {
		t.Fatalf("a receipt matching the allowlist was refused: %v", err)
	}

	err := check(agent.Event{
		Type: "system", Subtype: "init",
		Tools:      []string{"Read", "mcp__grafana__update_dashboard"},
		MCPServers: []agent.MCPServer{{Name: "grafana"}},
	})
	if !errors.Is(err, agent.ErrReceiptMismatch) {
		t.Fatalf("a tool nobody allowed, on a server that was allowed, passed: %v", err)
	}
	if !strings.Contains(err.Error(), "update_dashboard") {
		t.Fatalf("the refusal does not name it: %v", err)
	}
}

// And it is producible end to end, which needs the stub to report the allowlist as well.
func TestAnAllowlistMismatchIsProducibleAgainstTheStub(t *testing.T) {
	decl := agent.Declaration{Restricted: true, Tools: []string{"Read"}}

	opts := options(t, fakeagent.ModeSuccess)
	// The process is given more than the declaration says, the way a bounding flag an
	// older executable silently ignores would leave it.
	opts.OnEvent = agent.CheckReceipt(decl)
	wider := agent.Declaration{Restricted: true, Tools: []string{"Read", "Glob"}}

	outcome, err := agent.Run(t.Context(), wider, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(outcome.Aborted, agent.ErrReceiptMismatch) {
		t.Fatalf("aborted = %v; the receipt reported %v", outcome.Aborted, outcome.Stream.Init.Tools)
	}
}

// A scoped file form is a permission on a tool, not a tool, and it never appears in a
// receipt. Folding the whole allowlist in widened what the check accepts — which is the
// direction that hides a mismatch rather than catching one.
func TestAScopedAllowEntryDoesNotWidenWhatTheReceiptAccepts(t *testing.T) {
	check := agent.CheckReceipt(agent.Declaration{
		Tools: []string{"Read"},
		Allow: []string{"Read(./**)", "Glob(./**)"},
	})

	// A child reporting a tool the allowlist merely scoped is still reporting a tool the
	// declaration did not name.
	err := check(agent.Event{Type: "system", Subtype: "init", Tools: []string{"Read", "Glob"}})
	if !errors.Is(err, agent.ErrReceiptMismatch) {
		t.Fatalf("a tool outside the declared set passed because the allowlist mentioned it: %v", err)
	}

	// The case that separates "fold in the whole allowlist" from "fold in what a receipt
	// can report": a bare tool name in the allowlist, which the gate permits, is not a
	// grant of that tool. The tool set is, and this one does not name it.
	bare := agent.CheckReceipt(agent.Declaration{Tools: []string{"Read"}, Allow: []string{"Glob"}})
	if err := bare(agent.Event{
		Type: "system", Subtype: "init", Tools: []string{"Read", "Glob"},
	}); !errors.Is(err, agent.ErrReceiptMismatch) {
		t.Fatalf("an allowlist entry stood in for the tool set: %v", err)
	}

	// And an MCP tool in the allowlist IS foldable, because a connected server's tools
	// do appear in a receipt.
	mcp := agent.CheckReceipt(agent.Declaration{
		Tools: []string{"Read"}, MCPServers: []string{"grafana"},
		Allow: []string{"mcp__grafana__query_prometheus"},
	})
	if err := mcp(agent.Event{
		Type: "system", Subtype: "init",
		Tools:      []string{"Read", "mcp__grafana__query_prometheus"},
		MCPServers: []agent.MCPServer{{Name: "grafana"}},
	}); err != nil {
		t.Fatalf("an allowed MCP tool was refused: %v", err)
	}
}

// StructuredOutput is what the executable adds when BuildArgs passes --json-schema, and
// it does that whenever the playbook declares an output schema — which the playbook
// schema makes required. Measured on the shipped executable: the same invocation reports
// `tools: [Read]` without the flag and `tools: [Read, StructuredOutput]` with it.
//
// Before this, every run against the real agent was refused by its own receipt check, and
// no test saw it because the stub does not add the tool. That is what T063 was for.
func TestStructuredOutputIsAcceptedOnlyWhenTheRunAskedForIt(t *testing.T) {
	declared := agent.Declaration{
		Tools:        []string{"Read"},
		OutputSchema: map[string]any{"type": "object"},
	}
	receipt := agent.Event{
		Type: "system", Subtype: "init",
		Tools: []string{"Read", "StructuredOutput"},
	}
	if err := agent.CheckReceipt(declared)(receipt); err != nil {
		t.Fatalf("a run declaring an output schema was refused for the tool that "+
			"serves it: %v", err)
	}

	// The refusing half, which is what keeps the fold from being a widening: without a
	// declared schema the runtime never passes --json-schema, so a receipt naming the
	// tool is a bound nobody asked for.
	withoutSchema := agent.Declaration{Tools: []string{"Read"}}
	err := agent.CheckReceipt(withoutSchema)(receipt)
	if !errors.Is(err, agent.ErrReceiptMismatch) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "StructuredOutput") {
		t.Fatalf("the refusal does not name the tool: %v", err)
	}
}

// The terminal event as the shipped executable actually writes it. Copied from a real
// run's transcript rather than composed here: every field below was named by guessing
// once, and `permission_denials` was guessed wrong — the struct read `tool` and `reason`
// where the executable writes `tool_name` and `tool_input`, so both decoded empty and
// `gronin show` printed "refused   : " for a refusal that had happened.
//
// The refusal section is the one an operator only ever reads when something went wrong,
// which is the worst place for a field name nobody checked.
func TestTheTerminalEventDecodesAsTheExecutableWritesIt(t *testing.T) {
	const terminal = `{"type":"result","subtype":"success","is_error":false,` +
		`"duration_ms":19159,"num_turns":7,"total_cost_usd":0.037,` +
		`"result":"{\"findings\":[]}","structured_output":{"findings":[]},` +
		`"usage":{"input_tokens":11,"output_tokens":22},` +
		`"permission_denials":[{"tool_name":"Glob","tool_use_id":"toolu_01",` +
		`"tool_input":{"pattern":"**/README.md","path":"/"}}]}`

	stream, err := agent.Decode(strings.NewReader(terminal), nil)
	if err != nil {
		t.Fatal(err)
	}
	if stream.Result == nil {
		t.Fatal("no terminal event was decoded")
	}

	denials := stream.Result.PermissionDenials
	if len(denials) != 1 {
		t.Fatalf("denials = %+v", denials)
	}
	if denials[0].Tool != "Glob" {
		t.Errorf("the refused tool decoded as %q", denials[0].Tool)
	}
	if !strings.Contains(string(denials[0].Input), "README.md") {
		t.Errorf("what the agent asked for decoded as %q", denials[0].Input)
	}

	// The answer is taken from structured_output. `result` holds the same answer as a
	// JSON string, and validating that against an object schema is what failed every real
	// run before this.
	outcome := &agent.Outcome{Stream: stream}
	report, err := outcome.Report()
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.ValidateReport(report, map[string]any{"type": "object"}); err != nil {
		t.Errorf("the report does not satisfy an object schema: %v", err)
	}
}

// A turn can fail without the credential being the reason, and is_error is set either
// way. Refusing a rate limit or a bad minute upstream with "found no credential" sends
// an operator to check something that is fine — so the status beside it decides.
func TestAFailedTurnIsNotAlwaysAMissingCredential(t *testing.T) {
	_, err := agent.VerifyCredential(t.Context(), fakeagent.Build(t),
		[]string{"PATH=/usr/bin:/bin", fakeagent.APIErrorStatusVar + "=529"}, t.TempDir(), 0)

	if errors.Is(err, agent.ErrNoCredential) {
		t.Fatalf("an upstream failure was reported as a missing credential: %v", err)
	}
	if !errors.Is(err, agent.ErrStartupProbeFailed) {
		t.Fatalf("err = %v, want ErrStartupProbeFailed", err)
	}
	if !strings.Contains(err.Error(), "529") {
		t.Errorf("the refusal does not name what came back: %v", err)
	}

	// A credential the API refused is still a credential problem, and says so.
	_, err = agent.VerifyCredential(t.Context(), fakeagent.Build(t),
		[]string{"PATH=/usr/bin:/bin", fakeagent.APIErrorStatusVar + "=401"}, t.TempDir(), 0)
	if !errors.Is(err, agent.ErrNoCredential) {
		t.Fatalf("a 401 was not read as a credential problem: %v", err)
	}
}

// The probe's own bound is shorter than the executable's retry ladder, so the commonest
// real failure — a credential the API keeps refusing — arrives as a timeout rather than
// as a classified terminal event. Saying "no terminal event" for that names neither the
// bound nor the likeliest cause, which is what an operator needs at that moment.
func TestAProbeThatNeverFinishesNamesTheBoundAndWhereToLook(t *testing.T) {
	_, err := agent.VerifyCredential(t.Context(), fakeagent.Build(t),
		[]string{"PATH=/usr/bin:/bin", fakeagent.ModeVar + "=" + fakeagent.ModeTimeout},
		t.TempDir(), 300*time.Millisecond)

	if !errors.Is(err, agent.ErrStartupProbeFailed) {
		t.Fatalf("err = %v, want ErrStartupProbeFailed", err)
	}
	if strings.Contains(err.Error(), "no terminal event") {
		t.Errorf("a timeout was reported as a missing event: %v", err)
	}
	for _, wanted := range []string{"ANTHROPIC_API_KEY", "keychain"} {
		if !strings.Contains(err.Error(), wanted) {
			t.Errorf("the refusal does not say where a credential comes from: %v", err)
		}
	}
}
