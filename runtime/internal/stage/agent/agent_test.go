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
	code := m.Run()
	fakeagent.Cleanup()
	os.Exit(code)
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
