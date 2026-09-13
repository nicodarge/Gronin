package index

import (
	"context"
	"fmt"
)

// SetSeams installs the two points an update can be held at: after it has read the
// generation it will change, and inside its write transaction once the difference is
// applied and before the generation row is written.
func SetSeams(ix *Index, afterRead, inTransaction func()) {
	ix.seams.afterRead, ix.seams.inTransaction = afterRead, inTransaction
}

// SetWritable replaces the check of whether this process can write an index file, for as
// long as the test runs. The suite runs as root inside its network namespace, and root
// writes a file whatever its mode, so a file made unwritable by its mode cannot stand in.
func SetWritable(t interface{ Cleanup(func()) }, check func(file string) error) {
	previous := writable
	writable = check
	t.Cleanup(func() { writable = previous })
}

// SetBusySeam installs what an operation calls each time it finds the index held by
// another connection, before it waits and tries again.
func SetBusySeam(ix *Index, busy func()) {
	ix.seams.busy = busy
}

// Dump is everything an index holds, one line per row in a fixed order, so two indexes
// can be compared whole rather than through what a search happens to return.
func Dump(ctx context.Context, ix *Index) ([]string, error) {
	var lines []string
	var generation, identity string
	err := ix.db.QueryRowContext(ctx, `SELECT generation, identity FROM generation WHERE id = 1`).
		Scan(&generation, &identity)
	if err != nil {
		return nil, err
	}
	lines = append(lines, "generation "+generation+" "+identity)

	documents, err := ix.db.QueryContext(ctx, `SELECT source, digest, bytes FROM documents ORDER BY source`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = documents.Close() }()
	for documents.Next() {
		var source, digest string
		var size int64
		if err := documents.Scan(&source, &digest, &size); err != nil {
			return nil, err
		}
		lines = append(lines, fmt.Sprintf("document %s %s %d", source, digest, size))
	}
	if err := documents.Err(); err != nil {
		return nil, err
	}

	passages, err := ix.db.QueryContext(ctx,
		`SELECT source, ordinal, start, text FROM passages ORDER BY source, ordinal, rowid`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = passages.Close() }()
	for passages.Next() {
		var source, text string
		var ordinal, start int64
		if err := passages.Scan(&source, &ordinal, &start, &text); err != nil {
			return nil, err
		}
		lines = append(lines, fmt.Sprintf("passage %s %d %d %q", source, ordinal, start, text))
	}
	return lines, passages.Err()
}
