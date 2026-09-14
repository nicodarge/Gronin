package run

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
	"github.com/nicodarge/Gronin/runtime/internal/playbook"
	"github.com/nicodarge/Gronin/runtime/internal/record"
	"github.com/nicodarge/Gronin/runtime/internal/sink"
	"github.com/nicodarge/Gronin/runtime/internal/stage/agent"
	"github.com/nicodarge/Gronin/runtime/internal/stage/retrieve"
)

// ErrNotReplayable is returned for a run whose record does not hold what a replay needs.
var ErrNotReplayable = errors.New("this run cannot be replayed from its record")

// ErrPlaybookChanged refuses a replay or a resume whose playbook is no longer the one
// that produced the record.
//
// The record keeps the playbook as it actually ran, precisely so it stays readable after
// the file changes underneath it. Rebuilding the bound from whatever is on disk now
// would let a replay run under a WIDER declaration than the one whose inputs it is
// reusing, and a resume deliver an old report to a destination nobody reviewed against
// it — silently in both cases, which is the part that matters.
var ErrPlaybookChanged = errors.New("the playbook has changed since the run being replayed")

// sameAsRecorded compares the playbook now against the one the record kept.
func (e *Executor) sameAsRecorded(parent record.Run, book *playbook.Playbook) error {
	if parent.ResolvedPlaybookRef == "" {
		return fmt.Errorf("%w: it recorded no copy of the playbook it ran", ErrNotReplayable)
	}
	recorded, err := e.Store.Blobs().Get(parent.ResolvedPlaybookRef)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNotReplayable, err)
	}
	now, err := json.MarshalIndent(book, "", "  ")
	if err != nil {
		return err
	}
	if !bytes.Equal(bytes.TrimSpace(recorded), bytes.TrimSpace(now)) {
		return fmt.Errorf("%w: %s no longer matches what run %s used; read it with "+
			"`gronin show %s`", ErrPlaybookChanged, book.Name, parent.ID, parent.ID)
	}
	return nil
}

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
	retrievals, err := e.Store.Retrievals(ctx, parentID)
	if err != nil {
		return record.Run{}, err
	}
	if parent.PromptRef == "" {
		return record.Run{}, fmt.Errorf("%w: it recorded no prompt", ErrNotReplayable)
	}
	if err := e.sameAsRecorded(parent, book); err != nil {
		return record.Run{}, err
	}
	prompt, err := e.Store.Blobs().Get(parent.PromptRef)
	if err != nil {
		return record.Run{}, fmt.Errorf("%w: %w", ErrNotReplayable, err)
	}

	admitted, err := e.admit(ctx, book, record.TriggerReplay, time.Time{})
	if err != nil {
		return record.Run{}, err
	}
	stages, stopRun := context.WithCancel(ctx)
	defer stopRun()
	hold := e.guard().Hold(admitted, stopRun)
	defer hold.Done()

	claimed := claimedOf(admitted, book.Name)
	started, err := e.Manager.Begin(ctx, claimed, record.TriggerReplay, parentID)
	if err != nil {
		_ = claimed.Claim.Release(ctx)
		return record.Run{}, err
	}

	outcome := record.Run{Status: record.StatusFailed, ParentRunID: parentID}
	incomplete := &problems{}
	defer func() {
		stoppedByTheGuard(hold, &outcome)
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

	// FR-226: what the run's agent read from each retrieval, put back where it read it.
	// No index is opened: it has moved on since, and searching it again would hand the
	// agent something the run never saw.
	for _, retrieval := range retrievals {
		if retrieval.ResultsRef == "" {
			continue
		}
		restored, err := e.restoredRetrieval(retrieval)
		if err != nil {
			outcome.Status = record.StatusRefused
			outcome.Error = fmt.Errorf("%w: retrieval %q: %w", ErrNotReplayable, retrieval.AsName, err).Error()
			return e.finished(started.ID, &outcome, incomplete)
		}
		if err := os.WriteFile(filepath.Join(started.WorkDir, filepath.Base(retrieval.AsName)), restored.Results, 0o600); err != nil {
			outcome.Status = record.StatusRefused
			outcome.Error = err.Error()
			return e.finished(started.ID, &outcome, incomplete)
		}
		e.recordRetrieval(ctx, started.ID, restored, incomplete)
	}

	ref, err := e.Store.Blobs().Put(started.ID, "prompt.txt", prompt)
	incomplete.note(err)
	outcome.PromptRef = ref
	if resolved, err := e.storeResolved(started.ID, book); err == nil {
		outcome.ResolvedPlaybookRef = resolved
	}

	return e.agentAndSinks(ctx, stages, hold, started, book, string(prompt), &outcome, incomplete, nil)
}

// restoredRetrieval reads a recorded retrieval back whole — its row, its results file and
// each result's content — to be recorded again against a replay.
func (e *Executor) restoredRetrieval(recorded record.Retrieval) (retrieve.Retrieved, error) {
	results, err := e.Store.Blobs().Get(recorded.ResultsRef)
	if err != nil {
		return retrieve.Retrieved{}, err
	}
	restored := retrieve.Retrieved{Record: recorded, Results: results}
	restored.Record.ResultsRef = ""
	restored.Record.Items = make([]record.RetrievedItem, len(recorded.Items))
	for at, item := range recorded.Items {
		content, err := e.Store.Blobs().Get(item.ContentRef)
		if err != nil {
			return retrieve.Retrieved{}, err
		}
		item.ContentRef = ""
		restored.Record.Items[at] = item
		restored.Contents = append(restored.Contents, string(content))
	}
	return restored, nil
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
	if err := e.sameAsRecorded(parent, book); err != nil {
		return record.Run{}, err
	}

	admitted, err := e.admit(ctx, book, record.TriggerResume, time.Time{})
	if err != nil {
		return record.Run{}, err
	}
	stages, stopRun := context.WithCancel(ctx)
	defer stopRun()
	hold := e.guard().Hold(admitted, stopRun)
	defer hold.Done()

	claimed := claimedOf(admitted, book.Name)
	started, err := e.Manager.Begin(ctx, claimed, record.TriggerResume, parentID)
	if err != nil {
		_ = claimed.Claim.Release(ctx)
		return record.Run{}, err
	}

	outcome := record.Run{Status: record.StatusFailed, ParentRunID: parentID}
	incomplete := &problems{}
	defer func() {
		stoppedByTheGuard(hold, &outcome)
		outcome.EndedAt = e.now()
		if err := e.Manager.Finish(ctx, started, outcome); err != nil {
			e.log().Error("the resume's terminal state could not be recorded",
				"run", started.ID, "parent", parentID, "err", err)
		}
	}()

	sinks, refusals := sink.Build(declarationsOf(book), sink.BuildOptions{
		Interpolate:            func(text string) (string, error) { return e.Config.Interpolate(text, nil) },
		Client:                 e.Client,
		RefusePayloadReference: book.Trigger.Type == "webhook",
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
	e.deliver(ctx, stages, hold, started.ID, sinks, sink.Delivery{
		PlaybookName: book.Name, RunID: started.ID, Report: report,
	}, &outcome, incomplete)

	return e.finished(started.ID, &outcome, incomplete)
}

// agentAndSinks is the half of a run that a replay repeats: one agent stage, the schema
// check, and the sinks. Shared with Execute so a replay cannot drift into being bounded
// differently from the run it derives from.
func (e *Executor) agentAndSinks(
	ctx, stages context.Context, hold *guard.Hold, started *Run, book *playbook.Playbook,
	prompt string, outcome *record.Run, incomplete *problems, trigger map[string]string,
) (record.Run, error) {
	sinks, refusals := sink.Build(declarationsOf(book), sink.BuildOptions{
		Interpolate:            func(text string) (string, error) { return e.Config.Interpolate(text, trigger) },
		Client:                 e.Client,
		RefusePayloadReference: book.Trigger.Type == "webhook",
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

	servers, err := e.serversOf(book)
	if err != nil {
		outcome.Status = record.StatusRefused
		outcome.Error = err.Error()
		return e.finished(started.ID, outcome, incomplete)
	}

	// R3's first fence: before the agent stage starts, which is the first thing here
	// that costs anything.
	if err := hold.Fence(ctx); err != nil {
		outcome.Error = err.Error()
		return e.finished(started.ID, outcome, incomplete)
	}

	declaration := declarationOf(book)
	stage, err := agent.Run(stages, declaration, agent.Options{
		Executable: e.AgentExecutable,
		WorkDir:    started.WorkDir,
		Prompt:     prompt,
		Timeout:    timeout,
		Env:        e.AgentEnv,
		MCPServers: servers,
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

	e.deliver(ctx, stages, hold, started.ID, sinks, delivery, outcome, incomplete)
	stoppedByTheGuard(hold, outcome)
	return e.finished(started.ID, outcome, incomplete)
}

// deliver runs the sinks and records what each one did. The hold fences before each
// one: a side effect is the thing a claim that can no longer be proven held must not
// produce (R3).
func (e *Executor) deliver(
	ctx, stages context.Context, hold *guard.Hold, runID string, sinks []sink.Sink,
	delivery sink.Delivery, outcome *record.Run, incomplete *problems,
) {
	delivered, fenceErr := sink.DeliverAll(stages, sinks, delivery, hold.Fence)
	if fenceErr != nil {
		outcome.Error = joinReasons(outcome.Error, fenceErr.Error())
	}
	for _, delivered := range delivered {
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
