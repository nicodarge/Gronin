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
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
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

// ReportsConfiguration is the configuration of a collection over the recorded reports of
// the named playbooks.
func ReportsConfiguration(playbooks []string) Configuration {
	return Configuration{SourceKind: "reports", Source: strings.Join(playbooks, ",")}
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

// seams are where a test holds an update, or learns what it is doing. Nil outside tests.
type seams struct {
	afterRead     func()
	inTransaction func()
	busy          func()
	read          func(source string)
}

func (ix *Index) seam(at func()) {
	if at != nil {
		at()
	}
}

// ErrHeld is an index another connection held for as long as the caller was willing to wait.
var ErrHeld = errors.New("the index is held by another connection")

// busyPause is how long an operation that found the index held waits before trying again.
var busyPause = 20 * time.Millisecond

// beginError is a transaction that could not begin: the one failure that means an operation
// never took the lock it waited for.
type beginError struct{ err error }

func (e *beginError) Error() string { return e.err.Error() }

func (e *beginError) Unwrap() error { return e.err }

func isBegin(err error) bool {
	var begin *beginError
	return errors.As(err, &begin)
}

// beginFailure marks err a begin failure when it is either a genuine BUSY or exactly ctx's
// own error — the two ways BeginTx, or the first statement of a deferred read-only
// transaction, never takes the lock it waited for: a real BUSY, or database/sql refusing to
// even ask the driver because ctx had already ended by the time the call was made, the way
// a retry attempt starting just after retryBusy's own bound check does. An unrelated
// failure (disk I/O, corruption, a permission error, schema drift) is neither, and is left
// exactly as it is: it is not the index held, whatever ctx happens to be doing at the same
// moment.
func beginFailure(ctx context.Context, err error) error {
	if err == nil || isBegin(err) {
		return err
	}
	if isBusy(err) || (ctx.Err() != nil && errors.Is(err, ctx.Err())) {
		return &beginError{err}
	}
	return err
}

// afterDeferredBegin runs once a deferred read-only transaction has begun, before its
// first read — the point up to which a failure is still the transaction never having taken
// the lock it waited for (see beginFailure). Nil outside tests; a test uses it to end ctx
// deterministically at exactly that point, rather than racing a live clock against it.
//
// It is a package-level var, not one of Index's own seams, because readStored is a free
// function ReadStored calls with no *Index to hold one: a listing reads an index it has not
// opened. A test installing it must not run in parallel with another exercising this seam.
var afterDeferredBegin = func() {}

// commitBusyError is COMMIT giving up on a reader after waiting out commitWait: the
// transaction, still open (SQLite does not roll back a failed COMMIT), is exactly as it
// was when the reader appeared, and the deferred rollback returns it to the previous
// generation unchanged. Retrying would redo every read and write already applied, so this
// is reported once, as a plain terminal failure, never fed back into the busy-retry loop
// and never relabeled ErrHeld — it is not another connection holding the index, it is one
// reading it.
type commitBusyError struct {
	wait time.Duration
	err  error
}

func (e *commitBusyError) Error() string {
	return fmt.Sprintf("COMMIT waited %s for a reader to let go of the index and gave up; "+
		"the previous generation stays in place: %s", e.wait, e.err)
}

func (e *commitBusyError) Unwrap() error { return e.err }

// retryBusy runs operation until it does not find the index held, or until ctx ends. Both
// points that can end the loop on ctx call heldAtBound unconditionally — it is heldAtBound's
// own isBegin gate that decides what gets relabeled ErrHeld — so a failure that outlives
// ctx is reported once either way, rather than fed back to operation forever because
// neither point returned.
func retryBusy(ctx context.Context, busy func(), operation func() error) error {
	held := false
	for {
		err := operation()
		var atCommit *commitBusyError
		if errors.As(err, &atCommit) {
			return err
		}
		if held && ctx.Err() != nil {
			return heldAtBound(ctx, err)
		}
		if !isBusy(err) {
			return err
		}
		held = true
		if busy != nil {
			busy()
		}
		select {
		case <-ctx.Done():
		case <-time.After(busyPause):
		}
		if ctx.Err() != nil {
			return heldAtBound(ctx, err)
		}
	}
}

// heldAtBound is how a wait for a held index ends when the caller's context does: a bound
// that ran out on a transaction that never began is the index held; a bound that ran out
// on anything else — a BUSY at COMMIT among them — is that failure with the bound named,
// not relabeled; and a cancel is the caller's own either way.
func heldAtBound(ctx context.Context, err error) error {
	if isBegin(err) && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w: %w", ErrHeld, errors.Join(ctx.Err(), err))
	}
	return errors.Join(ctx.Err(), err)
}

func isBusy(err error) bool {
	var held *sqlite.Error
	return errors.As(err, &held) && held.Code()&0xff == sqlite3.SQLITE_BUSY
}

func (ix *Index) whileBusy(ctx context.Context, operation func() error) error {
	return retryBusy(ctx, ix.seams.busy, operation)
}

// unboundedCommitWait is how long COMMIT waits for a reader when the caller sets no bound
// of its own, as for a rebuild: for as long as the writer takes (cli.md).
var unboundedCommitWait = 365 * 24 * time.Hour

// commitWait is how long COMMIT may wait for a reader before giving up: the caller's own
// remaining bound in full, never a cap below it, or unboundedCommitWait when the caller
// set none.
var commitWait = func(ctx context.Context) time.Duration {
	if deadline, bounded := ctx.Deadline(); bounded {
		return max(time.Until(deadline), time.Millisecond)
	}
	return unboundedCommitWait
}

// name is what a collection may be called, checked again here because it becomes a path.
var name = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func path(dir, collection string) (string, error) {
	if !name.MatchString(collection) {
		return "", fmt.Errorf("collection name %q cannot name an index file", collection)
	}
	return filepath.Join(dir, collection+".db"), nil
}

// private creates an index file readable by its owner alone. An index holds the
// collection's text unredacted, and SQLite gives its journal the database file's mode.
//
// A file that already exists at a wider mode is narrowed only when this user owns it:
// chmod on another user's file fails, and using that file as it is would leave the text
// where others can read it, so it is refused naming the owner and the mode. A file this
// process cannot write is refused naming why, since the likeliest cause is a
// `gronin collections` command run as another user than the deployment's.
func private(file string) error {
	info, err := os.Stat(file)
	if errors.Is(err, os.ErrNotExist) {
		handle, createErr := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // the index path, from a checked collection name
		if createErr == nil {
			return handle.Close()
		}
		if !errors.Is(createErr, os.ErrExist) {
			return createErr
		}
		info, err = os.Stat(file)
	}
	if err != nil {
		return err
	}
	owner := ownerOf(info)
	wide := info.Mode().Perm()&^0o600 != 0
	if wide && owner != os.Getuid() {
		return fmt.Errorf("%s belongs to uid %d at mode %o, wider than 600, and this process, uid %d, "+
			"cannot narrow it; an index holds the collection's text unredacted, so "+
			"gronin collections has to run as the deployment's user", file, owner, info.Mode().Perm(), os.Getuid())
	}
	if wide {
		if err := os.Chmod(file, 0o600); err != nil {
			return err
		}
	}
	if err := writable(file); err != nil {
		return fmt.Errorf("%s belongs to uid %d and this process, uid %d, cannot write it; "+
			"gronin collections has to run as the deployment's user: %w", file, owner, os.Getuid(), err)
	}
	return nil
}

// writable is whether this process can write an index file.
var writable = func(file string) error {
	return unix.Access(file, unix.W_OK)
}

// ownerOf is the uid owning an index file, or -1 where the platform does not say.
var ownerOf = func(info os.FileInfo) int {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return int(stat.Uid)
	}
	return -1
}

// Open opens or creates a collection's index under dir.
//
// Writes take the database's write lock when they begin, not when they first write: two
// processes updating one collection then wait for each other at the start, rather than
// one finding at its first write that the other got there first. How long they wait is
// the context's, not SQLite's own busy_timeout — zero everywhere but COMMIT, since a busy
// handler inside SQLite ignores the context ending.
func Open(ctx context.Context, dir, collection string, cfg Configuration) (*Index, error) {
	file, err := path(dir, collection)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating the index directory: %w", err)
	}
	if err := private(file); err != nil {
		return nil, fmt.Errorf("opening the index of %s: %w", collection, err)
	}
	db, err := sql.Open("sqlite", "file:"+file+"?_pragma=busy_timeout(0)&_txlock=immediate")
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
//
// It waits for an update holding the index for as long as ctx allows, and no longer.
func ReadStored(ctx context.Context, dir, collection string) (Stored, bool, error) {
	file, err := path(dir, collection)
	if err != nil {
		return Stored{}, false, err
	}
	if _, err := os.Stat(file); errors.Is(err, os.ErrNotExist) {
		return Stored{}, false, nil
	}
	db, err := sql.Open("sqlite", "file:"+file+"?mode=rw&_pragma=busy_timeout(0)")
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
//
// A database with no generation table is not indexed yet: a first retrieval creates the
// file before it writes the schema into it, and a listing can land in between.
//
// A read-only BeginTx is deferred: SQLite takes no lock at BEGIN, only at the first
// statement that actually reads. So a genuine BUSY before this returns — not only
// BeginTx's own — can be that first statement finding the index held, and is reported
// that way; anything else stays exactly as it is, since it is not the index held.
//
// Neither BeginTx nor that first read statement has taken a real lock yet, so a failure at
// either is a begin failure whether it is a genuine BUSY or the bare context error
// database/sql returns when ctx already ended before the call could even ask the driver
// anything — the way a retry attempt starting just after retryBusy's own bound check does.
// A later statement failing after the lock was actually taken (a commit-side BUSY, an I/O
// error, a missing table) is not marked this way, and neither is an unrelated failure here
// (see beginFailure): it is not the index held, whether the index is still busy or the
// caller's bound simply ran out first — so it is retryBusy's own held state, not this
// marking, that decides whether that becomes ErrHeld.
func readStored(ctx context.Context, db *sql.DB) (stored Stored, err error) {
	tx, beginErr := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if beginErr != nil {
		return Stored{}, beginFailure(ctx, beginErr)
	}
	defer func() { _ = tx.Rollback() }()
	afterDeferredBegin()

	var tables int
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'generation'`,
	).Scan(&tables); err != nil {
		return Stored{}, beginFailure(ctx, err)
	}
	if tables == 0 {
		return Stored{}, nil
	}

	generation, err := readGeneration(ctx, tx)
	if err != nil {
		return Stored{}, err
	}
	stored = Stored{Generation: generation}
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

// maxRewalks is how often an update with no deadline walks its sources again for a file
// that keeps changing while it is read. An update with a deadline — a retrieval — walks
// again until its bound ends.
const maxRewalks = 10

// Update brings the index up to date with a walk of its sources (FR-217). It works the
// difference out from a read of the stored generation, then inside one write transaction
// checks that generation is still the one it read — working the difference out again if
// another update committed meanwhile — and applies it with the new generation's row. A
// search therefore sees the generation before or the one after, never part of either
// (FR-219). An index stored under another identity is emptied first.
//
// A file whose content no longer has the digest the walk took is never indexed under it:
// the transaction is rolled back, the sources are walked again and the difference worked
// out afresh.
func (ix *Index) Update(ctx context.Context, walk Walk) (Generation, error) {
	return ix.write(ctx, walk, false)
}

// Rebuild replaces everything the index holds with a walk of its sources, in one write
// transaction, whatever it held before (FR-220).
func (ix *Index) Rebuild(ctx context.Context, walk Walk) (Generation, error) {
	return ix.write(ctx, walk, true)
}

func (ix *Index) write(ctx context.Context, walk Walk, rebuild bool) (Generation, error) {
	// changed is the file the last attempt found changed. It is cleared once an attempt has
	// read every document it adds unchanged, and a held index is its own cause: a refusal
	// names the file only while the file is what kept the update from finishing.
	var changed error
	fail := func(err error) (Generation, error) {
		if changed != nil && ctx.Err() != nil && !errors.Is(err, ErrHeld) {
			err = errors.Join(err, changed)
		}
		return Generation{}, err
	}

	for rewalks := 0; ; {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		target := GenerationOf(ix.identity, walk.Documents)
		var read Stored
		if err := ix.whileBusy(ctx, func() error {
			var err error
			read, err = readStored(ctx, ix.db)
			return err
		}); err != nil {
			return fail(err)
		}
		if !rebuild && read.Generation.ID == target {
			return read.Generation, nil
		}
		ix.seam(ix.seams.afterRead)

		diff := difference(ix.identity, read, walk, rebuild)
		var generation Generation
		err := ix.whileBusy(ctx, func() error {
			var err error
			generation, err = ix.commit(ctx, read.Generation.ID, diff, target, func() { changed = nil })
			return err
		})
		var moved *changedError
		switch {
		case errors.As(err, &moved):
			changed = err
			rewalks++
			if _, bounded := ctx.Deadline(); !bounded && rewalks >= maxRewalks {
				return Generation{}, fmt.Errorf("%w again on each of %d walks", err, rewalks)
			}
			if walk, err = walk.rewalk(ctx); err != nil {
				return fail(err)
			}
		case errors.Is(err, errMoved):
		case err != nil:
			return fail(err)
		default:
			return generation, nil
		}
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

// apply writes the change into tx. Each added document's text is read as that document is
// indexed, one at a time: read first, every added file's text would be held until the
// commit, which on a rebuild or a first retrieval is the whole collection with no bound on
// its size.
func (c change) apply(ctx context.Context, tx *sql.Tx, read func(source string)) error {
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
		content, err := c.walk.contentOf(document, read)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO documents (source, digest, bytes) VALUES (?, ?, ?)`,
			document.Source, document.Digest, document.Bytes); err != nil {
			return fmt.Errorf("indexing %s: %w", document.Source, err)
		}
		for _, passage := range c.walk.passages(document.Source, content) {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO passages (text, source, ordinal, start) VALUES (?, ?, ?, ?)`,
				passage.Text, passage.Source, passage.Ordinal, passage.Offset); err != nil {
				return fmt.Errorf("indexing %s: %w", document.Source, err)
			}
		}
	}
	return nil
}

// commit applies diff and the new generation's row in one write transaction. settled is
// called once every document it adds has been read unchanged.
//
// It runs on a connection of its own, so the wait it gives COMMIT is set on that
// connection alone and taken off again before the connection goes back to the pool.
func (ix *Index) commit(
	ctx context.Context, readID string, diff change, target string, settled func(),
) (Generation, error) {
	conn, err := ix.db.Conn(ctx)
	if err != nil {
		return Generation{}, &beginError{err}
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), "PRAGMA busy_timeout = 0")
		_ = conn.Close()
	}()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return Generation{}, &beginError{err}
	}
	defer func() { _ = tx.Rollback() }()

	current, err := readGeneration(ctx, tx)
	if err != nil {
		return Generation{}, err
	}
	if current.ID != readID {
		return Generation{}, errMoved
	}
	if err := diff.apply(ctx, tx, ix.seams.read); err != nil {
		return Generation{}, err
	}
	settled()
	ix.seam(ix.seams.inTransaction)

	next := Generation{ID: target, Identity: ix.identity, BuiltAt: ix.now()}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO generation (id, generation, identity, built_at) VALUES (1, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET generation = excluded.generation,
		    identity = excluded.identity, built_at = excluded.built_at`,
		next.ID, next.Identity, next.BuiltAt.Format(time.RFC3339Nano)); err != nil {
		return Generation{}, err
	}
	// Under a rollback journal a reader open at COMMIT keeps it from taking the database
	// exclusively. SQLite waits for the reader here, in full, for as long as commitWait
	// allows, rather than the transaction failing and being redone with every document
	// read again; the driver commits under a background context, so this wait is not
	// interrupted by ctx ending first.
	wait := commitWait(ctx)
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d", wait.Milliseconds())); err != nil {
		return Generation{}, err
	}
	if err := tx.Commit(); err != nil {
		if isBusy(err) {
			return Generation{}, &commitBusyError{wait: wait, err: err}
		}
		return Generation{}, err
	}
	return next, nil
}
