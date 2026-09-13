package index_test

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/index"
)

// An index file this user owns and made wider than 0600 — an older runtime's, or a copy — is
// narrowed when it is opened.
func TestAnIndexFileWiderThanItsOwnerIsNarrowed(t *testing.T) {
	previous := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(previous) })
	dir := t.TempDir()
	file := filepath.Join(dir, "runbooks.db")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	ix, err := index.Open(t.Context(), dir, "runbooks", index.DirectoryConfiguration(dir))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ix.Close() })
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("an index file at 644 was left at %o, want 600", mode)
	}
}

// An index file another user owns at a mode wider than 0600 holds the collection's text
// where others can read it, and this process cannot narrow it. It is refused naming the
// owner and the mode, never used as it is.
func TestAnIndexFileAnotherUserOwnsAtAWiderModeIsRefused(t *testing.T) {
	previous := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(previous) })
	dir := t.TempDir()
	file := filepath.Join(dir, "runbooks.db")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	other := os.Getuid() + 1
	index.SetOwner(t, func(os.FileInfo) int { return other })

	ix, err := index.Open(t.Context(), dir, "runbooks", index.DirectoryConfiguration(dir))
	if err == nil {
		_ = ix.Close()
		t.Fatal("an index file another user owns at mode 644 was opened")
	}
	if !strings.Contains(err.Error(), "644") {
		t.Errorf("the refusal does not name the mode: %v", err)
	}
	if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0o644 {
		t.Errorf("another user's index file was changed: %v %v", info.Mode(), err)
	}
}

// An index file the runtime cannot write — created by `gronin collections rebuild` run as
// another user than the deployment's, say — is refused with that cause, not with whichever
// error SQLite reaches first. The check is made to answer as it does for another user's
// file: under the suite this process is root, which writes a file whatever its mode.
func TestAnIndexFileTheRuntimeCannotWriteIsRefusedSayingWhy(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "runbooks.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	index.SetWritable(t, func(string) error { return syscall.EACCES })

	ix, err := index.Open(t.Context(), dir, "runbooks", index.DirectoryConfiguration(dir))
	if err == nil {
		_ = ix.Close()
		t.Fatal("an index file the runtime cannot write was opened")
	}
	if !strings.Contains(err.Error(), "deployment's user") {
		t.Errorf("the refusal does not say the index belongs to the deployment's user: %v", err)
	}
}
