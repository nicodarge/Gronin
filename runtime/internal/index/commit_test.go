package index_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/index"
)

// A search open while an update commits holds the database, and under a rollback journal
// COMMIT has to wait for it to finish. The update waits at COMMIT, within its bound, and does
// not redo its transaction for it: redone, every added file is read and indexed again for
// each reader it meets, and a rebuild under a searching deployment can be starved by them.
func TestAReaderOpenAtCommitIsWaitedOutWithoutReadingAgain(t *testing.T) {
	dir := t.TempDir()
	sources := filepath.Join(dir, "sources")
	indexDir := filepath.Join(dir, "index")
	writeSources(t, sources, firstSources)
	ix := openIndex(t, indexDir, sources)
	walk := walked(t, sources)

	reader, err := sql.Open("sqlite", "file:"+filepath.Join(indexDir, "runbooks.db")+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })

	reads, busy := 0, 0
	index.SetReadSeam(ix, func(string) { reads++ })
	index.SetBusySeam(ix, func() { busy++ })
	opened := false
	done := make(chan struct{})
	index.SetSeams(ix, nil, func() {
		if opened {
			return
		}
		opened = true
		// A read transaction that has read something holds the database until it ends.
		tx, err := reader.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Error(err)
			close(done)
			return
		}
		var documents int
		if err := tx.QueryRowContext(t.Context(), `SELECT count(*) FROM sqlite_master`).Scan(&documents); err != nil {
			t.Error(err)
		}
		go func() {
			defer close(done)
			time.Sleep(200 * time.Millisecond)
			_ = tx.Rollback()
		}()
	})
	t.Cleanup(func() { index.SetSeams(ix, nil, nil) })

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if _, err := ix.Update(ctx, walk); err != nil {
		t.Fatalf("an update with a reader open at its commit was refused: %v", err)
	}
	<-done
	if reads != len(firstSources) {
		t.Errorf("%d documents were read to index %d: the transaction was redone for the reader", reads, len(firstSources))
	}
	if busy != 0 {
		t.Errorf("the update found the index held %d times, where it should have waited at COMMIT", busy)
	}
}

// commitWait is the caller's own remaining bound, in full, not a cap under it: a cap
// smaller than a retrieval's own bound (RetrievalTimeout, cmd/gronin/collections.go)
// reintroduces exactly what this test guards against — COMMIT giving up on a reader well
// before the caller would have, and the retry redoing the whole transaction for it. With
// no bound at all, as for a rebuild, the wait is unbounded rather than falling back to a
// fixed figure of this package's own.
func TestCommitWaitIsTheCallersRemainingBoundOrUnboundedWithNone(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	if wait := index.CommitWait(ctx); wait < 40*time.Second {
		t.Errorf("commit wait for a 45s bound came back %s, want it close to the bound, not capped under it", wait)
	}
	if wait := index.CommitWait(t.Context()); wait < 24*time.Hour {
		t.Errorf("commit wait with no bound came back %s, want it unbounded", wait)
	}
}

// A reader can outlast whatever fixed cap COMMIT's own wait might be capped at while
// still settling inside the caller's real bound: shortened here through the seam rather
// than a real bound and a real multi-second hold, so the suite stays fast, but the wait
// commit gives COMMIT is real and the reader's hold genuinely outlasts it. A cap under
// that wait would give up before the reader lets go and redo the transaction; the fix
// does not.
func TestAReaderHeldLongerThanAnyLikelyCapIsStillWaitedOutWithoutReadingAgain(t *testing.T) {
	dir := t.TempDir()
	sources := filepath.Join(dir, "sources")
	indexDir := filepath.Join(dir, "index")
	writeSources(t, sources, firstSources)
	ix := openIndex(t, indexDir, sources)
	walk := walked(t, sources)

	index.SetCommitWait(t, func(context.Context) time.Duration { return time.Second })

	reader, err := sql.Open("sqlite", "file:"+filepath.Join(indexDir, "runbooks.db")+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })

	reads, busy := 0, 0
	index.SetReadSeam(ix, func(string) { reads++ })
	index.SetBusySeam(ix, func() { busy++ })
	opened := false
	done := make(chan struct{})
	index.SetSeams(ix, nil, func() {
		if opened {
			return
		}
		opened = true
		tx, err := reader.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Error(err)
			close(done)
			return
		}
		var documents int
		if err := tx.QueryRowContext(t.Context(), `SELECT count(*) FROM sqlite_master`).Scan(&documents); err != nil {
			t.Error(err)
		}
		go func() {
			defer close(done)
			// Longer than any cap a regression might reintroduce well under the 1s wait
			// set above, and still comfortably inside it.
			time.Sleep(400 * time.Millisecond)
			_ = tx.Rollback()
		}()
	})
	t.Cleanup(func() { index.SetSeams(ix, nil, nil) })

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if _, err := ix.Update(ctx, walk); err != nil {
		t.Fatalf("an update with a reader open at its commit for 400ms was refused: %v", err)
	}
	<-done
	if reads != len(firstSources) {
		t.Errorf("%d documents were read to index %d: the transaction was redone for the reader", reads, len(firstSources))
	}
	if busy != 0 {
		t.Errorf("the update found the index held %d times, where it should have waited at COMMIT", busy)
	}
}

// Once COMMIT's own wait runs out, the update fails with a plain cause naming the wait:
// not the index held (it is a reader, not another connection wanting the write lock), and
// not redone from the start, since the transaction it built is exactly what a retry would
// throw away and read again. The previous generation stays in place, rolled back by the
// deferred call rather than left half-written.
func TestACommitThatExhaustsItsWaitFailsWithoutRedoingTheTransaction(t *testing.T) {
	dir := t.TempDir()
	sources := filepath.Join(dir, "sources")
	indexDir := filepath.Join(dir, "index")
	writeSources(t, sources, firstSources)
	ix := openIndex(t, indexDir, sources)
	first, err := ix.Update(t.Context(), walked(t, sources))
	if err != nil {
		t.Fatal(err)
	}
	writeSources(t, sources, secondSources)

	index.SetCommitWait(t, func(context.Context) time.Duration { return 50 * time.Millisecond })

	reader, err := sql.Open("sqlite", "file:"+filepath.Join(indexDir, "runbooks.db")+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })

	reads, busy := 0, 0
	index.SetReadSeam(ix, func(string) { reads++ })
	index.SetBusySeam(ix, func() { busy++ })
	opened := false
	index.SetSeams(ix, nil, func() {
		if opened {
			return
		}
		opened = true
		tx, err := reader.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Error(err)
			return
		}
		var documents int
		if err := tx.QueryRowContext(t.Context(), `SELECT count(*) FROM sqlite_master`).Scan(&documents); err != nil {
			t.Error(err)
		}
		t.Cleanup(func() { _ = tx.Rollback() })
	})
	t.Cleanup(func() { index.SetSeams(ix, nil, nil) })

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	_, err = ix.Update(ctx, walked(t, sources))
	if err == nil {
		t.Fatal("an update whose COMMIT never got past a reader succeeded")
	}
	if errors.Is(err, index.ErrHeld) {
		t.Errorf("COMMIT giving up on a reader is reported as the index held by another connection: %v", err)
	}
	if !strings.Contains(err.Error(), "reader") {
		t.Errorf("the refusal does not name why COMMIT gave up: %v", err)
	}
	if reads != len(firstSources) {
		t.Errorf("%d documents were read: a COMMIT that gave up was retried from the start", reads)
	}
	if busy != 0 {
		t.Errorf("the update found the index held %d times: a COMMIT timeout re-entered the busy-retry loop", busy)
	}

	dump, err := index.Dump(t.Context(), ix)
	if err != nil {
		t.Fatal(err)
	}
	if len(dump) == 0 || !strings.HasPrefix(dump[0], "generation "+first.ID+" ") {
		t.Errorf("the generation after a failed COMMIT is %v, want the previous one, %s, unchanged", dump, first.ID)
	}
}
