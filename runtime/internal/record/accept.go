package record

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// AcceptParams is everything one authenticated request offers Accept about a delivery,
// short of the decision Accept itself makes (data-model.md, *Delivery identity*).
type AcceptParams struct {
	Source string
	// Identity is the value at the source's declared pointer (FR-316); empty when the
	// source declares none, in which case Accept takes IdentityKind empty too and
	// computes the SHA-256 digest of Body itself.
	Identity     string
	IdentityKind IdentityKind
	// Body is the exact bytes received, filed under the delivery's own identifier when
	// this call is what accepts it.
	Body []byte
	// Peer is the connection's own address (FR-334).
	Peer string
	// ReceivedAt is the runtime clock's wall reading (FR-320): what a new delivery is
	// stamped with, and what an existing identity's age is judged against.
	ReceivedAt time.Time
	// Instance is the accepting process, recorded on a new delivery so a later
	// acceptance can tell whether it is still in flight.
	Instance string
	// ReplayWindow is the source's own (FR-317).
	ReplayWindow time.Duration
	// Playbooks is every loaded playbook bound to Source, so a new delivery's hand-off
	// rows are created with it, in the same transaction (data-model.md, *Delivery
	// identity*). Empty for a source no playbook is bound to.
	Playbooks []string
}

// Alive reports whether the process that accepted a delivery still holds its instance
// lock — the guard's, checked without this package importing guard (plan.md, *Path
// Conventions*). A nil Alive is treated as "still alive": it only ever widens what a
// retry could recover from a dead process, never lets a live one's delivery be reconciled
// out from under it.
type Alive func(instance string) bool

// AcceptSeam is called once inside Accept's transaction, when set: after it has decided
// whether the identity is new and written that decision, but before the transaction
// commits. Nil outside TestOneIdentityIsNewOnce (T039), the only thing that sets it, and
// only ever in the one process under test — never in the reference implementation.
var AcceptSeam func()

// Accept is one write transaction, holding the write lock from its first statement
// (data-model.md, *Delivery identity*; FR-318): a second acceptance of the same identity
// waits for this one to commit rather than reading around it. It decides whether params'
// identity is new, records the delivery, its identity and its hand-offs together when it
// is, and otherwise counts a repeat on the delivery already holding it. The second return
// value is that decision: true when this call is what recorded the delivery, false for a
// repeat, which FR-317 says is never handed off again.
func Accept(ctx context.Context, s *Store, params AcceptParams, alive Alive) (Delivery, bool, error) {
	if params.ReceivedAt.IsZero() {
		return Delivery{}, false, fmt.Errorf("accepting a delivery for %s: no time it was received at", params.Source)
	}

	digest := sha256.Sum256(params.Body)
	bodySHA := hex.EncodeToString(digest[:])
	identity, kind := s.redactor.Redact(params.Identity), params.IdentityKind
	if kind == "" {
		identity, kind = bodySHA, IdentityDigest
	}
	source := s.redactor.Redact(params.Source)
	peer := s.redactor.Redact(params.Peer)

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return Delivery{}, false, fmt.Errorf("accepting a delivery: %w", err)
	}
	defer func() { _ = conn.Close() }()

	// BEGIN IMMEDIATE, by hand rather than through sql.Tx: this takes SQLite's write
	// lock from this very statement (A1, A4), which is what makes a second acceptance
	// of the same identity wait for this one to commit rather than reading around it.
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return Delivery{}, false, fmt.Errorf("taking the write lock to accept a delivery: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
		}
	}()

	existing, found, err := existingDelivery(ctx, conn, source, identity)
	if err != nil {
		return Delivery{}, false, err
	}

	isNew, supersedes := true, ""
	if found {
		isNew, supersedes, err = judge(ctx, conn, existing, params, alive)
		if err != nil {
			return Delivery{}, false, err
		}
	}

	if !isNew {
		if _, err := conn.ExecContext(ctx,
			`UPDATE deliveries SET repeats = repeats + 1 WHERE id = ?`, existing.ID); err != nil {
			return Delivery{}, false, err
		}
		if AcceptSeam != nil {
			AcceptSeam()
		}
		if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
			return Delivery{}, false, err
		}
		committed = true
		existing.Repeats++
		return existing, false, nil
	}

	playbooks := params.Playbooks
	if supersedes != "" {
		decided, err := decidedInChain(ctx, conn, supersedes)
		if err != nil {
			return Delivery{}, false, err
		}
		filtered := make([]string, 0, len(playbooks))
		for _, name := range playbooks {
			if !decided[name] {
				filtered = append(filtered, name)
			}
		}
		playbooks = filtered
	}

	deliveryID, err := newDeliveryID(params.ReceivedAt)
	if err != nil {
		return Delivery{}, false, err
	}
	bodyRef, err := s.blobs.Put(deliveryID, "body", params.Body)
	if err != nil {
		return Delivery{}, false, err
	}

	delivery := Delivery{
		ID: deliveryID, Source: source, Identity: identity, IdentityKind: kind,
		ReceivedAt: params.ReceivedAt, Peer: peer, BodyRef: bodyRef, BodySHA256: bodySHA,
		Instance: s.redactor.Redact(params.Instance), Supersedes: supersedes,
		State: DeliveryAccepted,
	}
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO deliveries (id, source, identity, identity_kind, received_at, peer,
		                        body_ref, body_sha256, repeats, instance, supersedes, state)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?)`,
		delivery.ID, delivery.Source, delivery.Identity, string(delivery.IdentityKind),
		delivery.ReceivedAt.UTC().Format(sortableTime), delivery.Peer, delivery.BodyRef,
		delivery.BodySHA256, delivery.Instance, nullable(delivery.Supersedes),
		string(delivery.State)); err != nil {
		return Delivery{}, false, fmt.Errorf("recording delivery %s: %w", delivery.ID, err)
	}

	if _, err := conn.ExecContext(ctx, `
		INSERT INTO delivery_identities (source, identity, delivery_id, accepted_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(source, identity) DO UPDATE SET
			delivery_id = excluded.delivery_id, accepted_at = excluded.accepted_at`,
		source, identity, delivery.ID, delivery.ReceivedAt.UTC().Format(sortableTime)); err != nil {
		return Delivery{}, false, fmt.Errorf("repointing identity (%s, %s): %w", source, identity, err)
	}

	for _, playbookName := range playbooks {
		if _, err := conn.ExecContext(ctx, `
			INSERT INTO handoffs (delivery_id, playbook_name, state) VALUES (?, ?, ?)`,
			delivery.ID, s.redactor.Redact(playbookName), string(HandOffPending)); err != nil {
			return Delivery{}, false, fmt.Errorf("creating hand-off %s/%s: %w", delivery.ID, playbookName, err)
		}
	}

	if AcceptSeam != nil {
		AcceptSeam()
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return Delivery{}, false, err
	}
	committed = true
	return delivery, true, nil
}

// existingDelivery reads the delivery an identity currently points at, if any.
func existingDelivery(ctx context.Context, conn *sql.Conn, source, identity string) (Delivery, bool, error) {
	var deliveryID, acceptedAtText string
	err := conn.QueryRowContext(ctx, `
		SELECT delivery_id, accepted_at FROM delivery_identities WHERE source = ? AND identity = ?`,
		source, identity).Scan(&deliveryID, &acceptedAtText)
	if errors.Is(err, sql.ErrNoRows) {
		return Delivery{}, false, nil
	}
	if err != nil {
		return Delivery{}, false, err
	}
	delivery, err := scanDelivery(conn.QueryRowContext(ctx, deliveryColumns+` WHERE id = ?`, deliveryID))
	if err != nil {
		return Delivery{}, false, err
	}
	// received_at on the identity row is when it was last accepted, which is not
	// necessarily the delivery's own ReceivedAt for a delivery that has since been
	// repointed by a retry; the identity row's own accepted_at is the window's anchor
	// (data-model.md, *Delivery identity*).
	delivery.ReceivedAt = parseTime(sql.NullString{String: acceptedAtText, Valid: true})
	return delivery, true, nil
}

// judge decides whether an existing identity is new again, and what it supersedes, per
// data-model.md's *Delivery identity*: new when the window has passed, when the delivery
// it points at is already dropped, or when that delivery's accepting process is gone and
// reconciling it here finds it undecided and drops it. Otherwise the identity is a
// repeat of the delivery it already points at.
func judge(
	ctx context.Context, conn *sql.Conn, existing Delivery, params AcceptParams, alive Alive,
) (isNew bool, supersedes string, err error) {
	if params.ReceivedAt.Sub(existing.ReceivedAt) >= params.ReplayWindow {
		return true, "", nil
	}
	if existing.State == DeliveryDropped {
		return true, existing.ID, nil
	}
	if existing.State != DeliveryAccepted && existing.State != DeliveryWaiting {
		return false, "", nil
	}
	if alive == nil || alive(existing.Instance) {
		return false, "", nil
	}
	if err := reconcileDelivery(ctx, conn, existing.ID, params.ReceivedAt); err != nil {
		return false, "", err
	}
	var state string
	if err := conn.QueryRowContext(ctx, `SELECT state FROM deliveries WHERE id = ?`, existing.ID).
		Scan(&state); err != nil {
		return false, "", err
	}
	if DeliveryState(state) == DeliveryDropped {
		return true, existing.ID, nil
	}
	return false, "", nil
}

// txlike is what reconcileDelivery and decidedInChain read and write through: a
// connection already inside Accept's own transaction, or — once T051 lands — the
// command-level reconciliation's. Never a *sql.Tx of its own: nesting one inside a
// connection already holding BEGIN IMMEDIATE's write lock would deadlock against itself.
type txlike interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// reconcileDelivery marks dropped every hand-off of deliveryID that no durable record
// decides — a run, a guard refusal other than dropped, or a delivery refusal — including
// one whose own waiting row is still `waiting`, which this ends itself with its own drop
// record rather than through EndWait's own transaction (which would deadlock here), and
// sets the delivery's own state from what that leaves: dropped if any hand-off is,
// handed_off otherwise (data-model.md, *Hand-off*, "On restart"). Used by Accept for the
// one delivery a retry's identity points at, and reused by the command-level
// reconciliation (T051) for every accepted or waiting delivery whose instance lock can be
// taken — after the guard's own waiting reconciliation (G075) has run, which this does
// not do: record does not import guard.
func reconcileDelivery(ctx context.Context, tx txlike, deliveryID string, at time.Time) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT playbook_name, state FROM handoffs WHERE delivery_id = ?`, deliveryID)
	if err != nil {
		return err
	}
	var pending []string
	for rows.Next() {
		var name, state string
		if err := rows.Scan(&name, &state); err != nil {
			_ = rows.Close()
			return err
		}
		if state == string(HandOffPending) || state == string(HandOffWaiting) {
			pending = append(pending, name)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()

	dropped := false
	for _, playbookName := range pending {
		state, decided, err := decidedElsewhere(ctx, tx, deliveryID, playbookName)
		if err != nil {
			return err
		}
		if decided {
			if _, err := tx.ExecContext(ctx, `
				UPDATE handoffs SET state = ?, decided_at = ? WHERE delivery_id = ? AND playbook_name = ?`,
				string(state), formatTime(at), deliveryID, playbookName); err != nil {
				return err
			}
			continue
		}

		var waitingID string
		err = tx.QueryRowContext(ctx, `
			SELECT id FROM waiting_triggers
			 WHERE delivery_id = ? AND playbook_name = ? AND outcome = 'waiting'`,
			deliveryID, playbookName).Scan(&waitingID)
		switch {
		case err == nil:
			if _, err := tx.ExecContext(ctx, `
				UPDATE waiting_triggers SET outcome = ?, outcome_at = ?
				 WHERE id = ? AND outcome = 'waiting'`,
				string(WaitDropped), at.UTC().Format(sortableTime), waitingID); err != nil {
				return err
			}
			id, err := newRecordID()
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO refusals (id, playbook_name, trigger_kind, waiting_trigger_id,
				                      mechanism, detail, refused_at, delivery_id)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				id, playbookName, string(TriggerWebhook), waitingID, string(MechanismDropped),
				fmt.Sprintf("delivery %s: accepting process ended before its wait resolved", deliveryID),
				at.UTC().Format(sortableTime), deliveryID); err != nil {
				return err
			}
		case errors.Is(err, sql.ErrNoRows):
			// Never handed to the guard at all: nothing else to end.
		default:
			return err
		}

		if _, err := tx.ExecContext(ctx, `
			UPDATE handoffs SET state = ?, decided_at = ? WHERE delivery_id = ? AND playbook_name = ?`,
			string(HandOffDropped), formatTime(at), deliveryID, playbookName); err != nil {
			return err
		}
		dropped = true
	}

	state := DeliveryHandedOff
	if dropped {
		state = DeliveryDropped
	}
	_, err = tx.ExecContext(ctx, `UPDATE deliveries SET state = ? WHERE id = ?`, string(state), deliveryID)
	return err
}

// decidedElsewhere reports whether a hand-off no column names as decided already has a
// durable record deciding it anyway — the case a kill between the record and its column
// leaves (data-model.md, *Hand-off*).
func decidedElsewhere(ctx context.Context, tx txlike, deliveryID, playbookName string) (HandOffState, bool, error) {
	var runID string
	err := tx.QueryRowContext(ctx,
		`SELECT id FROM runs WHERE delivery_id = ? AND playbook_name = ? LIMIT 1`,
		deliveryID, playbookName).Scan(&runID)
	switch {
	case err == nil:
		return HandOffHandedOff, true, nil
	case !errors.Is(err, sql.ErrNoRows):
		return "", false, err
	}

	var mechanism string
	err = tx.QueryRowContext(ctx, `
		SELECT mechanism FROM refusals
		 WHERE delivery_id = ? AND playbook_name = ? AND mechanism != ?
		 ORDER BY refused_at DESC LIMIT 1`,
		deliveryID, playbookName, string(MechanismDropped)).Scan(&mechanism)
	switch {
	case err == nil:
		return HandOffRefused, true, nil
	case !errors.Is(err, sql.ErrNoRows):
		return "", false, err
	}

	var refusalID string
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM delivery_refusals WHERE delivery_id = ? AND playbook_name = ? LIMIT 1`,
		deliveryID, playbookName).Scan(&refusalID)
	switch {
	case err == nil:
		return HandOffRefused, true, nil
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	default:
		return "", false, err
	}
}

// decidedInChain is every playbook name with a decided hand-off anywhere in the chain of
// deliveries deliveryID supersedes, walked back to its root (data-model.md, *Delivery
// identity*): a retry of a dropped delivery is handed only to the bound playbooks none of
// them decided.
func decidedInChain(ctx context.Context, tx txlike, deliveryID string) (map[string]bool, error) {
	decided := map[string]bool{}
	for deliveryID != "" {
		rows, err := tx.QueryContext(ctx,
			`SELECT playbook_name, state FROM handoffs WHERE delivery_id = ?`, deliveryID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var name, state string
			if err := rows.Scan(&name, &state); err != nil {
				_ = rows.Close()
				return nil, err
			}
			if state == string(HandOffHandedOff) || state == string(HandOffRefused) {
				decided[name] = true
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		_ = rows.Close()

		var supersedes sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT supersedes FROM deliveries WHERE id = ?`, deliveryID).
			Scan(&supersedes); err != nil {
			return nil, err
		}
		deliveryID = supersedes.String
	}
	return decided, nil
}

// newDeliveryID mints a delivery identifier in the run identifier's shape (data-model.md,
// *Delivery*), so the blob store files its body the way a run's artifacts are filed.
// Duplicated from internal/run's own generator rather than imported: internal/run imports
// internal/record, so importing it back here would cycle.
func newDeliveryID(at time.Time) (string, error) {
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		return "", fmt.Errorf("generating a delivery identifier: %w", err)
	}
	return at.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(suffix), nil
}
