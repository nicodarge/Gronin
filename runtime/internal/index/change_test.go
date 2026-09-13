package index_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/index"
)

// A file rewritten once while an update is under way is no reason to refuse the retrieval:
// the update walks the directory again, within its bound, and indexes the file as it now
// stands — never the new text under the digest the first walk took of the old.
func TestAFileChangedDuringAnUpdateIsIndexedAsItNowStands(t *testing.T) {
	dir := t.TempDir()
	sources := filepath.Join(dir, "sources")
	indexDir := filepath.Join(dir, "index")
	writeSources(t, sources, firstSources)
	ix := openIndex(t, indexDir, sources)
	walk := walked(t, sources)

	rewritten := false
	index.SetSeams(ix, func() {
		if rewritten {
			return
		}
		rewritten = true
		if err := os.WriteFile(filepath.Join(sources, "a.md"), []byte("rewritten while indexed\n"), 0o600); err != nil {
			t.Error(err)
		}
	}, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	_, err := ix.Update(ctx, walk)
	index.SetSeams(ix, nil, nil)
	if err != nil {
		t.Fatalf("an update over a file rewritten once was refused: %v", err)
	}

	if got := sourcesOf(t, ix, "rewritten"); !reflect.DeepEqual(got, []string{"a.md"}) {
		t.Errorf("the file's text as it now stands is not what was indexed: rewritten found in %v", got)
	}
	if got := sourcesOf(t, ix, "disk"); len(got) != 0 {
		t.Errorf("the file's earlier text is still indexed: disk found in %v", got)
	}
	stored, _, err := index.ReadStored(t.Context(), indexDir, "runbooks")
	if err != nil {
		t.Fatal(err)
	}
	for _, document := range stored.Documents {
		if document.Source == "a.md" && document.Digest != digestOf("rewritten while indexed\n") {
			t.Errorf("a.md is recorded under digest %s, not the digest of what was indexed", document.Digest)
		}
	}
}

// A file that changes every time it is read never lets an update finish. It is refused when
// the retrieval's bound ends, naming the file — not at its first change, and not after the
// bound has passed.
func TestAFileThatKeepsChangingIsRefusedAtTheBound(t *testing.T) {
	dir := t.TempDir()
	sources := filepath.Join(dir, "sources")
	writeSources(t, sources, firstSources)
	ix := openIndex(t, filepath.Join(dir, "index"), sources)
	walk := walked(t, sources)

	written := 0
	index.SetSeams(ix, func() {
		written++
		content := fmt.Sprintf("appended line %d\n", written)
		if err := os.WriteFile(filepath.Join(sources, "a.md"), []byte(content), 0o600); err != nil {
			t.Error(err)
		}
	}, nil)
	t.Cleanup(func() { index.SetSeams(ix, nil, nil) })

	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := ix.Update(ctx, walk)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("the update ended with %v, want its bound's deadline", err)
		}
		if err == nil || !strings.Contains(err.Error(), "a.md") {
			t.Errorf("the refusal does not name the file that kept changing: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("an update bounded at 500ms over a file that keeps changing had not ended after 5s")
	}
}

// A file that changed once and settled is not what a later refusal blames. Here the index is
// taken by another connection once the file has settled, and held past the bound: the
// refusal is the held index.
func TestAHeldIndexAtTheBoundDoesNotBlameAFileThatSettled(t *testing.T) {
	dir := t.TempDir()
	sources := filepath.Join(dir, "sources")
	indexDir := filepath.Join(dir, "index")
	writeSources(t, sources, firstSources)
	ix := openIndex(t, indexDir, sources)
	walk := walked(t, sources)

	calls := 0
	index.SetSeams(ix, func() {
		calls++
		switch calls {
		case 1:
			if err := os.WriteFile(filepath.Join(sources, "a.md"), []byte("rewritten once\n"), 0o600); err != nil {
				t.Error(err)
			}
		case 2:
			holdWriteLock(t, indexDir)
		}
	}, nil)
	t.Cleanup(func() { index.SetSeams(ix, nil, nil) })

	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	_, err := ix.Update(ctx, walk)
	if !errors.Is(err, index.ErrHeld) {
		t.Errorf("the update ended with %v, want the held index", err)
	}
	if err != nil && strings.Contains(err.Error(), "a.md") {
		t.Errorf("the refusal blames a file that had settled: %v", err)
	}
}

// Nor does a refusal at the bound blame it when the bound ends while the update, the file
// long settled, is still writing the generation.
func TestAnUpdateAtTheBoundDoesNotBlameAFileThatSettled(t *testing.T) {
	dir := t.TempDir()
	sources := filepath.Join(dir, "sources")
	writeSources(t, sources, firstSources)
	ix := openIndex(t, filepath.Join(dir, "index"), sources)
	walk := walked(t, sources)

	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	calls := 0
	index.SetSeams(ix, func() {
		calls++
		if calls == 1 {
			if err := os.WriteFile(filepath.Join(sources, "a.md"), []byte("rewritten once\n"), 0o600); err != nil {
				t.Error(err)
			}
		}
	}, func() { <-ctx.Done() })
	t.Cleanup(func() { index.SetSeams(ix, nil, nil) })

	_, err := ix.Update(ctx, walk)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("the update ended with %v, want its bound's deadline", err)
	}
	if err != nil && strings.Contains(err.Error(), "a.md") {
		t.Errorf("the refusal blames a file that had settled: %v", err)
	}
}

// A rebuild runs outside any retrieval and has no bound, so a file that changes on every
// read would hold it forever. It fails naming the file once the file has changed on each of
// its walks.
func TestARebuildOverAFileThatKeepsChangingFailsNamingIt(t *testing.T) {
	dir := t.TempDir()
	sources := filepath.Join(dir, "sources")
	writeSources(t, sources, firstSources)
	ix := openIndex(t, filepath.Join(dir, "index"), sources)
	walk := walked(t, sources)

	written := 0
	index.SetSeams(ix, func() {
		written++
		content := fmt.Sprintf("appended line %d\n", written)
		if err := os.WriteFile(filepath.Join(sources, "a.md"), []byte(content), 0o600); err != nil {
			t.Error(err)
		}
	}, nil)
	t.Cleanup(func() { index.SetSeams(ix, nil, nil) })

	done := make(chan error, 1)
	go func() {
		_, err := ix.Rebuild(t.Context(), walk)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "a.md") || !strings.Contains(err.Error(), "walks") {
			t.Errorf("a rebuild over a file that keeps changing ended with %v, want a refusal naming the file", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("a rebuild over a file that keeps changing had not ended after 30s")
	}
}
