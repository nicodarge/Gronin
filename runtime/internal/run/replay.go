package run

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nicodarge/Gronin/runtime/internal/playbook"
	"github.com/nicodarge/Gronin/runtime/internal/record"
	"github.com/nicodarge/Gronin/runtime/internal/sink"
	"github.com/nicodarge/Gronin/runtime/internal/stage/agent"
)

// ErrNotReplayable is returned for a run whose record does not hold what a replay needs.
var ErrNotReplayable = errors.New("this run cannot be replayed from its record")

// ErrNotResumable is returned for a run that produced no report to deliver.
var ErrNotResumable = errors.New("this run has no recorded report to resume from")

// Replay re-runs the agent stage against a recorded run's inputs.
//
// Gather does not re-execute and no trigger fires: the point of a replay is to change
// the prompt, or the model, and see what a different agent does with the SAME inputs.
// Re-gathering would change the question being asked, and firing the trigger would make
// a diagnostic tool a cause of load.
//
// It is a distinct run linked to the one it derives from, because it costs money and a
// record that hid that would understate what a deployment spent.
func (e *Executor) Replay(
	ctx context.Context, parentID string, book *playbook.Playbook,
) (record.Run, error) {
	parent, err := e.Store.GetRun(ctx, parentID)
	if err != nil {
		return record.Run{}, err
	}
	inputs, err := e.Store.GatheredInputs(ctx, parentID)
	if err != nil {
		return record.Run{}, err
	}
	if parent.PromptRef == "" {
		return record.Run{}, fmt.Errorf("%w: it recorded no prompt", ErrNotReplayable)
	}
	prompt, err := e.Store.Blobs().Get(parent.PromptRef)
	if err != nil {
		return record.Run{}, fmt.Errorf("%w: %w", ErrNotReplayable, err)
	}

	started, err := e.Manager.Begin(ctx, book.Name, record.TriggerReplay, parentID)
	if err != nil {
		return record.Run{}, err
	}

	outcome := record.Run{Status: record.StatusFailed, ParentRunID: parentID}
	incomplete := &problems{}
	defer func() {
		outcome.EndedAt = e.now()
		if err := e.Manager.Finish(ctx, started, outcome); err != nil {
			e.log().Error("the replay's terminal state could not be recorded",
				"run", started.ID, "parent", parentID, "err", err)
		}
	}()

	// The recorded inputs are put back where the agent expects them. Restoring rather
	// than re-gathering is the whole distinction: same inputs, possibly a different
	// prompt.
	for _, input := range inputs {
		if input.BlobRef == "" {
			continue
		}
		data, err := e.Store.Blobs().Get(input.BlobRef)
		if err != nil {
			outcome.Status = record.StatusRefused
			outcome.Error = fmt.Errorf("%w: input %q: %w", ErrNotReplayable, input.Name, err).Error()
			return e.finished(started.ID, &outcome, incomplete)
		}
		path := filepath.Join(started.WorkDir, filepath.Base(input.Name))
		if err := os.WriteFile(path, data, 0o600); err != nil {
			outcome.Status = record.StatusRefused
			outcome.Error = err.Error()
			return e.finished(started.ID, &outcome, incomplete)
		}
		incomplete.note(e.Store.AddGatheredInput(ctx, started.ID, input))
	}

	ref, err := e.Store.Blobs().Put(started.ID, "prompt.txt", prompt)
	incomplete.note(err)
	outcome.PromptRef = ref
	if resolved, err := e.storeResolved(started.ID, book); err == nil {
		outcome.ResolvedPlaybookRef = resolved
	}

	return e.agentAndSinks(ctx, started, book, string(prompt), &outcome, incomplete, nil)
}

// Resume re-runs only the sinks, against the report a run already produced.
//
// The agent does not run, so a resume costs nothing — which is the point. A sink that
// was down when the run finished should not cost a second agent run to deliver to.
func (e *Executor) Resume(
	ctx context.Context, parentID string, book *playbook.Playbook,
) (record.Run, error) {
	parent, err := e.Store.GetRun(ctx, parentID)
	if err != nil {
		return record.Run{}, err
	}
	if parent.ReportRef == "" {
		return record.Run{}, fmt.Errorf("%w: it produced none", ErrNotResumable)
	}
	report, err := e.Store.Blobs().Get(parent.ReportRef)
	if err != nil {
		return record.Run{}, fmt.Errorf("%w: %w", ErrNotResumable, err)
	}

	started, err := e.Manager.Begin(ctx, book.Name, record.TriggerResume, parentID)
	if err != nil {
		return record.Run{}, err
	}

	outcome := record.Run{Status: record.StatusFailed, ParentRunID: parentID}
	incomplete := &problems{}
	defer func() {
		outcome.EndedAt = e.now()
		if err := e.Manager.Finish(ctx, started, outcome); err != nil {
			e.log().Error("the resume's terminal state could not be recorded",
				"run", started.ID, "parent", parentID, "err", err)
		}
	}()

	sinks, refusals := sink.Build(declarationsOf(book), sink.BuildOptions{
		Interpolate: func(text string) (string, error) { return e.Config.Interpolate(text, nil) },
		Client:      e.Client,
	})
	if len(refusals) > 0 {
		outcome.Status = record.StatusRefused
		outcome.Error = errors.Join(refusals...).Error()
		return e.finished(started.ID, &outcome, incomplete)
	}

	ref, err := e.Store.Blobs().Put(started.ID, "report.json", report)
	incomplete.note(err)
	outcome.ReportRef = ref

	// No cost and no tokens are written: the agent did not run, and a resume that
	// reported a cost would double-count what the original already recorded.
	outcome.Status = record.StatusSucceeded
	e.deliver(ctx, started.ID, sinks, sink.Delivery{
		PlaybookName: book.Name, RunID: started.ID, Report: report,
	}, &outcome, incomplete)

	return e.finished(started.ID, &outcome, incomplete)
}

// agentAndSinks is the half of a run that a replay repeats: one agent stage, the schema
// check, and the sinks. Shared with Execute so a replay cannot drift into being bounded
// differently from the run it derives from.
func (e *Executor) agentAndSinks(
	ctx context.Context, started *Run, book *playbook.Playbook, prompt string,
	outcome *record.Run, incomplete *problems, trigger map[string]string,
) (record.Run, error) {
	sinks, refusals := sink.Build(declarationsOf(book), sink.BuildOptions{
		Interpolate: func(text string) (string, error) { return e.Config.Interpolate(text, trigger) },
		Client:      e.Client,
	})
	if len(refusals) > 0 {
		outcome.Status = record.StatusRefused
		outcome.Error = errors.Join(refusals...).Error()
		return e.finished(started.ID, outcome, incomplete)
	}

	timeout, err := book.Agent.StageTimeout()
	if err != nil {
		outcome.Status = record.StatusRefused
		outcome.Error = err.Error()
		return e.finished(started.ID, outcome, incomplete)
	}

	declaration := declarationOf(book)
	stage, err := agent.Run(ctx, declaration, agent.Options{
		Executable: e.AgentExecutable,
		WorkDir:    started.WorkDir,
		Prompt:     prompt,
		Timeout:    timeout,
		Env:        e.AgentEnv,
		MCPServers: serversOf(book),
		OnEvent:    agent.CheckReceipt(declaration),
	})
	if err != nil {
		outcome.Error = err.Error()
		return e.finished(started.ID, outcome, incomplete)
	}
	e.recordStage(ctx, started.ID, stage, outcome, incomplete)

	if stage.Aborted != nil {
		outcome.Status = record.StatusRefused
		outcome.Error = stage.Aborted.Error()
		return e.finished(started.ID, outcome, incomplete)
	}
	if stage.TimedOut {
		outcome.Status = record.StatusTimedOut
		outcome.Error = fmt.Sprintf("the agent stage exceeded its %s timeout", timeout)
		return e.finished(started.ID, outcome, incomplete)
	}

	report, reportErr := stage.Report()
	switch {
	case stage.DecodeErr != nil && stage.Stream.Result == nil:
		reportErr = fmt.Errorf("the agent's output could not be read: %w", stage.DecodeErr)
	case reportErr == nil:
		reportErr = agent.ValidateReport(report, book.Agent.OutputSchema)
	}

	delivery := sink.Delivery{PlaybookName: book.Name, RunID: started.ID}
	switch {
	case reportErr != nil:
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

	e.deliver(ctx, started.ID, sinks, delivery, outcome, incomplete)
	return e.finished(started.ID, outcome, incomplete)
}

// deliver runs the sinks and records what each one did.
func (e *Executor) deliver(
	ctx context.Context, runID string, sinks []sink.Sink, delivery sink.Delivery,
	outcome *record.Run, incomplete *problems,
) {
	for _, delivered := range sink.DeliverAll(ctx, sinks, delivery) {
		incomplete.note(e.Store.AddSinkOutcome(ctx, runID, record.SinkOutcome{
			Sink: delivered.Sink, Status: string(delivered.Status),
			ItemsCreated: delivered.ItemsCreated, ItemsSkipped: delivered.ItemsSkipped,
			Detail: delivered.Detail,
		}))
		if delivered.Status == sink.StatusFailed && outcome.Status == record.StatusSucceeded {
			outcome.Status = record.StatusFailed
			outcome.Error = "a sink failed; the report is in the record and the run can be resumed"
		}
	}
}
