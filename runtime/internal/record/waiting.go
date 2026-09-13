package record

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// WaitOutcome is how a waiting trigger ended, or `waiting` while it has not.
type WaitOutcome string

// Every outcome a waiting trigger can have, as data-model.md lists them. Each one but
// waiting and ran is also the mechanism of the refusal that ended it.
const (
	WaitWaiting            WaitOutcome = "waiting"
	WaitRan                WaitOutcome = "ran"
	WaitExpired            WaitOutcome = "wait_expired"
	WaitRateLimited        WaitOutcome = "rate_limited"
	WaitPlaybookChanged    WaitOutcome = "playbook_changed"
	WaitBackendUnavailable WaitOutcome = "backend_unavailable"
	WaitDropped            WaitOutcome = "dropped"
)

// WaitingTrigger is a trigger accepted to wait for the run it collided with (FR-110). The
// wait itself lives in the memory of the process that accepted it; this is its acceptance,
// written when it happens so that a kill cannot erase it (FR-127).
type WaitingTrigger struct {
	ID           string
	PlaybookName string
	// PlaybookPath is the file it was accepted from, which is read again when it runs
	// (FR-121).
	PlaybookPath string
	TriggerKind  TriggerKind
	TriggerRef   string
	// AcceptedAt and ExpiresAt are the host clock's wall reading, for a reader. The wait is
	// enforced on the monotonic reading by the process holding it.
	AcceptedAt time.Time
	ExpiresAt  time.Time
	// Instance is the accepting process, whose liveness decides whether a waiting row is
	// really waiting.
	Instance  string
	Outcome   WaitOutcome
	OutcomeAt time.Time
	RunID     string
}

// ErrWaitingSlotFull is returned when a trigger is already waiting for the playbook
// (FR-111).
var ErrWaitingSlotFull = errors.New("a trigger is already waiting for this playbook")

// AcceptWaiting records a trigger accepted to wait, and fills in its identifier. The row is
// inserted only if no live one exists for the playbook, in the one statement that inserts
// it, so two processes on one state directory cannot both accept. When one is already
// waiting it returns that one, wrapped in ErrWaitingSlotFull.
func (s *Store) AcceptWaiting(
	ctx context.Context, trigger WaitingTrigger, values map[string]string,
) (WaitingTrigger, error) {
	if trigger.AcceptedAt.IsZero() || trigger.ExpiresAt.IsZero() {
		return WaitingTrigger{}, errors.New("a waiting trigger with no acceptance or expiry time")
	}
	id, err := newRecordID()
	if err != nil {
		return WaitingTrigger{}, err
	}
	trigger.ID, trigger.Outcome = id, WaitWaiting
	trigger.PlaybookName = s.redactor.Redact(trigger.PlaybookName)

	if len(values) > 0 {
		encoded, err := json.Marshal(values)
		if err != nil {
			return WaitingTrigger{}, err
		}
		ref, err := s.blobs.Put(trigger.ID, "trigger.json", encoded)
		if err != nil {
			return WaitingTrigger{}, err
		}
		trigger.TriggerRef = ref
	}

	result, err := s.db.ExecContext(ctx, `
		INSERT INTO waiting_triggers (id, playbook_name, playbook_path, trigger_kind, trigger_ref,
		                              accepted_at, expires_at, instance, outcome)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?
		 WHERE NOT EXISTS (SELECT 1 FROM waiting_triggers
		                    WHERE playbook_name = ? AND outcome = 'waiting')`,
		trigger.ID, trigger.PlaybookName, s.redactor.Redact(trigger.PlaybookPath),
		string(trigger.TriggerKind), nullable(trigger.TriggerRef),
		trigger.AcceptedAt.UTC().Format(sortableTime), trigger.ExpiresAt.UTC().Format(sortableTime),
		s.redactor.Redact(trigger.Instance), string(WaitWaiting), trigger.PlaybookName)
	if err != nil {
		return WaitingTrigger{}, fmt.Errorf("accepting a waiting trigger of %s: %w", trigger.PlaybookName, err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return WaitingTrigger{}, err
	}
	if inserted == 0 {
		occupant, err := s.waitingFor(ctx, trigger.PlaybookName)
		if err != nil {
			return WaitingTrigger{}, fmt.Errorf("%w; and reading which one: %w", ErrWaitingSlotFull, err)
		}
		return occupant, fmt.Errorf("%w: %s", ErrWaitingSlotFull, occupant.ID)
	}
	return trigger, nil
}

// WaitEnd is how a wait ended.
type WaitEnd struct {
	Outcome WaitOutcome
	At      time.Time
	// RunID is the run it became, when Outcome is ran.
	RunID string
	// Refusal is written in the same transaction, and only by the call that ended the wait.
	Refusal *Refusal
}

// EndWait records how a wait ended, and reports whether this call is what ended it. A wait
// ends once: a second call, from a reconciliation racing the process that held it, changes
// nothing and writes no second refusal.
func (s *Store) EndWait(ctx context.Context, id string, end WaitEnd) (bool, error) {
	if end.Outcome == WaitWaiting || end.At.IsZero() {
		return false, fmt.Errorf("waiting trigger %s: an end with no outcome or no time", id)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `
		UPDATE waiting_triggers SET outcome = ?, outcome_at = ?, run_id = ?
		 WHERE id = ? AND outcome = 'waiting'`,
		string(end.Outcome), end.At.UTC().Format(sortableTime), nullable(end.RunID), id)
	if err != nil {
		return false, fmt.Errorf("ending waiting trigger %s: %w", id, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if changed == 0 {
		return false, nil
	}
	if end.Refusal != nil {
		if err := s.insertRefusal(ctx, tx, *end.Refusal); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

const waitingColumns = `
		SELECT id, playbook_name, playbook_path, trigger_kind, trigger_ref, accepted_at,
		       expires_at, instance, outcome, outcome_at, run_id
		  FROM waiting_triggers`

// StillWaiting lists every trigger whose row says it is waiting, whether or not the
// process holding it is still alive: telling the two apart is the caller's.
func (s *Store) StillWaiting(ctx context.Context) ([]WaitingTrigger, error) {
	rows, err := s.db.QueryContext(ctx, waitingColumns+` WHERE outcome = 'waiting' ORDER BY accepted_at`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var triggers []WaitingTrigger
	for rows.Next() {
		trigger, err := scanWaiting(rows)
		if err != nil {
			return nil, err
		}
		triggers = append(triggers, trigger)
	}
	return triggers, rows.Err()
}

// GetWaitingTrigger reads one waiting trigger back, whatever its outcome.
func (s *Store) GetWaitingTrigger(ctx context.Context, id string) (WaitingTrigger, error) {
	trigger, err := scanWaiting(s.db.QueryRowContext(ctx, waitingColumns+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return WaitingTrigger{}, fmt.Errorf("no waiting trigger %s", id)
	}
	return trigger, err
}

func (s *Store) waitingFor(ctx context.Context, playbookName string) (WaitingTrigger, error) {
	return scanWaiting(s.db.QueryRowContext(ctx,
		waitingColumns+` WHERE playbook_name = ? AND outcome = 'waiting'`, playbookName))
}

func scanWaiting(from scanner) (WaitingTrigger, error) {
	var (
		trigger                          WaitingTrigger
		kind, accepted, expires, outcome string
		ref, outcomeAt, runID            sql.NullString
	)
	if err := from.Scan(&trigger.ID, &trigger.PlaybookName, &trigger.PlaybookPath, &kind, &ref,
		&accepted, &expires, &trigger.Instance, &outcome, &outcomeAt, &runID); err != nil {
		return WaitingTrigger{}, err
	}
	trigger.TriggerKind, trigger.Outcome = TriggerKind(kind), WaitOutcome(outcome)
	trigger.TriggerRef, trigger.RunID = ref.String, runID.String
	trigger.AcceptedAt = parseTime(sql.NullString{String: accepted, Valid: true})
	trigger.ExpiresAt = parseTime(sql.NullString{String: expires, Valid: true})
	trigger.OutcomeAt = parseTime(outcomeAt)
	return trigger, nil
}
