package index_test

import (
	"path/filepath"
	"reflect"
	"testing"
)

// SC-217. A query can be written by whoever sends a webhook, so FTS5's own language must
// not reach MATCH through it (research.md §2). Each hostile string returns what its words
// return searched as plain terms, and the set is named as well: two searches that both
// return nothing would otherwise agree.
func TestAQueryIsPlainText(t *testing.T) {
	dir := t.TempDir()
	sources := filepath.Join(dir, "sources")
	writeSources(t, sources, map[string]string{
		"a.md": "disk full on the var partition\n",
		"b.md": "logs rotated nightly\n",
		"c.md": "certificate expired\n",
		"d.md": "cert renewal\n",
		"e.md": "text encoding of a report\n",
		"f.md": "do not panic\n",
		"g.md": "and then nothing\n",
	})
	ix := openIndex(t, filepath.Join(dir, "index"), sources)
	if _, err := ix.Update(t.Context(), walked(t, sources)); err != nil {
		t.Fatal(err)
	}

	for hostile, probe := range map[string]struct {
		plain string
		want  []string
	}{
		// Read as FTS5, NOT removes the documents holding logs, and a.md alone comes back.
		"disk NOT logs": {plain: "disk not logs", want: []string{"a.md", "b.md", "f.md"}},
		// Read as FTS5, a prefix search, which also finds certificate.
		"cert*": {plain: "cert", want: []string{"d.md"}},
		// Read as FTS5, an unterminated string, and the search fails.
		`disk" AND (`: {plain: "disk and", want: []string{"a.md", "g.md"}},
		// Read as FTS5, a filter on the text column, which finds disk alone.
		"text:disk": {plain: "text disk", want: []string{"a.md", "e.md"}},
	} {
		t.Run(hostile, func(t *testing.T) {
			found, err := ix.Search(t.Context(), hostile, 50)
			if err != nil {
				t.Fatalf("the query errored: %v", err)
			}
			plain, err := ix.Search(t.Context(), probe.plain, 50)
			if err != nil {
				t.Fatalf("the plain query errored: %v", err)
			}
			if !reflect.DeepEqual(found.Hits, plain.Hits) {
				t.Errorf("%q and %q returned different results:\n  %+v\n  %+v",
					hostile, probe.plain, found.Hits, plain.Hits)
			}
			got := map[string]bool{}
			for _, hit := range found.Hits {
				got[hit.Source] = true
			}
			wanted := map[string]bool{}
			for _, source := range probe.want {
				wanted[source] = true
			}
			if !reflect.DeepEqual(got, wanted) {
				t.Errorf("%q found %v, want %v", hostile, got, wanted)
			}
		})
	}
}
