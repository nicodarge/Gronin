package record

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// DeliveryRefusalReason is why a request did not become a delivery, or a delivery did not
// become a hand-off for one playbook (FR-332).
type DeliveryRefusalReason string

// Every reason a delivery refusal names, as data-model.md's *Delivery refusal* lists them.
const (
	ReasonBodyNotJSON       DeliveryRefusalReason = "body_not_json"
	ReasonIdentityAbsent    DeliveryRefusalReason = "identity_absent"
	ReasonIdentityNotSingle DeliveryRefusalReason = "identity_not_single"
	ReasonValueAbsent       DeliveryRefusalReason = "value_absent"
	ReasonValueNotSingle    DeliveryRefusalReason = "value_not_single"
	ReasonValueTooLong      DeliveryRefusalReason = "value_too_long"
	ReasonValueNoMatch      DeliveryRefusalReason = "value_no_match"
	ReasonValueLeadingDash  DeliveryRefusalReason = "value_leading_dash"
)

// DeliveryRefusal is an authenticated request that did not become a delivery, or a
// delivery whose value one playbook refused (data-model.md, *Delivery refusal*). One row
// each — a repeat is not a refusal (FR-317).
type DeliveryRefusal struct {
	ID     string
	Source string
	Reason DeliveryRefusalReason
	// ValueName is the declared value concerned, set for a value refusal.
	ValueName string
	// PlaybookName is set for a value refusal: the delivery was accepted, and this is one
	// playbook's refusal of it.
	PlaybookName string
	// DeliveryID is set for a value refusal.
	DeliveryID string
	// ReceivedAt is the host clock's wall reading.
	ReceivedAt time.Time
	// Peer is the connection's own address.
	Peer string
	// BodyRef is set for a request that did not become a delivery; a value refusal points
	// at its delivery's own body instead and carries none of its own.
	BodyRef string
}

// AddDeliveryRefusal writes one authenticated refusal, filling in its identifier when
// empty. body is written under the refusal's own identifier when given, and left unset
// for a value refusal, whose body is the delivery's.
func (s *Store) AddDeliveryRefusal(ctx context.Context, refusal DeliveryRefusal, body []byte) (string, error) {
	if refusal.ID == "" {
		id, err := newRecordID()
		if err != nil {
			return "", err
		}
		refusal.ID = id
	}
	if refusal.ReceivedAt.IsZero() {
		return "", fmt.Errorf("delivery refusal %s: no time it was received at", refusal.ID)
	}

	if len(body) > 0 {
		ref, err := s.blobs.Put(refusal.ID, "body", body)
		if err != nil {
			return "", err
		}
		refusal.BodyRef = ref
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO delivery_refusals (id, source, reason, value_name, playbook_name,
		                               delivery_id, received_at, peer, body_ref)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		refusal.ID, s.redactor.Redact(refusal.Source), string(refusal.Reason),
		nullable(s.redactor.Redact(refusal.ValueName)), nullable(s.redactor.Redact(refusal.PlaybookName)),
		nullable(refusal.DeliveryID),
		refusal.ReceivedAt.UTC().Format(sortableTime), refusal.Peer, nullable(refusal.BodyRef))
	if err != nil {
		return "", fmt.Errorf("recording delivery refusal %s: %w", refusal.ID, err)
	}
	return refusal.ID, nil
}

// ListDeliveryRefusals returns authenticated refusals most recent first.
func (s *Store) ListDeliveryRefusals(ctx context.Context, limit int) ([]DeliveryRefusal, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, source, reason, value_name, playbook_name, delivery_id, received_at, peer,
		       body_ref
		  FROM delivery_refusals ORDER BY received_at DESC, rowid DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var refusals []DeliveryRefusal
	for rows.Next() {
		var (
			refusal                             DeliveryRefusal
			reason, received                    string
			valueName, playbookName, deliveryID sql.NullString
			bodyRef                             sql.NullString
		)
		if err := rows.Scan(&refusal.ID, &refusal.Source, &reason, &valueName, &playbookName,
			&deliveryID, &received, &refusal.Peer, &bodyRef); err != nil {
			return nil, err
		}
		refusal.Reason = DeliveryRefusalReason(reason)
		refusal.ValueName, refusal.PlaybookName = valueName.String, playbookName.String
		refusal.DeliveryID, refusal.BodyRef = deliveryID.String, bodyRef.String
		refusal.ReceivedAt = parseTime(sql.NullString{String: received, Valid: true})
		refusals = append(refusals, refusal)
	}
	return refusals, rows.Err()
}

// UnauthenticatedRefusalCount is a request refused before its signature passed
// (data-model.md, *Unauthenticated refusal count*). Never a row per request: anyone who
// can reach the ingress can send one.
type UnauthenticatedRefusalCount struct {
	// IntervalStart is the wall reading truncated to the counting interval by the caller.
	IntervalStart time.Time
	Reason        string
	// SourceBucket is a configured source's name, or "(unconfigured)".
	SourceBucket string
	Count        int
	LastPeer     string
	LastAt       time.Time
}

// CountUnauthenticated is one upsert adding to the row for its key
// (interval, reason, bucket): the first request in an interval inserts it, and every one
// after adds to its count and updates the last peer seen. So a flood adds at most one row
// per reason and bucket per interval, whatever the number of requests (FR-333).
func (s *Store) CountUnauthenticated(
	ctx context.Context, interval time.Time, reason, bucket, peer string, at time.Time,
) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO ingress_refusal_counts (interval_start, reason, source_bucket, count,
		                                    last_peer, last_at)
		VALUES (?, ?, ?, 1, ?, ?)
		ON CONFLICT(interval_start, reason, source_bucket)
		DO UPDATE SET count = count + 1, last_peer = excluded.last_peer, last_at = excluded.last_at`,
		interval.UTC().Format(sortableTime), reason, bucket, peer, at.UTC().Format(sortableTime))
	if err != nil {
		return fmt.Errorf("counting an unauthenticated refusal (%s, %s): %w", reason, bucket, err)
	}
	return nil
}

// ListUnauthenticatedCounts returns unauthenticated refusal counts, most recent interval
// first.
func (s *Store) ListUnauthenticatedCounts(ctx context.Context, limit int) ([]UnauthenticatedRefusalCount, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT interval_start, reason, source_bucket, count, last_peer, last_at
		  FROM ingress_refusal_counts
		 ORDER BY interval_start DESC, reason, source_bucket LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var counts []UnauthenticatedRefusalCount
	for rows.Next() {
		var (
			count            UnauthenticatedRefusalCount
			interval, lastAt string
		)
		if err := rows.Scan(&interval, &count.Reason, &count.SourceBucket, &count.Count,
			&count.LastPeer, &lastAt); err != nil {
			return nil, err
		}
		count.IntervalStart = parseTime(sql.NullString{String: interval, Valid: true})
		count.LastAt = parseTime(sql.NullString{String: lastAt, Valid: true})
		counts = append(counts, count)
	}
	return counts, rows.Err()
}
