package index_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// An index holds the collection's text unredacted, so it is readable by the runtime's own
// user only, whatever the process's umask would otherwise grant. The umask is set to the
// common default here so that a file created with SQLite's own mode is seen to be wider.
func TestTheIndexIsReadableOnlyByItsOwner(t *testing.T) {
	previous := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(previous) })

	dir := t.TempDir()
	sources := filepath.Join(dir, "sources")
	indexDir := filepath.Join(dir, "index")
	writeSources(t, sources, firstSources)
	ix := openIndex(t, indexDir, sources)
	if _, err := ix.Update(t.Context(), walked(t, sources)); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(filepath.Join(indexDir, "runbooks.db"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("the index is created with mode %o, want 600", mode)
	}
}
