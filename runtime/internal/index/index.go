package index

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	// The driver the record store already links; FTS5 is compiled into it.
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

//go:embed schema.sql
var schema string

// Tokenizer is the one tokenizer every collection uses (research.md §10). It is named in
// schema.sql and carried in the configuration identity, so changing it rebuilds an index
// rather than mixing two tokenizations in one.
const Tokenizer = "unicode61"

// Configuration is what decides how a collection's passages are cut and tokenized. The
// timeouts are not part of it: allowing a slower source changes nothing the index holds.
type Configuration struct {
	SourceKind string
	Source     string
}

// DirectoryConfiguration is the configuration of a collection over a directory.
func DirectoryConfiguration(dir string) Configuration {
	return Configuration{SourceKind: "directory", Source: dir}
}

// Identity is a SHA-256 over a canonical encoding of the configuration, the passage rule
// and the tokenizer. An index stored under another identity is treated as empty (FR-217).
func (c Configuration) Identity() string {
	canonical, err := json.Marshal(struct {
		PassageRule string `json:"passage_rule"`
		Tokenizer   string `json:"tokenizer"`
		SourceKind  string `json:"source_kind"`
		Source      string `json:"source"`
	}{PassageRule, Tokenizer, c.SourceKind, c.Source})
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

// Generation is one complete state of an index. Its ID is derived from the identity and
// the documents it was built from, so two builds of the same sources carry the same ID
// whichever path built them.
type Generation struct {
	ID       string
	Identity string
	BuiltAt  time.Time
}

// Short is the generation as the operator's surface names it.
func (g Generation) Short() string {
	if len(g.ID) < 12 {
		return g.ID
	}
	return g.ID[:12]
}

// GenerationOf is the ID of a generation built under identity from documents.
func GenerationOf(identity string, documents []Document) string {
	pairs := make([][2]string, 0, len(documents))
	for _, document := range documents {
		pairs = append(pairs, [2]string{document.Source, document.Digest})
	}
	return generationOfPairs(identity, pairs)
}

func generationOfPairs(identity string, pairs [][2]string) string {
	sort.Slice(pairs, func(i, j int) bool { return pairs[i][0] < pairs[j][0] })
	var digest = sha256.New()
	_, _ = digest.Write([]byte(identity))
	for _, pair := range pairs {
		_, _ = fmt.Fprintf(digest, "\n%s\x00%s", pair[0], pair[1])
	}
	return hex.EncodeToString(digest.Sum(nil))
}

// Index is one collection's database, open for updating and searching.
type Index struct {
	db       *sql.DB
	identity string
	seams    seams
	now      func() time.Time
}

// seams are where a test holds an update, or learns that it found the index held. Nil
// outside tests.
type seams struct {
	afterRead     func()
	inTransaction func()
	busy          func()
}

func (ix *Index) seam(at func()) {
	if at != nil {
		at()
	}
}

// busyPause is how long an operation that found the index held waits before trying again.
const busyPause = 20 * time.Millisecond

// retryBusy runs operation until it does not find the database held by another
// connection, or until ctx ends. The wait is the caller's bound — a retrieval's own
// remaining time — and not a figure of this package's: SQLite's busy_timeout is kept to a
// slice, because a busy handler sleeping inside SQLite does not notice a context ending.
func retryBusy(ctx context.Context, busy func(), operation func() error) error {
	for {
		err := operation()
		if !isBusy(err) {
			return err
		}
		if busy != nil {
			busy()
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("the index is held by another process: %w", errors.Join(ctx.Err(), err))
		case <-time.After(busyPause):
		}
	}
}

func isBusy(err error) bool {
	var held *sqlite.Error
	return errors.As(err, &held) && held.Code()&0xff == sqlite3.SQLITE_BUSY
}

func (ix *Index) whileBusy(ctx context.Context, operation func() error) error {
	return retryBusy(ctx, ix.seams.busy, operation)
}

// name is what a collection may be called, checked again here because it becomes a path.
var name = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func path(dir, collection string) (string, error) {
	if !name.MatchString(collection) {
		return "", fmt.Errorf("collection name %q cannot name an index file", collection)
	}
	return filepath.Join(dir, collection+".db"), nil
}

// private creates an index file readable by its owner alone, and narrows one created wider
// before. An index holds the collection's text unredacted, and SQLite gives its journal
// the database file's mode.
func private(file string) error {
	handle, err := os.OpenFile(file, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // the index path, from a checked collection name
	if err != nil {
		return err
	}
	if err := handle.Close(); err != nil {
		return err
	}
	return os.Chmod(file, 0o600)
}

// Open opens or creates a collection's index under dir.
//
// Writes take the database's write lock when they begin, not when they first write: two
// processes updating one collection then wait for each other at the start, rather than
// one finding at its first write that the other got there first. How long they wait is
// the context's.
func Open(ctx context.Context, dir, collection string, cfg Configuration) (*Index, error) {
	file, err := path(dir, collection)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating the index directory: %w", err)
	}
	if err := private(file); err != nil {
		return nil, fmt.Errorf("creating the index of %s: %w", collection, err)
	}
	db, err := sql.Open("sqlite", "file:"+file+"?_pragma=busy_timeout(100)&_txlock=immediate")
	if err != nil {
		return nil, fmt.Errorf("opening the index of %s: %w", collection, err)
	}
	if err := retryBusy(ctx, nil, func() error {
		_, err := db.ExecContext(ctx, schema)
		return err
	}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("opening the index of %s: %w", collection, err)
	}
	return &Index{
		db: db, identity: cfg.Identity(),
		now: func() time.Time { return time.Now().UTC() },
	}, nil
}

// Close releases the database.
func (ix *Index) Close() error { return ix.db.Close() }

// Stored is what an index holds, without its passages.
type Stored struct {
	Generation Generation
	Documents  []StoredDocument
}

// StoredDocument is one document a generation was built from.
type StoredDocument struct {
	Source string
	Digest string
	Bytes  int64
}

// ReadStored reads a collection's index, and reports false when no index exists. Listing a
// collection is not a request to index it (FR-221): it writes nothing to the index, with
// one exception. The connection is read-write, never read-only, because a rebuild killed
// mid-transaction leaves a hot journal, and only a connection that can write rolls it back
// to the previous generation — a read-only one refuses to read at all (SC-211).
func ReadStored(ctx context.Context, dir, collection string) (Stored, bool, error) {
	file, err := path(dir, collection)
	if err != nil {
		return Stored{}, false, err
	}
	if _, err := os.Stat(file); errors.Is(err, os.ErrNotExist) {
		return Stored{}, false, nil
	}
	db, err := sql.Open("sqlite", "file:"+file+"?mode=rw&_pragma=busy_timeout(100)")
	if err != nil {
		return Stored{}, false, err
	}
	defer func() { _ = db.Close() }()
	var stored Stored
	err = retryBusy(ctx, nil, func() error {
		var err error
		stored, err = readStored(ctx, db)
		return err
	})
	if err != nil {
		return Stored{}, true, fmt.Errorf("reading the index of %s: %w", collection, err)
	}
	return stored, true, nil
}

// querier is a database or a transaction.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func readGeneration(ctx context.Context, q querier) (Generation, error) {
	var generation Generation
	var builtAt string
	err := q.QueryRowContext(ctx, `SELECT generation, identity, built_at FROM generation WHERE id = 1`).
		Scan(&generation.ID, &generation.Identity, &builtAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Generation{}, nil
	}
	if err != nil {
		return Generation{}, err
	}
	generation.BuiltAt, err = time.Parse(time.RFC3339Nano, builtAt)
	return generation, err
}

// readStored reads the generation and its documents inside one read transaction, so the
// two cannot come from different generations.
func readStored(ctx context.Context, db *sql.DB) (Stored, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Stored{}, err
	}
	defer func() { _ = tx.Rollback() }()

	generation, err := readGeneration(ctx, tx)
	if err != nil {
		return Stored{}, err
	}
	stored := Stored{Generation: generation}
	rows, err := tx.QueryContext(ctx, `SELECT source, digest, bytes FROM documents ORDER BY source`)
	if err != nil {
		return Stored{}, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var document StoredDocument
		if err := rows.Scan(&document.Source, &document.Digest, &document.Bytes); err != nil {
			return Stored{}, err
		}
		stored.Documents = append(stored.Documents, document)
	}
	return stored, rows.Err()
}

// Comparison is a walk set against the generation an index holds, which is what the
// operator's listing shows (FR-215).
type Comparison struct {
	// Target is the generation a walk of the sources as they stand would build.
	Target string
	// Changed is whether that differs from the generation the index holds.
	Changed         bool
	Added, Modified int
	Removed         []string
	marks           map[string]string
	indexed         bool
}

// Mark is how a walked document stands against the stored generation.
func (c Comparison) Mark(source string) string {
	if !c.indexed {
		return "not indexed"
	}
	return c.marks[source]
}

// Compare sets a walk against what an index holds, writing nothing.
func Compare(cfg Configuration, stored Stored, walk Walk) Comparison {
	identity := cfg.Identity()
	comparison := Comparison{
		Target:  GenerationOf(identity, walk.Documents),
		marks:   map[string]string{},
		indexed: stored.Generation.ID != "",
	}
	comparison.Changed = comparison.Target != stored.Generation.ID

	held := map[string]string{}
	if stored.Generation.Identity == identity {
		for _, document := range stored.Documents {
			held[document.Source] = document.Digest
		}
	}
	present := map[string]bool{}
	for _, document := range walk.Documents {
		present[document.Source] = true
		digest, found := held[document.Source]
		switch {
		case !found:
			comparison.marks[document.Source] = "added"
			comparison.Added++
		case digest != document.Digest:
			comparison.marks[document.Source] = "changed"
			comparison.Modified++
		default:
			comparison.marks[document.Source] = "unchanged"
		}
	}
	for _, document := range stored.Documents {
		if !present[document.Source] || stored.Generation.Identity != identity {
			comparison.Removed = append(comparison.Removed, document.Source)
		}
	}
	return comparison
}

// errMoved is an update whose read is stale: another update committed a generation
// between its read and its write.
var errMoved = errors.New("another update committed a generation since this one read it")

// Update brings the index up to date with a walk of its sources (FR-217). It works the
// difference out from a read of the stored generation, then inside one write transaction
// checks that generation is still the one it read — working the difference out again if
// another update committed meanwhile — and applies it with the new generation's row. A
// search therefore sees the generation before or the one after, never part of either
// (FR-219). An index stored under another identity is emptied first.
func (ix *Index) Update(ctx context.Context, walk Walk) (Generation, error) {
	return ix.write(ctx, walk, false)
}

// Rebuild replaces everything the index holds with a walk of its sources, in one write
// transaction, whatever it held before (FR-220).
func (ix *Index) Rebuild(ctx context.Context, walk Walk) (Generation, error) {
	return ix.write(ctx, walk, true)
}

func (ix *Index) write(ctx context.Context, walk Walk, rebuild bool) (Generation, error) {
	target := GenerationOf(ix.identity, walk.Documents)
	for {
		if err := ctx.Err(); err != nil {
			return Generation{}, err
		}
		var read Stored
		if err := ix.whileBusy(ctx, func() error {
			var err error
			read, err = readStored(ctx, ix.db)
			return err
		}); err != nil {
			return Generation{}, err
		}
		if !rebuild && read.Generation.ID == target {
			return read.Generation, nil
		}
		ix.seam(ix.seams.afterRead)

		var generation Generation
		err := ix.whileBusy(ctx, func() error {
			var err error
			generation, err = ix.commit(ctx, read.Generation.ID,
				difference(ix.identity, read, walk, rebuild), target)
			return err
		})
		if errors.Is(err, errMoved) {
			continue
		}
		return generation, err
	}
}

// change is what an update applies: everything removed first, or the named sources, then
// the documents added, whose text is read from the walk's directory as each is indexed.
type change struct {
	everything bool
	remove     []string
	add        []Document
	walk       Walk
}

func difference(identity string, read Stored, walk Walk, rebuild bool) change {
	if rebuild || read.Generation.Identity != identity {
		return change{everything: true, add: walk.Documents, walk: walk}
	}
	held := make(map[string]string, len(read.Documents))
	for _, document := range read.Documents {
		held[document.Source] = document.Digest
	}

	diff := change{walk: walk}
	present := make(map[string]bool, len(walk.Documents))
	for _, document := range walk.Documents {
		present[document.Source] = true
		digest, found := held[document.Source]
		switch {
		case !found:
			diff.add = append(diff.add, document)
		case digest != document.Digest:
			diff.remove = append(diff.remove, document.Source)
			diff.add = append(diff.add, document)
		}
	}
	for _, document := range read.Documents {
		if !present[document.Source] {
			diff.remove = append(diff.remove, document.Source)
		}
	}
	return diff
}

func (c change) apply(ctx context.Context, tx *sql.Tx) error {
	if c.everything {
		for _, statement := range []string{`DELETE FROM passages`, `DELETE FROM documents`} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
	}
	for _, source := range c.remove {
		if _, err := tx.ExecContext(ctx, `DELETE FROM passages WHERE source = ?`, source); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM documents WHERE source = ?`, source); err != nil {
			return err
		}
	}
	for _, document := range c.add {
		content, err := c.walk.contentOf(document)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO documents (source, digest, bytes) VALUES (?, ?, ?)`,
			document.Source, document.Digest, document.Bytes); err != nil {
			return fmt.Errorf("indexing %s: %w", document.Source, err)
		}
		for _, passage := range TextPassages(document.Source, content) {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO passages (text, source, ordinal, start) VALUES (?, ?, ?, ?)`,
				passage.Text, passage.Source, passage.Ordinal, passage.Offset); err != nil {
				return fmt.Errorf("indexing %s: %w", document.Source, err)
			}
		}
	}
	return nil
}

func (ix *Index) commit(ctx context.Context, readID string, diff change, target string) (Generation, error) {
	tx, err := ix.db.BeginTx(ctx, nil)
	if err != nil {
		return Generation{}, err
	}
	defer func() { _ = tx.Rollback() }()

	current, err := readGeneration(ctx, tx)
	if err != nil {
		return Generation{}, err
	}
	if current.ID != readID {
		return Generation{}, errMoved
	}
	if err := diff.apply(ctx, tx); err != nil {
		return Generation{}, err
	}
	ix.seam(ix.seams.inTransaction)

	next := Generation{ID: target, Identity: ix.identity, BuiltAt: ix.now()}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO generation (id, generation, identity, built_at) VALUES (1, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET generation = excluded.generation,
		    identity = excluded.identity, built_at = excluded.built_at`,
		next.ID, next.Identity, next.BuiltAt.Format(time.RFC3339Nano)); err != nil {
		return Generation{}, err
	}
	if err := tx.Commit(); err != nil {
		return Generation{}, err
	}
	return next, nil
}
