package index_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/index"
)

// The search refuses a query holding no word it can search, whoever calls it: an empty
// FTS5 match is not a search, and what it returned would read as nothing found.
func TestASearchHoldingNoWordIsRefused(t *testing.T) {
	dir := t.TempDir()
	sources := filepath.Join(dir, "sources")
	writeSources(t, sources, firstSources)
	ix := openIndex(t, filepath.Join(dir, "index"), sources)
	if _, err := ix.Update(t.Context(), walked(t, sources)); err != nil {
		t.Fatal(err)
	}

	for _, query := range []string{"  \t", "*** ---"} {
		found, err := ix.Search(t.Context(), query, 5)
		if !errors.Is(err, index.ErrNoWords) {
			t.Errorf("searching %q returned %+v, %v; want the query refused", query, found.Hits, err)
		}
	}
}
