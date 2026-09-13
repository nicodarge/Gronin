package index_test

import (
	"path/filepath"
	"reflect"
	"testing"
)

// SC-216, the lexical half. Three documents holding the same words at the same length tie
// exactly (research.md §3), and they reach the index in the reverse of FR-210's key: each
// update adds one, z before m before a, so the index's own row order is z, m, a. A fixture
// written in the key's order passes with the tie-break deleted.
func TestTiesFollowTheKey(t *testing.T) {
	dir := t.TempDir()
	sources := filepath.Join(dir, "sources")
	ix := openIndex(t, filepath.Join(dir, "index"), sources)

	added := map[string]string{}
	for _, name := range []string{"z.md", "m.md", "a.md"} {
		added[name] = "disk full on var\n"
		writeSources(t, sources, added)
		if _, err := ix.Update(t.Context(), walked(t, sources)); err != nil {
			t.Fatal(err)
		}
	}

	want := []string{"a.md", "m.md", "z.md"}
	for attempt := range 10 {
		found, err := ix.Search(t.Context(), "disk", 10)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, hit := range found.Hits {
			got = append(got, hit.Source)
			if hit.Score != found.Hits[0].Score {
				t.Fatalf("the fixture does not tie: %v scored %v, %v scored %v",
					hit.Source, hit.Score, found.Hits[0].Source, found.Hits[0].Score)
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("search %d returned %v, want the key's order %v", attempt+1, got, want)
		}
	}
}
