package index_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/index"
)

// outsideMarker is the content of a file outside the collection, reachable only through a
// symbolic link inside it. It must never be read.
const outsideMarker = "OUTSIDE-MARKER-7d41c0"

// skippingDirectory builds SC-207's directory: a text file, a file holding a NUL byte, a
// text file one byte past the document bound, a link to a file outside, and a link to
// the directory's own parent. It returns the collection's directory.
func skippingDirectory(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	collection := filepath.Join(root, "collection")
	for _, dir := range []string{outside, filepath.Join(collection, "sub")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(outside, "elsewhere.md"):        "certificate rotation " + outsideMarker + "\n",
		filepath.Join(collection, "notes.md"):         "disk full on /var\n",
		filepath.Join(collection, "sub", "deeper.md"): "swap exhausted\n",
		filepath.Join(collection, "binary.dat"):       "disk\x00full",
		filepath.Join(collection, "latin1.txt"):       "d\xe9j\xe0 vu",
		filepath.Join(collection, "large.txt"):        strings.Repeat("a", index.MaxDocumentBytes+1),
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(outside, "elsewhere.md"), filepath.Join(collection, "outside-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..", filepath.Join(collection, "parent-link")); err != nil {
		t.Fatal(err)
	}
	return collection
}

func digestOf(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// SC-207, the walk. The result is asserted whole: a walk that follows the link to its
// parent lists the text files again under another path, or never returns, and the
// deadline here is the test's own so that the second fails as an assertion.
func TestTheWalkSkipsWhatItMustNotRead(t *testing.T) {
	dir := skippingDirectory(t)

	type walked struct {
		walk index.Walk
		err  error
	}
	done := make(chan walked, 1)
	go func() {
		walk, err := index.WalkDirectory(t.Context(), dir)
		done <- walked{walk, err}
	}()

	var got walked
	select {
	case got = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the walk did not return within 10s; a link it followed is the likeliest reason")
	}
	if got.err != nil {
		t.Fatalf("the walk failed: %v", got.err)
	}

	wantDocuments := []index.Document{
		{Source: "notes.md", Digest: digestOf("disk full on /var\n"), Bytes: 18},
		{Source: "sub/deeper.md", Digest: digestOf("swap exhausted\n"), Bytes: 15},
	}
	var documents []index.Document
	for _, document := range got.walk.Documents {
		if strings.Contains(string(document.Content), outsideMarker) {
			t.Errorf("%s holds the content of a file outside the collection", document.Source)
		}
		document.Content = nil
		documents = append(documents, document)
	}
	if !reflect.DeepEqual(documents, wantDocuments) {
		t.Errorf("documents:\n  got  %+v\n  want %+v", documents, wantDocuments)
	}

	wantSkipped := []index.Skipped{
		{Source: "binary.dat", Reason: index.ReasonNotText},
		{Source: "large.txt", Reason: index.ReasonTooLarge},
		{Source: "latin1.txt", Reason: index.ReasonNotText},
		{Source: "outside-link", Reason: index.ReasonSymlink},
		{Source: "parent-link", Reason: index.ReasonSymlink},
	}
	if !reflect.DeepEqual(got.walk.Skipped, wantSkipped) {
		t.Errorf("skipped:\n  got  %+v\n  want %+v", got.walk.Skipped, wantSkipped)
	}
}

// FR-228: a directory that is not there is a refusal naming it. An empty walk would tell
// the agent nothing was found, and an operator reading that has no way to tell a quiet
// collection from a missing one.
func TestTheWalkOfAMissingDirectoryIsAnError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-mounted")

	walk, err := index.WalkDirectory(t.Context(), missing)
	if err == nil {
		t.Fatalf("a missing directory walked as %+v", walk)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("the error does not name the directory: %v", err)
	}
}
