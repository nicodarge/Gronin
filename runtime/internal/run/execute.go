package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/config"
	"github.com/nicodarge/Gronin/runtime/internal/playbook"
	"github.com/nicodarge/Gronin/runtime/internal/record"
	"github.com/nicodarge/Gronin/runtime/internal/sink"
	"github.com/nicodarge/Gronin/runtime/internal/stage/agent"
	"github.com/nicodarge/Gronin/runtime/internal/stage/gather"
)

// Executor runs one playbook end to end: gather, one agent, the sinks, and the record of
// all of it.
//
// Nothing here reaches outside the run's working directory except the sinks. That is
// Principle II's runtime half, and the reason this type does the delivering rather than
// handing the report to anything that could act on it.
type Executor struct {
	Manager *Manager
	Store   *record.Store
	Config  *config.Config

	// AgentExecutable is the CLI this deployment drives, named rather than searched.
	AgentExecutable string
	// AgentEnv is the child's environment, and where the credential travels.
	AgentEnv []string
	// StepEnv is what a gather step runs with — deliberately not this process's own.
	StepEnv []string

	Client         *http.Client
	Now            func() time.Time
	GatherMaxBytes int64
}

// problems collects what could not be written down. A record write that fails silently
// is worse than one that fails loudly: Principle III's whole claim is that a run can be
// explained afterwards, and a run whose record is incomplete cannot be. They were
// swallowed here at first, and it hid two unrelated defects during development.
type problems struct{ list []error }

func (p *problems) note(err error) {
	if err != nil {
		p.list = append(p.list, err)
	}
}

func (p *problems) err() error { return errors.Join(p.list...) }

func (e *Executor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now().UTC()
}

// Execute runs one playbook. It returns the recorded run, whatever the outcome: a
// refusal, a timeout and a failure are all runs that happened and are all worth reading.
func (e *Executor) Execute(
	ctx context.Context, book *playbook.Playbook, kind record.TriggerKind,
	trigger map[string]string,
) (record.Run, error) {
	started, err := e.Manager.Begin(ctx, book.Name, kind, "")
	if err != nil {
		return record.Run{}, err
	}

	outcome := record.Run{Status: record.StatusFailed}
	incomplete := &problems{}
	// Whatever happens below, the run is finished and the working directory goes. The
	// copies into the record have to have happened by then — FR-011 removes the
	// directory and FR-026 requires the inputs to survive it.
	defer func() {
		outcome.EndedAt = e.now()
		if err := e.Manager.Finish(ctx, started, outcome); err != nil {
			_ = err
		}
	}()

	ref, err := e.storeResolved(started.ID, book)
	incomplete.note(err)
	outcome.ResolvedPlaybookRef = ref

	// The sinks are built before the agent runs. A destination this deployment cannot
	// reach is worth finding out about before spending a run on a report nobody gets.
	sinks, problems := sink.Build(declarationsOf(book), sink.BuildOptions{
		Interpolate: func(text string) (string, error) {
			return e.Config.Interpolate(text, trigger)
		},
		Client: e.Client,
	})
	if len(problems) > 0 {
		outcome.Status = record.StatusRefused
		outcome.Error = errors.Join(problems...).Error()
		return e.finished(started.ID, &outcome, incomplete)
	}

	gatherErr := e.gather(ctx, started, book, incomplete)
	if gatherErr != nil {
		// FR-010: the run is refused before the stage that costs money, and the inputs
		// it did collect are kept, because they are what explains the refusal.
		outcome.Status = record.StatusRefused
		outcome.Error = gatherErr.Error()
		return e.finished(started.ID, &outcome, incomplete)
	}

	prompt, err := e.prompt(started, book, trigger)
	if err != nil {
		outcome.Status = record.StatusRefused
		outcome.Error = err.Error()
		return e.finished(started.ID, &outcome, incomplete)
	}
	outcome.PromptRef = prompt.ref

	timeout, err := book.Agent.StageTimeout()
	if err != nil {
		outcome.Status = record.StatusRefused
		outcome.Error = err.Error()
		return e.finished(started.ID, &outcome, incomplete)
	}

	stage, err := agent.Run(ctx, declarationOf(book), agent.Options{
		Executable: e.AgentExecutable,
		WorkDir:    started.WorkDir,
		Prompt:     prompt.text,
		Timeout:    timeout,
		Env:        e.AgentEnv,
		MCPServers: serversOf(book),
	})
	if err != nil {
		outcome.Error = err.Error()
		return e.finished(started.ID, &outcome, incomplete)
	}
	e.recordStage(ctx, started.ID, stage, &outcome, incomplete)

	if stage.TimedOut {
		outcome.Status = record.StatusTimedOut
		outcome.Error = fmt.Sprintf("the agent stage exceeded its %s timeout", timeout)
		return e.finished(started.ID, &outcome, incomplete)
	}

	report, reportErr := stage.Report()
	if reportErr == nil {
		reportErr = agent.ValidateReport(report, book.Agent.OutputSchema)
	}

	delivery := sink.Delivery{PlaybookName: book.Name, RunID: started.ID}
	switch {
	case reportErr != nil:
		// FR-014: failed, and what the sinks are given is the refusal rather than the
		// content the runtime has just decided it cannot read.
		outcome.Status = record.StatusFailed
		outcome.Error = reportErr.Error()
		delivery.Failure = reportErr
	default:
		outcome.Status = record.StatusSucceeded
		delivery.Report = report
		ref, err := e.Store.Blobs().Put(started.ID, "report.json", report)
		incomplete.note(err)
		outcome.ReportRef = ref
	}

	for _, delivered := range sink.DeliverAll(ctx, sinks, delivery) {
		incomplete.note(e.Store.AddSinkOutcome(ctx, started.ID, record.SinkOutcome{
			Sink: delivered.Sink, Status: string(delivered.Status),
			ItemsCreated: delivered.ItemsCreated, ItemsSkipped: delivered.ItemsSkipped,
			Detail: delivered.Detail,
		}))
		if delivered.Status == sink.StatusFailed && outcome.Status == record.StatusSucceeded {
			// The report stands; the delivery did not. FR-025 keeps them apart.
			outcome.Status = record.StatusFailed
			outcome.Error = "a sink failed; the report is in the record and the run can be resumed"
		}
	}

	return e.finished(started.ID, &outcome, incomplete)
}

type resolvedPrompt struct {
	text string
	ref  string
}

func (e *Executor) prompt(
	started *Run, book *playbook.Playbook, trigger map[string]string,
) (resolvedPrompt, error) {
	raw, err := os.ReadFile(book.PromptPath()) //nolint:gosec // the path the playbook names, beside it
	if err != nil {
		return resolvedPrompt{}, fmt.Errorf("reading the prompt: %w", err)
	}
	text, err := e.Config.Interpolate(string(raw), trigger)
	if err != nil {
		return resolvedPrompt{}, fmt.Errorf("resolving the prompt: %w", err)
	}
	ref, err := e.Store.Blobs().Put(started.ID, "prompt.txt", []byte(text))
	if err != nil {
		return resolvedPrompt{}, err
	}
	return resolvedPrompt{text: text, ref: ref}, nil
}

// gather runs the steps and copies what they produced into the record. The copy happens
// here, while the working directory still exists — the record is empty without it, and
// the ordering is the whole of this function.
func (e *Executor) gather(
	ctx context.Context, started *Run, book *playbook.Playbook, incomplete *problems,
) error {
	steps := make([]gather.Step, 0, len(book.Gather))
	for _, step := range book.Gather {
		steps = append(steps, gather.Step{Run: step.Run, As: step.As})
	}

	results, runErr := gather.Run(ctx, steps, gather.Options{
		WorkDir:  started.WorkDir,
		MaxBytes: e.GatherMaxBytes,
		Env:      e.StepEnv,
	})

	for _, result := range results {
		input := record.GatheredInput{
			Name: result.Name, Bytes: result.Bytes,
			Truncated: result.Truncated, ExitCode: result.ExitCode,
		}
		data, err := os.ReadFile(result.Path) //nolint:gosec // written by the step above
		incomplete.note(err)
		if err == nil {
			ref, err := e.Store.Blobs().Put(started.ID, result.Name, data)
			incomplete.note(err)
			input.BlobRef = ref
		}
		if len(result.Stderr) > 0 {
			ref, err := e.Store.Blobs().Put(started.ID, result.Name+".stderr", result.Stderr)
			incomplete.note(err)
			input.StderrRef = ref
		}
		incomplete.note(e.Store.AddGatheredInput(ctx, started.ID, input))
	}
	return runErr
}

func (e *Executor) recordStage(
	ctx context.Context, runID string, stage *agent.Outcome, outcome *record.Run,
	incomplete *problems,
) {
	if stage.Stream == nil {
		return
	}
	if len(stage.Stream.Raw) > 0 {
		_, err := e.Store.Blobs().Put(runID, "transcript.jsonl", stage.Stream.Raw)
		incomplete.note(err)
	}
	if init := stage.Stream.Init; init != nil {
		outcome.AgentSessionID = init.SessionID
		outcome.CredentialSource = init.APIKeySource
	}
	if result := stage.Stream.Result; result != nil {
		outcome.CostUSD = result.TotalCostUSD
		outcome.Tokens = result.Usage.Total()
		for at, denial := range result.PermissionDenials {
			incomplete.note(e.Store.AddRefusedAction(ctx, runID, record.RefusedAction{
				Sequence: at + 1, Tool: denial.Tool, Reason: denial.Reason,
			}))
		}
	}
	for at, call := range stage.Stream.ToolCalls {
		entry := record.ToolCall{
			Sequence: at + 1, Name: call.Name, StartedAt: call.StartedAt, Outcome: call.Outcome,
		}
		if len(call.Input) > 0 {
			ref, err := e.Store.Blobs().Put(runID, fmt.Sprintf("tool-%03d-in.json", at+1), call.Input)
			incomplete.note(err)
			entry.InputRef = ref
		}
		if len(call.Output) > 0 {
			ref, err := e.Store.Blobs().Put(runID, fmt.Sprintf("tool-%03d-out.json", at+1), call.Output)
			incomplete.note(err)
			entry.OutputRef = ref
		}
		incomplete.note(e.Store.AddToolCall(ctx, runID, entry))
	}
}

func (e *Executor) storeResolved(runID string, book *playbook.Playbook) (string, error) {
	document, err := json.MarshalIndent(book, "", "  ")
	if err != nil {
		return "", err
	}
	return e.Store.Blobs().Put(runID, "playbook.json", document)
}

// finished is the summary Execute returns. The authoritative copy is the record, which
// the deferred Finish writes after this returns — so a caller that needs every field
// reads it back by identifier rather than trusting this.
func (e *Executor) finished(
	runID string, outcome *record.Run, incomplete *problems,
) (record.Run, error) {
	outcome.ID = runID
	if err := incomplete.err(); err != nil {
		// A run whose record could not be written is not a run anyone can explain, which
		// is the whole of what Principle III promises. It is reported as failed even
		// when everything else worked.
		if outcome.Status == record.StatusSucceeded {
			outcome.Status = record.StatusFailed
		}
		outcome.Error = joinReasons(outcome.Error, "the record is incomplete: "+err.Error())
	}
	return record.Run{ID: runID, Status: outcome.Status, Error: outcome.Error}, nil
}

func joinReasons(first, second string) string {
	if first == "" {
		return second
	}
	return first + "; " + second
}

func declarationOf(book *playbook.Playbook) agent.Declaration {
	return agent.Declaration{
		Model:        book.Agent.Model,
		Restricted:   book.Agent.IsRestricted(),
		Tools:        book.Agent.Tools,
		MCPServers:   book.Agent.MCP,
		Allow:        book.Agent.Allow,
		OutputSchema: book.Agent.OutputSchema,
	}
}

func declarationsOf(book *playbook.Playbook) []sink.Declaration {
	declared := make([]sink.Declaration, 0, len(book.Sinks))
	for _, one := range book.Sinks {
		name, value, ok := one.Type()
		if !ok {
			declared = append(declared, sink.Declaration{Type: "", Config: map[string]any{}})
			continue
		}
		declared = append(declared, sink.Declaration{Type: name, Config: value})
	}
	return declared
}

// serversOf is the strict MCP configuration this run is bounded by. A playbook names
// servers; the deployment supplies what they are. Until the deployment's own server
// catalogue exists (the load gate refuses an unknown name), a named server contributes
// an empty entry — the file still exists, which is what makes --strict-mcp-config mean
// "these and no others".
func serversOf(book *playbook.Playbook) map[string]any {
	servers := map[string]any{}
	for _, name := range book.Agent.MCP {
		servers[name] = map[string]any{}
	}
	return servers
}
