package record

import (
	"context"
	"time"
)

// CountRateStarts is a single-host rate window (FR-114): how many runs of playbookName
// have started at or after since. Read on the wall reading of the clock that calls it.
func (s *Store) CountRateStarts(ctx context.Context, playbookName string, since time.Time) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM rate_starts WHERE playbook_name = ? AND started_at >= ?`,
		s.redactor.Redact(playbookName), since.UTC().Format(sortableTime)).Scan(&count)
	return count, err
}

// RecordRateStart takes one of a playbook's rate slots, at the instant a run started
// (FR-115). It is never deleted: a slot frees only once the window moves past it (C9), not
// when the run it belongs to ends.
func (s *Store) RecordRateStart(ctx context.Context, playbookName, runID string, startedAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO rate_starts (playbook_name, run_id, started_at) VALUES (?, ?, ?)`,
		s.redactor.Redact(playbookName), runID, startedAt.UTC().Format(sortableTime))
	return err
}
