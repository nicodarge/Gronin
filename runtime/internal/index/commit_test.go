package index_test

import (
	"context"
	"database/sql"
	"path/filepath"
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
