package index

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
)

// contentOf's held branch has no way to disagree with itself through any Walk this
// package builds: reportsWalkOf takes a document's digest and its held content from the
// same bytes in the same loop, so nothing this repository exercises can ever make them
// differ. The digest check is still there, unexercised by any of that, so it is driven
// directly on a Walk built by hand — the held-branch twin of
// TestAFileChangedDuringAnUpdateIsIndexedAsItNowStands, which drives the directory
// branch's own check the same way, through a file rewritten on disk.
func TestContentOfRefusesHeldContentThatDoesNotMatchItsDigest(t *testing.T) {
	sum := sha256.Sum256([]byte("not what held holds"))
	digestOfSomethingElse := hex.EncodeToString(sum[:])

	walk := Walk{held: map[string][]byte{"a": []byte("x")}}
	_, err := walk.contentOf(Document{Source: "a", Digest: digestOfSomethingElse}, nil)

	var changed *changedError
	if !errors.As(err, &changed) {
		t.Fatalf("contentOf returned %v, want a changedError", err)
	}
	if changed.source != "a" {
		t.Errorf("the refusal names %q, want %q", changed.source, "a")
	}
}
