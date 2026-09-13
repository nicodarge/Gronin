package index_test

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/index"
)

// killHelperEnv, when set, turns TestHelperUpdateThenBlocks into the process
// TestAKilledRebuildLeavesThePreviousGeneration kills: it names a directory holding the
// sources and the index.
const killHelperEnv = "GRONIN_INDEX_KILL_HELPER_DIR"

// writeSources replaces a directory's files with the ones given.
func writeSources(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func openIndex(t *testing.T, indexDir, sources string) *index.Index {
	t.Helper()
	ix, err := index.Open(t.Context(), indexDir, "runbooks", index.DirectoryConfiguration(sources))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ix.Close() })
	return ix
}

func walked(t *testing.T, dir string) index.Walk {
	t.Helper()
	walk, err := index.WalkDirectory(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return walk
}

func sourcesOf(t *testing.T, ix *index.Index, query string) []string {
	t.Helper()
	found, err := ix.Search(t.Context(), query, 50)
	if err != nil {
		t.Fatal(err)
	}
	var sources []string
	for _, hit := range found.Hits {
		sources = append(sources, hit.Source)
	}
	return sources
}

var (
	firstSources  = map[string]string{"a.md": "disk full on /var\n", "b.md": "certificate expired\n"}
	secondSources = map[string]string{"a.md": "memory pressure\n", "c.md": "disk quota exceeded\n"}
)

// TestHelperUpdateThenBlocks is not a test of its own. It does nothing unless
// killHelperEnv is set, which only the test below does, on a re-execution of this binary.
func TestHelperUpdateThenBlocks(t *testing.T) {
	dir := os.Getenv(killHelperEnv)
	if dir == "" {
		t.Skip("not running as the kill test's subprocess")
	}
	sources := filepath.Join(dir, "sources")
	ix, err := index.Open(t.Context(), filepath.Join(dir, "index"), "runbooks",
		index.DirectoryConfiguration(sources))
	if err != nil {
		t.Fatal(err)
	}
	walk, err := index.WalkDirectory(t.Context(), sources)
	if err != nil {
		t.Fatal(err)
	}
	// Say so, then hang with the write transaction open until SIGKILL: no rollback, no
	// deferred close. What the parent reads afterwards is what the file holds.
	index.SetSeams(ix, nil, func() {
		if _, err := os.Stdout.WriteString("in transaction\n"); err != nil {
			t.Fatal(err)
		}
		select {}
	})
	_, err = ix.Rebuild(t.Context(), walk)
	t.Fatalf("the update returned past a seam that never releases: %v", err)
}

// SC-211, the kill. A rebuild over changed sources is killed with its write transaction
// open and its difference already applied; the index still names the previous generation
// and a search returns what that generation held. Killed rather than stopped, because an
// implementation that cleans up on its way out passes a graceful stop.
func TestAKilledRebuildLeavesThePreviousGeneration(t *testing.T) {
	dir := t.TempDir()
	sources := filepath.Join(dir, "sources")
	indexDir := filepath.Join(dir, "index")
	writeSources(t, sources, firstSources)

	before, err := func() (index.Generation, error) {
		ix, err := index.Open(t.Context(), indexDir, "runbooks", index.DirectoryConfiguration(sources))
		if err != nil {
			return index.Generation{}, err
		}
		defer func() { _ = ix.Close() }()
		return ix.Update(t.Context(), walked(t, sources))
	}()
	if err != nil {
		t.Fatal(err)
	}
	writeSources(t, sources, secondSources)

	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestHelperUpdateThenBlocks$")
	cmd.Env = append(os.Environ(), killHelperEnv+"="+dir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	reached := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		reached <- line
	}()
	select {
	case line := <-reached:
		if line != "in transaction\n" {
			t.Fatalf("the subprocess did not reach the seam: %q", line)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("the subprocess did not reach the seam within 60s")
	}
	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()

	stored, present, err := index.ReadStored(t.Context(), indexDir, "runbooks")
	if err != nil || !present {
		t.Fatalf("the index cannot be read after the kill: present=%v err=%v", present, err)
	}
	if stored.Generation.ID != before.ID {
		t.Fatalf("the index names generation %s after the kill, want the previous %s",
			stored.Generation.Short(), before.Short())
	}

	ix := openIndex(t, indexDir, sources)
	if got := sourcesOf(t, ix, "disk"); !reflect.DeepEqual(got, []string{"a.md"}) {
		t.Errorf("a search for disk returned %v after the kill, want the previous generation's [a.md]", got)
	}
	if got := sourcesOf(t, ix, "memory"); len(got) != 0 {
		t.Errorf("a search for memory returned %v after the kill: the killed rebuild's passages are visible", got)
	}
}

// SC-211, the interleaving. Update A reads the generation and is held there; update B,
// over the same sources, commits; A is released. What is left matches one full rebuild of
// the sources in a fresh index, row for row. The interleaving is injected rather than
// hoped for: two updates started together overlap only when the scheduler agrees to.
func TestConcurrentUpdatesLeaveOneWholeGeneration(t *testing.T) {
	dir := t.TempDir()
	sources := filepath.Join(dir, "sources")
	indexDir := filepath.Join(dir, "index")
	writeSources(t, sources, firstSources)

	first := openIndex(t, indexDir, sources)
	if _, err := first.Update(t.Context(), walked(t, sources)); err != nil {
		t.Fatal(err)
	}
	writeSources(t, sources, secondSources)
	walk := walked(t, sources)

	a := openIndex(t, indexDir, sources)
	b := openIndex(t, indexDir, sources)
	reached, release := make(chan struct{}), make(chan struct{})
	index.SetSeams(a, func() {
		close(reached)
		<-release
	}, nil)

	finished := make(chan error, 1)
	go func() {
		_, err := a.Update(t.Context(), walk)
		finished <- err
	}()
	select {
	case <-reached:
	case <-time.After(30 * time.Second):
		t.Fatal("update A never reached the seam after its read")
	}
	if _, err := b.Update(t.Context(), walk); err != nil {
		t.Fatalf("update B failed while A was held: %v", err)
	}
	close(release)
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("update A failed once released: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("update A did not finish within 30s of its release")
	}

	rebuilt := openIndex(t, filepath.Join(dir, "rebuilt"), sources)
	if _, err := rebuilt.Rebuild(t.Context(), walk); err != nil {
		t.Fatal(err)
	}
	got, err := index.Dump(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	want, err := index.Dump(t.Context(), rebuilt)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the index after two interleaved updates is not one rebuild:\n  got  %q\n  want %q", got, want)
	}
}
