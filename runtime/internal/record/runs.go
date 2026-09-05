package record

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// CreateRun writes the row a run starts from.
func (s *Store) CreateRun(ctx context.Context, run Run) error {
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now().UTC()
	}
	var parent any
	if run.ParentRunID != "" {
		parent = run.ParentRunID
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO runs (id, playbook_name, resolved_playbook_ref, report_ref, prompt_ref,
		                  trigger_kind, parent_run_id, status, started_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, s.redactor.Redact(run.PlaybookName), nullable(run.ResolvedPlaybookRef),
		nullable(run.ReportRef), nullable(run.PromptRef), string(run.TriggerKind), parent,
		string(run.Status), formatTime(run.StartedAt))
	if err != nil {
		return fmt.Errorf("recording run %s: %w", run.ID, err)
	}
	return nil
}

// FinishRun writes a run's terminal state. Every text field it carries goes through the
// redactor on the way in — an error message is one of the likeliest places for a
// credential to surface, because it is usually the thing that failed to authenticate.
func (s *Store) FinishRun(ctx context.Context, run Run) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE runs
		   SET status = ?, ended_at = ?, cost_usd = ?, tokens = ?, agent_session_id = ?,
		       credential_source = ?, error = ?, report_ref = coalesce(?, report_ref),
		       prompt_ref = coalesce(?, prompt_ref)
		 WHERE id = ?`,
		string(run.Status), formatTime(run.EndedAt), run.CostUSD, run.Tokens,
		nullable(run.AgentSessionID), nullable(run.CredentialSource),
		nullable(s.redactor.Redact(run.Error)), nullable(run.ReportRef), nullable(run.PromptRef),
		run.ID)
	if err != nil {
		return fmt.Errorf("finishing run %s: %w", run.ID, err)
	}
	return oneRowChanged(result, run.ID)
}

// GetRun reads one run back.
func (s *Store) GetRun(ctx context.Context, id string) (Run, error) {
	var (
		run                                       Run
		resolved, report, prompt, parent, session sql.NullString
		credential, failure                       sql.NullString
		ended                                     sql.NullString
		cost                                      sql.NullFloat64
		tokens                                    sql.NullInt64
		started                                   sql.NullString
		trigger, status                           string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT id, playbook_name, resolved_playbook_ref, report_ref, prompt_ref, trigger_kind,
		       parent_run_id, status, started_at, ended_at, cost_usd, tokens, agent_session_id,
		       credential_source, error
		  FROM runs WHERE id = ?`, id).
		Scan(&run.ID, &run.PlaybookName, &resolved, &report, &prompt, &trigger, &parent,
			&status, &started, &ended, &cost, &tokens, &session, &credential, &failure)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return Run{}, err
	}

	run.ResolvedPlaybookRef, run.ReportRef, run.PromptRef = resolved.String, report.String, prompt.String
	run.TriggerKind, run.Status = TriggerKind(trigger), Status(status)
	run.ParentRunID, run.AgentSessionID = parent.String, session.String
	run.CredentialSource, run.Error = credential.String, failure.String
	run.StartedAt, run.EndedAt = parseTime(started), parseTime(ended)
	run.CostUSD, run.Tokens = cost.Float64, tokens.Int64
	return run, nil
}

// ListRuns returns runs most recent first.
func (s *Store) ListRuns(ctx context.Context, limit int) ([]Run, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM runs ORDER BY started_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	runs := make([]Run, 0, len(ids))
	for _, id := range ids {
		run, err := s.GetRun(ctx, id)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, nil
}

// GatheredInput is one artifact a gather step produced.
type GatheredInput struct {
	Name      string
	BlobRef   string
	Bytes     int64
	Truncated bool
	ExitCode  int
	StderrRef string
}

// AddGatheredInput records one gather step's result.
func (s *Store) AddGatheredInput(ctx context.Context, runID string, input GatheredInput) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO gathered_inputs (run_id, name, blob_ref, bytes, truncated, exit_code, stderr_ref)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		runID, s.redactor.Redact(input.Name), nullable(input.BlobRef), input.Bytes,
		input.Truncated, input.ExitCode, nullable(input.StderrRef))
	return err
}

// GatheredInputs reads a run's gathered inputs back, in name order.
func (s *Store) GatheredInputs(ctx context.Context, runID string) ([]GatheredInput, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT name, blob_ref, bytes, truncated, exit_code, stderr_ref
		  FROM gathered_inputs WHERE run_id = ? ORDER BY name`, runID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var inputs []GatheredInput
	for rows.Next() {
		var (
			input        GatheredInput
			blob, stderr sql.NullString
		)
		if err := rows.Scan(&input.Name, &blob, &input.Bytes, &input.Truncated,
			&input.ExitCode, &stderr); err != nil {
			return nil, err
		}
		input.BlobRef, input.StderrRef = blob.String, stderr.String
		inputs = append(inputs, input)
	}
	return inputs, rows.Err()
}

// ToolCall is one tool invocation within a run.
type ToolCall struct {
	Sequence   int
	Name       string
	InputRef   string
	OutputRef  string
	StartedAt  time.Time
	DurationMS int64
	Outcome    string
}

// AddToolCall records one tool invocation.
func (s *Store) AddToolCall(ctx context.Context, runID string, call ToolCall) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tool_calls (run_id, sequence, name, input_ref, output_ref, started_at,
		                        duration_ms, outcome)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		runID, call.Sequence, s.redactor.Redact(call.Name), nullable(call.InputRef),
		nullable(call.OutputRef), formatTime(call.StartedAt), call.DurationMS,
		s.redactor.Redact(call.Outcome))
	return err
}

// RefusedAction is one action the agent attempted that its bounds refused.
type RefusedAction struct {
	Sequence int
	Tool     string
	Reason   string
}

// AddRefusedAction records one refusal the agent process reported.
func (s *Store) AddRefusedAction(ctx context.Context, runID string, action RefusedAction) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO refused_actions (run_id, sequence, tool, reason) VALUES (?, ?, ?, ?)`,
		runID, action.Sequence, s.redactor.Redact(action.Tool), s.redactor.Redact(action.Reason))
	return err
}

// RefusedActions reads a run's refusals back, in order.
func (s *Store) RefusedActions(ctx context.Context, runID string) ([]RefusedAction, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT sequence, tool, reason FROM refused_actions WHERE run_id = ? ORDER BY sequence`, runID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var actions []RefusedAction
	for rows.Next() {
		var action RefusedAction
		if err := rows.Scan(&action.Sequence, &action.Tool, &action.Reason); err != nil {
			return nil, err
		}
		actions = append(actions, action)
	}
	return actions, rows.Err()
}

// SinkOutcome is what one sink did for one run.
type SinkOutcome struct {
	Sink         string
	Status       string
	ItemsCreated int
	ItemsSkipped int
	Detail       string
}

// AddSinkOutcome records what one sink did. Per sink, so one sink failing does not make
// the run's other deliveries unknowable.
func (s *Store) AddSinkOutcome(ctx context.Context, runID string, outcome SinkOutcome) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sink_outcomes (run_id, sink, status, items_created, items_skipped, detail)
		VALUES (?, ?, ?, ?, ?, ?)`,
		runID, s.redactor.Redact(outcome.Sink), outcome.Status, outcome.ItemsCreated,
		outcome.ItemsSkipped, nullable(s.redactor.Redact(outcome.Detail)))
	return err
}

// SinkOutcomes reads a run's per-sink outcomes back.
func (s *Store) SinkOutcomes(ctx context.Context, runID string) ([]SinkOutcome, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT sink, status, items_created, items_skipped, detail
		  FROM sink_outcomes WHERE run_id = ? ORDER BY sink`, runID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var outcomes []SinkOutcome
	for rows.Next() {
		var (
			outcome SinkOutcome
			detail  sql.NullString
		)
		if err := rows.Scan(&outcome.Sink, &outcome.Status, &outcome.ItemsCreated,
			&outcome.ItemsSkipped, &detail); err != nil {
			return nil, err
		}
		outcome.Detail = detail.String
		outcomes = append(outcomes, outcome)
	}
	return outcomes, rows.Err()
}

// RecordMissedOccurrence records a scheduled occurrence that did not execute, and why
// (FR-030). Silence about a schedule that did not fire is indistinguishable from a
// schedule that fired and found nothing to say.
func (s *Store) RecordMissedOccurrence(ctx context.Context, playbookName string, dueAt time.Time, reason string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO missed_occurrences (playbook_name, due_at, noticed_at, reason)
		VALUES (?, ?, ?, ?)`,
		s.redactor.Redact(playbookName), formatTime(dueAt), nowUTC(), s.redactor.Redact(reason))
	return err
}

// MarkRunningAsInterrupted is startup reconciliation (FR-031). A run the database still
// calls running cannot be: this process has just started. It is marked interrupted and
// left alone — resuming it automatically would spend money on a decision nobody made.
func (s *Store) MarkRunningAsInterrupted(ctx context.Context) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE runs SET status = ?, ended_at = ?, error = ?
		 WHERE status = ?`,
		string(StatusInterrupted), nowUTC(),
		"the runtime stopped while this run was in flight", string(StatusRunning))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func oneRowChanged(result sql.Result, id string) error {
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return nil
}
