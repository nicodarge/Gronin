package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/config"
	"github.com/nicodarge/Gronin/runtime/internal/guard"
	"github.com/nicodarge/Gronin/runtime/internal/mcpcatalog"
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
	// Catalog is the MCP servers this deployment provides. A playbook names a server; the
	// catalogue supplies what it is. Nil is treated as an empty catalogue.
	Catalog *mcpcatalog.Catalog

	// AgentExecutable is the CLI this deployment drives, named rather than searched.
	AgentExecutable string
	// AgentEnv is the child's environment, and where the credential travels.
	AgentEnv []string
	// StepEnv is what a gather step runs with — deliberately not this process's own.
	StepEnv []string

	// Guard decides whether a trigger becomes a run and holds the claim while it does.
	// Nil is the single-host file lock of FR-109, which is what a deployment with no
	// coordination backend gets.
	Guard *guard.Guard

	Client         *http.Client
	Log            *slog.Logger
	Now            func() time.Time
	GatherMaxBytes int64

	singleHost sync.Once
	fallback   *guard.Guard
}

func (e *Executor) log() *slog.Logger {
	if e.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return e.Log
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

// Trigger is what brought a run about: its kind, the instant a schedule computed for it,
// and the values a playbook may interpolate.
type Trigger struct {
	Kind record.TriggerKind
	// DueAt is the instant the scheduler fired for, carried for a scheduled trigger
	// alone and never read from this host's clock (FR-129).
	DueAt  time.Time
	Values map[string]string
	// OnWait is told when a trigger that will not come again begins to wait for the run it
	// collided with (FR-110).
	OnWait func(guard.Waiting)
}

// Execute runs one playbook. It returns the recorded run, whatever the outcome: a
// refusal, a timeout and a failure are all runs that happened and are all worth reading.
func (e *Executor) Execute(
	ctx context.Context, book *playbook.Playbook, trigger Trigger,
) (record.Run, error) {
	admitted, err := e.admitTrigger(ctx, book, trigger)
	if err != nil {
		return record.Run{}, err
	}
	// A trigger that waited runs its playbook as the file declares it now (FR-121).
	if admitted.Book != nil {
		book = admitted.Book
	}
	// The run's own context, which the hold cancels if the claim can no longer be proven
	// held. The record keeps being written under the caller's, which that cancellation
	// must not take with it.
	stages, stopRun := context.WithCancel(ctx)
	defer stopRun()
	hold := e.guard().Hold(admitted, stopRun)
	defer hold.Done()

	claimed := claimedOf(admitted, book.Name)
	started, err := e.Manager.Begin(ctx, claimed, trigger.Kind, "")
	if err != nil {
		_ = claimed.Claim.Release(ctx)
		return record.Run{}, err
	}

	outcome := record.Run{Status: record.StatusFailed}
	incomplete := &problems{}
	// Whatever happens below, the run is finished and the working directory goes. The
	// copies into the record have to have happened by then — FR-011 removes the
	// directory and FR-026 requires the inputs to survive it.
	defer func() {
		stoppedByTheGuard(hold, &outcome)
		outcome.EndedAt = e.now()
		if err := e.Manager.Finish(ctx, started, outcome); err != nil {
			// This is the write that makes the run's terminal state durable, and the one
			// place the problems accumulator cannot reach: it runs after the value it
			// would have gone into was returned. Swallowing it left a run whose final
			// state never landed and nothing anywhere said so.
			e.log().Error("the run's terminal state could not be recorded",
				"run", started.ID, "playbook", started.PlaybookName,
				"status", string(outcome.Status), "err", err)
		}
	}()

	ref, err := e.storeResolved(started.ID, book)
	incomplete.note(err)
	outcome.ResolvedPlaybookRef = ref

	gatherErr := e.gather(stages, started, book, incomplete, trigger.Values)
	if gatherErr != nil {
		// FR-010: the run is refused before the stage that costs money, and the inputs
		// it did collect are kept, because they are what explains the refusal.
		outcome.Status = record.StatusRefused
		outcome.Error = gatherErr.Error()
		return e.finished(started.ID, &outcome, incomplete)
	}

	prompt, err := e.prompt(started, book, trigger.Values)
	if err != nil {
		outcome.Status = record.StatusRefused
		outcome.Error = err.Error()
		return e.finished(started.ID, &outcome, incomplete)
	}
	outcome.PromptRef = prompt.ref

	// The same path a replay takes, so a replay cannot end up bounded differently from
	// the run it derives from.
	return e.agentAndSinks(ctx, stages, hold, started, book, prompt.text, &outcome, incomplete, trigger.Values)
}

// admit puts the trigger through the guard, which decides before anything is created
// and before gather runs (FR-102). The identifier is minted first, so the claim the
// guard takes names the run that will hold it.
func (e *Executor) admit(
	ctx context.Context, book *playbook.Playbook, kind record.TriggerKind, dueAt time.Time,
) (*guard.Admitted, error) {
	return e.admitTrigger(ctx, book, Trigger{Kind: kind, DueAt: dueAt})
}

// admitTrigger is admit with everything a trigger carries: its values, kept with a waiting
// trigger's acceptance, and who to tell when it begins to wait.
func (e *Executor) admitTrigger(
	ctx context.Context, book *playbook.Playbook, trigger Trigger,
) (*guard.Admitted, error) {
	id, err := NewRunID()
	if err != nil {
		return nil, err
	}
	return e.guard().Admit(ctx, book, guard.Request{
		RunID: id, Kind: trigger.Kind, DueAt: trigger.DueAt,
		Values: trigger.Values, OnWait: trigger.OnWait,
	})
}

// guard is the stage this executor puts every trigger through. Nil is the single-host
// file lock of FR-109, which is what a deployment with no coordination backend gets.
func (e *Executor) guard() *guard.Guard {
	if e.Guard != nil {
		return e.Guard
	}
	e.singleHost.Do(func() {
		e.fallback = &guard.Guard{
			Coordinator: e.Manager.FileLock(), Store: e.Store,
			Config: guard.DefaultConfig(), Log: e.Log,
		}
	})
	return e.fallback
}

// claimedOf is what Begin needs of an admitted claim.
func claimedOf(admitted *guard.Admitted, playbookName string) Claimed {
	return Claimed{
		Claim: admitted.Claim, RunID: admitted.RunID,
		PlaybookName: playbookName, Reach: admitted.Reach,
		WaitingTriggerID: admitted.WaitingTriggerID,
		WaitedMS:         admitted.Waited.Milliseconds(),
	}
}

// stoppedByTheGuard is FR-105's terminal state. A run the runtime stopped because it
// could no longer prove it held its claim did not fail: recording it as failed sends an
// operator looking for a fault in the playbook.
func stoppedByTheGuard(hold *guard.Hold, outcome *record.Run) {
	if !hold.Stopped() {
		return
	}
	outcome.Status = record.StatusClaimLost
	outcome.Error = joinReasons(outcome.Error, hold.Reason())
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
	trigger map[string]string,
) error {
	steps := make([]gather.Step, 0, len(book.Gather))
	// A step's references resolve through its environment rather than into its text.
	// Substituting a value into a shell line is command injection by construction, and
	// the value is one the deployment holds and the playbook cannot see.
	for _, step := range book.Gather {
		bound, err := e.Config.BindShell(step.Run, trigger)
		if err != nil {
			return fmt.Errorf("gather step %s: %w", step.As, err)
		}
		steps = append(steps, gather.Step{Run: bound.Line, As: step.As, Env: bound.Env})
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
				Sequence: at + 1, Tool: denial.Tool, Asked: string(denial.Input),
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
			// Malformed. The type name survives when there was one — a sink whose value
			// is a scalar refuses as "discord: ..." rather than as an anonymous entry,
			// which is the difference between a refusal an author can act on and one
			// they have to go hunting for.
			declared = append(declared, sink.Declaration{Type: name, Config: nil})
			continue
		}
		declared = append(declared, sink.Declaration{Type: name, Config: value})
	}
	return declared
}

// serversOf is the strict MCP configuration this run is bounded by. A playbook names
// servers; the catalogue supplies what they are.
//
// It iterates book.Agent.MCP and nothing else, which is the whole of what keeps a
// catalogued server that no playbook names from ever reaching the child: unreferenced
// here, it is never written to the file --strict-mcp-config bounds the run to.
func (e *Executor) serversOf(book *playbook.Playbook) (map[string]any, error) {
	servers := map[string]any{}
	if e.Catalog == nil {
		return servers, nil
	}
	interpolate := func(text string) (string, error) { return e.Config.Interpolate(text, nil) }
	for _, name := range book.Agent.MCP {
		resolved, err := e.Catalog.Resolve(name, interpolate)
		if err != nil {
			return nil, err
		}
		servers[name] = resolved
	}
	return servers, nil
}
