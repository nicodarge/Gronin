package index_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/index"
)

// A reader blocked at its first statement — a deferred read-only BeginTx takes no lock of
// its own, so it is the first statement that reads that finds the index held — is still
// reported held, the same as a reader blocked at BeginTx itself would be.
func TestAReaderBlockedAtItsFirstStatementIsStillReportedHeldThroughReadStored(t *testing.T) {
	dir := t.TempDir()
	sources := filepath.Join(dir, "sources")
	indexDir := filepath.Join(dir, "index")
	writeSources(t, sources, firstSources)
	ix := openIndex(t, indexDir, sources)
	if _, err := ix.Update(t.Context(), walked(t, sources)); err != nil {
		t.Fatal(err)
	}
	holdExclusiveLock(t, indexDir)

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if _, _, err := index.ReadStored(ctx, indexDir, "runbooks"); !errors.Is(err, index.ErrHeld) {
		t.Errorf("a reader blocked at its first statement is not reported held: %v", err)
	}
}

// The same, through search.
func TestAReaderBlockedAtItsFirstStatementIsStillReportedHeldThroughSearch(t *testing.T) {
	dir := t.TempDir()
	sources := filepath.Join(dir, "sources")
	indexDir := filepath.Join(dir, "index")
	writeSources(t, sources, firstSources)
	ix := openIndex(t, indexDir, sources)
	if _, err := ix.Update(t.Context(), walked(t, sources)); err != nil {
		t.Fatal(err)
	}
	holdExclusiveLock(t, indexDir)

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if _, err := ix.Search(ctx, "disk", 10); !errors.Is(err, index.ErrHeld) {
		t.Errorf("a search blocked at its first statement is not reported held: %v", err)
	}
}

// Once a genuine BUSY has set retryBusy's own "found it held" flag, a later, unrelated
// failure — disk I/O, corruption, a permission error, schema drift — is not the index
// held: it is not another connection wanting the write lock, and relabeling it that hides
// its real cause behind a lock that was never the problem. Driven through AsBegin, the
// exact rule readStored and search both apply to decide that, and RetryBusy, their shared
// retry loop — real errors throughout (a write actually blocked by an exclusive lock held
// elsewhere, and a query against a table that genuinely does not exist), timed
// deterministically via a synthetic operation rather than by racing a live query against
// an already-expired context, which database/sql refuses to even start.
func TestANonBusyErrorAfterAPriorBusyHitIsNotRelabeledHeldByAsBegin(t *testing.T) {
	dir := t.TempDir()
	indexDir := filepath.Join(dir, "index")
	ix := openIndex(t, indexDir, filepath.Join(dir, "sources"))
	if _, err := ix.Update(t.Context(), index.Walk{}); err != nil {
		t.Fatal(err)
	}
	release := holdExclusiveLock(t, indexDir)

	busyDB, err := sql.Open("sqlite", "file:"+filepath.Join(indexDir, "runbooks.db")+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = busyDB.Close() })
	_, busyErr := busyDB.ExecContext(context.Background(), `DELETE FROM documents WHERE source = 'does-not-exist'`)
	if busyErr == nil {
		t.Fatal("a write against an index held exclusively elsewhere succeeded")
	}
	release()
	_, otherErr := busyDB.ExecContext(context.Background(), `SELECT * FROM no_such_table`)
	if otherErr == nil {
		t.Fatal("a query against a table that does not exist succeeded")
	}

	index.SetBusyPause(t, 5*time.Millisecond)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	calls := 0
	err = index.RetryBusy(ctx, nil, func() error {
		calls++
		if calls == 1 {
			return index.AsBegin(busyErr)
		}
		<-ctx.Done()
		return index.AsBegin(otherErr)
	})
	if err == nil {
		t.Fatal("a retry over a table that does not exist succeeded")
	}
	if errors.Is(err, index.ErrHeld) {
		t.Errorf("an unrelated failure after a prior busy hit is reported as the index held: %v", err)
	}
}

// BeginTx's own failure is a begin failure whatever caused it — not only a genuine BUSY.
// That matters because database/sql refuses to even start BeginTx once ctx has already
// ended, returning ctx's bare error without ever asking the driver anything: the exact
// shape of failure a retry attempt gets when the caller's bound runs out in the narrow
// window between retryBusy's own check and the next attempt actually beginning. Left
// unmarked, that bare error reaching heldAtBound after a genuine BUSY was already seen is
// not relabeled ErrHeld, and a listing behind a held index prints "context deadline
// exceeded" instead of saying the index is held. Driven directly through readStored with a
// context already past its deadline, so the failure is guaranteed, not raced.
func TestABeginTxFailureIsABeginFailureWhateverCausedIt(t *testing.T) {
	dir := t.TempDir()
	indexDir := filepath.Join(dir, "index")
	ix := openIndex(t, indexDir, filepath.Join(dir, "sources"))
	if _, err := ix.Update(t.Context(), index.Walk{}); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", "file:"+filepath.Join(indexDir, "runbooks.db")+"?mode=rw&_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	expired, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()

	_, err = index.ReadStoredRaw(expired, db)
	if err == nil {
		t.Fatal("readStored against an already-expired context succeeded")
	}
	if !index.IsBegin(err) {
		t.Errorf("a BeginTx that never reached the driver because ctx had already ended "+
			"is not marked a begin failure: %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("the failure does not carry the context's own deadline: %v", err)
	}
}

// The same, for the other point a transaction has not yet taken a real lock: a deferred
// read-only BeginTx succeeds without asking the driver for one, so ctx can just as well end
// between BeginTx returning and the first read that actually asks for the lock — and that
// failure must be marked a begin failure exactly as BeginTx's own would be, not left bare.
// Ended deterministically by SetAfterDeferredBegin, at the one point between BeginTx and
// the first read, rather than by racing a live query against a context that happens to end
// there.
func TestAFirstReadFailingBecauseCtxEndedRightAfterBeginTxIsABeginFailure(t *testing.T) {
	dir := t.TempDir()
	indexDir := filepath.Join(dir, "index")
	ix := openIndex(t, indexDir, filepath.Join(dir, "sources"))
	if _, err := ix.Update(t.Context(), index.Walk{}); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", "file:"+filepath.Join(indexDir, "runbooks.db")+"?mode=rw&_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(5*time.Millisecond))
	defer cancel()
	index.SetAfterDeferredBegin(t, func() { time.Sleep(20 * time.Millisecond) })

	_, err = index.ReadStoredRaw(ctx, db)
	if err == nil {
		t.Fatal("readStored against a context that ended before its first read succeeded")
	}
	if !index.IsBegin(err) {
		t.Errorf("a first read failing because ctx ended right after BeginTx "+
			"is not marked a begin failure: %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("the failure does not carry the context's own deadline: %v", err)
	}
}
