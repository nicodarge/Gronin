package record

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"sort"
)

//go:embed migrations/*.sql
var migrations embed.FS

// migrate applies every migration the database has not seen, in name order, each in its
// own transaction. Names are numbered and never reused: renumbering to tidy the
// directory would re-apply a migration somewhere and skip one elsewhere, silently.
func migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		name       TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("creating the migration table: %w", err)
	}

	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		var applied int
		if err := db.QueryRowContext(ctx,
			`SELECT count(*) FROM schema_migrations WHERE name = ?`, name,
		).Scan(&applied); err != nil {
			return err
		}
		if applied > 0 {
			continue
		}

		statements, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(statements)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("applying %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`, name, nowUTC(),
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("recording %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("committing %s: %w", name, err)
		}
	}
	return nil
}
