package index_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/nicodarge/Gronin/runtime/internal/index"
)

// Each document's text is read inside the write transaction, as that document is indexed.
// Read before it, every added file's text is held until the commit — on a rebuild or a
// first retrieval, the whole collection, with no bound on its total size. When the text is
// read, a connection of the test's own must find the write lock taken.
func TestADocumentIsReadInsideTheWriteTransaction(t *testing.T) {
	dir := t.TempDir()
	sources := filepath.Join(dir, "sources")
	indexDir := filepath.Join(dir, "index")
	writeSources(t, sources, firstSources)
	ix := openIndex(t, indexDir, sources)
	walk := walked(t, sources)

	probe, err := sql.Open("sqlite", "file:"+filepath.Join(indexDir, "runbooks.db")+
		"?_pragma=busy_timeout(0)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = probe.Close() })

	read := 0
	index.SetReadSeam(ix, func(source string) {
		read++
		tx, err := probe.BeginTx(context.Background(), nil)
		if err == nil {
			_ = tx.Rollback()
			t.Errorf("%s was read while no write transaction held the index", source)
			return
		}
		var held *sqlite.Error
		if !errors.As(err, &held) || held.Code()&0xff != sqlite3.SQLITE_BUSY {
			t.Errorf("probing the write lock while %s was read: %v", source, err)
		}
	})
	if _, err := ix.Update(t.Context(), walk); err != nil {
		t.Fatal(err)
	}
	if read != len(firstSources) {
		t.Errorf("%d documents were read to index %d", read, len(firstSources))
	}
}
