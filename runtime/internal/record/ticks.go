package record

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Tick is the last scheduled occurrence of a playbook that took its claim (FR-128), on a
// deployment with no coordination backend. Only DueAt is ever compared; the holder is
// kept to be named in a refusal.
type Tick struct {
	DueAt    time.Time
	Host     string
	Instance string
	RunID    string
}

// LastTick reads a playbook's last tick, and reports whether one was recorded. It is
// meant to be read only while the playbook's file lock is held: read outside it, the
// answer can be overtaken before it is acted on.
func (s *Store) LastTick(ctx context.Context, playbookName string) (Tick, bool, error) {
	var (
		tick Tick
		due  string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT due_at, host, instance, run_id FROM last_ticks WHERE playbook_name = ?`,
		playbookName).Scan(&due, &tick.Host, &tick.Instance, &tick.RunID)
	if errors.Is(err, sql.ErrNoRows) {
		return Tick{}, false, nil
	}
	if err != nil {
		return Tick{}, false, fmt.Errorf("reading the last tick of %s: %w", playbookName, err)
	}
	tick.DueAt = parseTime(sql.NullString{String: due, Valid: true})
	if tick.DueAt.IsZero() {
		return Tick{}, false, fmt.Errorf("the last tick of %s is not a time: %q", playbookName, due)
	}
	return tick, true, nil
}

// SetLastTick records a playbook's last tick, replacing the one before it. Written under
// the same file lock as LastTick is read.
func (s *Store) SetLastTick(ctx context.Context, playbookName string, tick Tick) error {
	if tick.DueAt.IsZero() {
		return fmt.Errorf("the last tick of %s: no due instant", playbookName)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO last_ticks (playbook_name, due_at, host, instance, run_id)
		VALUES (?, ?, ?, ?, ?)`,
		playbookName, tick.DueAt.UTC().Format(sortableTime), s.redactor.Redact(tick.Host),
		s.redactor.Redact(tick.Instance), tick.RunID)
	if err != nil {
		return fmt.Errorf("recording the last tick of %s: %w", playbookName, err)
	}
	return nil
}
