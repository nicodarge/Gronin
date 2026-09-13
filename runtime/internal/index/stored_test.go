package index_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/index"
)

// The first retrieval creates the index file before it writes a table into it, so a listing
// that runs in between finds a file holding nothing. That collection is not indexed yet; it
// is not a collection that cannot be listed.
func TestAnIndexFileHoldingNoGenerationIsNotIndexedYet(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "runbooks.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	stored, _, err := index.ReadStored(t.Context(), dir, "runbooks")
	if err != nil {
		t.Fatalf("an index file holding nothing yet cannot be read: %v", err)
	}
	if stored.Generation.ID != "" || len(stored.Documents) != 0 {
		t.Errorf("an index file holding nothing reads as %+v", stored)
	}
}
