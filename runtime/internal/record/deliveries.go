package record

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// DeliveryState is a delivery's own state, which follows its hand-offs (data-model.md,
// *Hand-off*).
type DeliveryState string

// Every state a delivery can be in.
const (
	DeliveryAccepted  DeliveryState = "accepted"
	DeliveryWaiting   DeliveryState = "waiting"
	DeliveryHandedOff DeliveryState = "handed_off"
	DeliveryDropped   DeliveryState = "dropped"
	// DeliveryUnbound is a delivery whose source no loaded playbook is bound to. It hands
	// off to nothing, and the record says so rather than the delivery vanishing.
	DeliveryUnbound DeliveryState = "unbound"
)

// IdentityKind is how a delivery's identity was decided (FR-316).
type IdentityKind string

// Either a value the source declares, or the body's own digest.
const (
	IdentityDeclared IdentityKind = "declared"
	IdentityDigest   IdentityKind = "digest"
)

// Delivery is one authenticated request the ingress accepted — new, or a retry of one it
// dropped (data-model.md, *Delivery*).
type Delivery struct {
	ID           string
	Source       string
	Identity     string
	IdentityKind IdentityKind
	// ReceivedAt is the host clock's wall reading (FR-320).
	ReceivedAt time.Time
	// Peer is the connection's own address, never a forwarded one (FR-334).
	Peer string
	// BodyRef is the blob the exact bytes were written to, filed under the delivery's own
	// identifier and redacted at the blob store's write boundary like every other blob.
	BodyRef string
	// BodySHA256 is over the exact bytes received, before redaction.
	BodySHA256 string
	// Repeats is how many times this identity was seen again inside the window (FR-317).
	Repeats int
	// Instance is the accepting process, whose instance lock decides whether an accepted
	// or waiting delivery is really in flight.
	Instance string
	// Supersedes is the dropped delivery this one retries, for a retry of one FR-315
	// dropped.
	Supersedes string
	State      DeliveryState
}

// HandOffState is one delivery's outcome against one bound playbook.
type HandOffState string

// Every state a hand-off can be in (data-model.md, *Hand-off*). Only handed_off, refused
// and dropped are decided; pending and waiting are not.
const (
	HandOffPending   HandOffState = "pending"
	HandOffWaiting   HandOffState = "waiting"
	HandOffHandedOff HandOffState = "handed_off"
	HandOffRefused   HandOffState = "refused"
	HandOffDropped   HandOffState = "dropped"
)

// HandOff is one delivery's attempt against one bound playbook, created with the delivery
// (data-model.md, *Hand-off*).
type HandOff struct {
	DeliveryID   string
	PlaybookName string
	State        HandOffState
	// DecidedAt is set once the hand-off is decided, and zero while it is pending or
	// waiting under the guard.
	DecidedAt time.Time
}

// ErrNoDelivery is returned for a delivery identifier nothing holds.
var ErrNoDelivery = errors.New("no such delivery")

const deliveryColumns = `
	SELECT id, source, identity, identity_kind, received_at, peer, body_ref, body_sha256,
	       repeats, instance, supersedes, state
	  FROM deliveries`

func scanDelivery(from scanner) (Delivery, error) {
	var (
		delivery       Delivery
		kind, received string
		state          string
		supersedes     sql.NullString
	)
	if err := from.Scan(&delivery.ID, &delivery.Source, &delivery.Identity, &kind,
		&received, &delivery.Peer, &delivery.BodyRef, &delivery.BodySHA256,
		&delivery.Repeats, &delivery.Instance, &supersedes, &state); err != nil {
		return Delivery{}, err
	}
	delivery.IdentityKind, delivery.State = IdentityKind(kind), DeliveryState(state)
	delivery.ReceivedAt = parseTime(sql.NullString{String: received, Valid: true})
	delivery.Supersedes = supersedes.String
	return delivery, nil
}

// GetDelivery reads one delivery back.
func (s *Store) GetDelivery(ctx context.Context, id string) (Delivery, error) {
	delivery, err := scanDelivery(s.db.QueryRowContext(ctx, deliveryColumns+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Delivery{}, fmt.Errorf("%w: %s", ErrNoDelivery, id)
	}
	if err != nil {
		return Delivery{}, err
	}
	return delivery, nil
}

// ListDeliveries returns deliveries most recent first.
func (s *Store) ListDeliveries(ctx context.Context, limit int) ([]Delivery, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, deliveryColumns+`
		 ORDER BY received_at DESC, rowid DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var deliveries []Delivery
	for rows.Next() {
		delivery, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		deliveries = append(deliveries, delivery)
	}
	return deliveries, rows.Err()
}

// HandOffsOf returns every hand-off a delivery has, one per bound playbook, in playbook
// name order — deterministic, for a reader and for a test alike.
func (s *Store) HandOffsOf(ctx context.Context, deliveryID string) ([]HandOff, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT delivery_id, playbook_name, state, decided_at
		  FROM handoffs WHERE delivery_id = ? ORDER BY playbook_name`, deliveryID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var handoffs []HandOff
	for rows.Next() {
		var (
			handoff   HandOff
			state     string
			decidedAt sql.NullString
		)
		if err := rows.Scan(&handoff.DeliveryID, &handoff.PlaybookName, &state, &decidedAt); err != nil {
			return nil, err
		}
		handoff.State = HandOffState(state)
		handoff.DecidedAt = parseTime(decidedAt)
		handoffs = append(handoffs, handoff)
	}
	return handoffs, rows.Err()
}

// SetHandOffState writes one hand-off's decision: a run starting or a guard refusal
// (T054), a value refused (T028), or the reconciliation finding the accepting process
// gone (T051). decidedAt is zero for a transition into waiting, which is not a decision
// (data-model.md, *Hand-off*).
func (s *Store) SetHandOffState(
	ctx context.Context, deliveryID, playbookName string, state HandOffState, decidedAt time.Time,
) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE handoffs SET state = ?, decided_at = ? WHERE delivery_id = ? AND playbook_name = ?`,
		string(state), formatTime(decidedAt), deliveryID, playbookName)
	if err != nil {
		return fmt.Errorf("setting hand-off %s/%s: %w", deliveryID, playbookName, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return fmt.Errorf("%w: hand-off %s/%s", ErrNoDelivery, deliveryID, playbookName)
	}
	return nil
}

// SetDeliveryState writes a delivery's own state, which follows its hand-offs' as
// data-model.md, *Hand-off* describes.
func (s *Store) SetDeliveryState(ctx context.Context, id string, state DeliveryState) error {
	result, err := s.db.ExecContext(ctx, `UPDATE deliveries SET state = ? WHERE id = ?`, string(state), id)
	if err != nil {
		return fmt.Errorf("setting delivery %s to %s: %w", id, state, err)
	}
	return oneRowChanged(result, id)
}
