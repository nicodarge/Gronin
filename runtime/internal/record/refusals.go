package record

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"
)

// Refusal is a trigger that did not become a run, terminally (FR-117). It is not a run
// and is never counted as one.
type Refusal struct {
	ID           string
	PlaybookName string
	TriggerKind  TriggerKind
	// DueAt is, for a scheduled trigger, the instant its schedule computed (FR-129). Never
	// the host's clock at acceptance, and zero for any other kind.
	DueAt time.Time
	// WaitingTriggerID is set when the refusal ended a wait.
	WaitingTriggerID string
	Mechanism        Mechanism
	// Detail names what refused it: the holder, the limit, the backend and its error, or
	// what the playbook's file now declares.
	Detail string
	// RefusedAt is the host clock's wall reading.
	RefusedAt time.Time

	// DeliveryID is set for every refusal of a webhook trigger (FR-328), so the delivery
	// that a drop or a decision names is readable from the refusal alone.
	DeliveryID string
}

// sortableTime is fixed-width so that the text sorts as the instant does. RFC3339Nano
// drops trailing zeros, and "05.5Z" sorts before "05Z" although it is later.
const sortableTime = "2006-01-02T15:04:05.000000000Z07:00"

// RecordRefusal writes one refusal record. Its detail and playbook name pass the redactor
// on the way in, like every other record: a backend's error is one of the likeliest
// places for a credential to surface. An empty identifier is filled in.
func (s *Store) RecordRefusal(ctx context.Context, refusal Refusal) error {
	return s.insertRefusal(ctx, s.db, refusal)
}

// execer is what a refusal is inserted through: the database, or a transaction that ends a
// wait in the same step.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func (s *Store) insertRefusal(ctx context.Context, db execer, refusal Refusal) error {
	if refusal.ID == "" {
		id, err := newRecordID()
		if err != nil {
			return err
		}
		refusal.ID = id
	}
	if refusal.RefusedAt.IsZero() {
		return fmt.Errorf("refusal %s: no time it was refused at", refusal.ID)
	}
	var due any
	if !refusal.DueAt.IsZero() {
		due = refusal.DueAt.UTC().Format(sortableTime)
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO refusals (id, playbook_name, trigger_kind, due_at, waiting_trigger_id,
		                      mechanism, detail, refused_at, delivery_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		refusal.ID, s.redactor.Redact(refusal.PlaybookName), string(refusal.TriggerKind), due,
		nullable(refusal.WaitingTriggerID), string(refusal.Mechanism),
		s.redactor.Redact(refusal.Detail), refusal.RefusedAt.UTC().Format(sortableTime),
		nullable(refusal.DeliveryID))
	if err != nil {
		return fmt.Errorf("recording refusal %s: %w", refusal.ID, err)
	}
	return nil
}

// ListRefusals returns refusal records most recent first.
func (s *Store) ListRefusals(ctx context.Context, limit int) ([]Refusal, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, playbook_name, trigger_kind, due_at, waiting_trigger_id, mechanism, detail,
		       refused_at, delivery_id
		  FROM refusals ORDER BY refused_at DESC, rowid DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var refusals []Refusal
	for rows.Next() {
		var (
			refusal                  Refusal
			kind, mechanism, refused string
			due, waitingTrigger      sql.NullString
			delivery                 sql.NullString
		)
		if err := rows.Scan(&refusal.ID, &refusal.PlaybookName, &kind, &due, &waitingTrigger,
			&mechanism, &refusal.Detail, &refused, &delivery); err != nil {
			return nil, err
		}
		refusal.TriggerKind, refusal.Mechanism = TriggerKind(kind), Mechanism(mechanism)
		refusal.DueAt = parseTime(due)
		refusal.WaitingTriggerID = waitingTrigger.String
		refusal.RefusedAt = parseTime(sql.NullString{String: refused, Valid: true})
		refusal.DeliveryID = delivery.String
		refusals = append(refusals, refusal)
	}
	return refusals, rows.Err()
}

func newRecordID() (string, error) {
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return "", fmt.Errorf("generating a record identifier: %w", err)
	}
	return hex.EncodeToString(suffix), nil
}
