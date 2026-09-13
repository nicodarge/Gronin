package index_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/index"
)

// holdWriteLock takes the index's write lock through a connection of the test's own, the
// way another process's rebuild holds it, and returns what releases it.
func holdWriteLock(t *testing.T, indexDir string) func() {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(indexDir, "runbooks.db"))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	release := sync.OnceFunc(func() {
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		_ = conn.Close()
		_ = db.Close()
	})
	t.Cleanup(release)
	return release
}

// indexHeldWhileSourcesChange is an index at a first generation, its sources changed, and
// its write lock held by another connection.
func indexHeldWhileSourcesChange(t *testing.T) (*index.Index, index.Walk, func()) {
	t.Helper()
	dir := t.TempDir()
	sources := filepath.Join(dir, "sources")
	indexDir := filepath.Join(dir, "index")
	writeSources(t, sources, firstSources)
	ix := openIndex(t, indexDir, sources)
	if _, err := ix.Update(t.Context(), walked(t, sources)); err != nil {
		t.Fatal(err)
	}
	writeSources(t, sources, secondSources)
	return ix, walked(t, sources), holdWriteLock(t, indexDir)
}

// A rebuild in another process holds the write lock for as long as it takes. An update
// finding it held waits, within its retrieval's own bound, rather than refusing at once:
// the lock is released only once the update has found it held, so an update that did not
// wait fails here whatever the timing.
func TestAHeldIndexIsWaitedOutWithinTheRetrievalBound(t *testing.T) {
	ix, walk, release := indexHeldWhileSourcesChange(t)
	found := 0
	index.SetBusySeam(ix, func() {
		found++
		release()
	})

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if _, err := ix.Update(ctx, walk); err != nil {
		t.Fatalf("an update refused an index held for less than its bound: %v", err)
	}
	if found == 0 {
		t.Fatal("the update never found the index held, so nothing here tested waiting")
	}
	if got := sourcesOf(t, ix, "memory"); !reflect.DeepEqual(got, []string{"a.md"}) {
		t.Errorf("after waiting, the update did not apply the sources as they stand: memory found in %v", got)
	}
}

// Found held once, the index is not what every later failure is. Here the update takes the
// lock once the other connection lets go, and its bound ends while it is inside its own
// transaction: that is the update running out of time, not the index being held.
func TestAFailureAfterTheLockIsTakenIsNotReportedAsHeld(t *testing.T) {
	ix, walk, release := indexHeldWhileSourcesChange(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	index.SetBusySeam(ix, release)
	index.SetSeams(ix, nil, func() { <-ctx.Done() })
	t.Cleanup(func() { index.SetSeams(ix, nil, nil) })

	_, err := ix.Update(ctx, walk)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("the update ended with %v, want its bound's deadline", err)
	}
	if errors.Is(err, index.ErrHeld) {
		t.Errorf("an update that held the lock when its bound ended is reported as held: %v", err)
	}
}

// A cancel is the caller's own, whatever the update was waiting for: an operator's Ctrl-C
// on a listing waiting behind a held index is not the index held past its bound.
func TestACancelIsNotReportedAsHeld(t *testing.T) {
	t.Run("while waiting for the lock", func(t *testing.T) {
		ix, walk, _ := indexHeldWhileSourcesChange(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		index.SetBusySeam(ix, cancel)

		_, err := ix.Update(ctx, walk)
		if !errors.Is(err, context.Canceled) || errors.Is(err, index.ErrHeld) {
			t.Errorf("a cancel while waiting for a held index ended with %v, want the cancel and not held", err)
		}
	})
	t.Run("inside the transaction", func(t *testing.T) {
		ix, walk, release := indexHeldWhileSourcesChange(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		index.SetBusySeam(ix, release)
		index.SetSeams(ix, nil, cancel)
		t.Cleanup(func() { index.SetSeams(ix, nil, nil) })

		_, err := ix.Update(ctx, walk)
		if !errors.Is(err, context.Canceled) || errors.Is(err, index.ErrHeld) {
			t.Errorf("a cancel inside the transaction ended with %v, want the cancel and not held", err)
		}
	})
}

// A BUSY that never went through BeginTx — an ordinary write statement finding the index
// held, the way a COMMIT does — is not relabeled the index held once the bound ends inside
// the retry pause, any more than it is at the top of the loop: retryBusy's two points that
// can end the loop on ctx apply the same rule. Driven directly with a synthetic operation,
// since nothing in this package's own calls leaves that rule untested once COMMIT's own
// BUSY is caught earlier and never reaches here at all.
func TestABusyThatNeverBeganIsNotRelabeledHeldInsideTheRetryPauseEither(t *testing.T) {
	dir := t.TempDir()
	indexDir := filepath.Join(dir, "index")
	ix := openIndex(t, indexDir, filepath.Join(dir, "sources"))
	if _, err := ix.Update(t.Context(), index.Walk{}); err != nil {
		t.Fatal(err)
	}
	holdWriteLock(t, indexDir)

	busyDB, err := sql.Open("sqlite", "file:"+filepath.Join(indexDir, "runbooks.db")+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = busyDB.Close() })

	index.SetBusyPause(t, 5*time.Millisecond)
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()

	err = index.RetryBusy(ctx, nil, func() error {
		// An ordinary autocommit write, not a BeginTx: exactly the shape of failure a
		// begin-only rule has to tell apart from one, since nothing marks it as begin.
		_, err := busyDB.ExecContext(context.Background(), `DELETE FROM documents WHERE source = 'does-not-exist'`)
		return err
	})
	if err == nil {
		t.Fatal("a write against an index held exclusively elsewhere succeeded")
	}
	if errors.Is(err, index.ErrHeld) {
		t.Errorf("a BUSY that never went through BeginTx is relabeled the index held once the bound ends inside the retry pause: %v", err)
	}
}

// Held past the bound, the update is refused when its bound ends, not after a wait of the
// index's own choosing. The watchdog is well past the bound and well short of a fixed
// five-second wait inside SQLite.
func TestAnIndexHeldPastTheRetrievalBoundRefusesAtTheBound(t *testing.T) {
	ix, walk, _ := indexHeldWhileSourcesChange(t)

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := ix.Update(ctx, walk)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("the update ended with %v, want its bound's deadline", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("an update bounded at 300ms was still waiting for the index after 3s")
	}
}
