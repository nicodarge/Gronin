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
