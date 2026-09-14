package index_test

import (
	"context"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/index"
)

// research.md §8's rules for a report, one row per rule. Each row states the passages it
// expects exactly by their text, since a cut in the wrong place still produces passages
// that look reasonable, and only their content and order say whether the rule was
// applied. Offsets are not asserted: they are into the report's joined values, which
// nothing outside this package reads back the way a directory's own bytes are.
func TestReportPassagesFollowTheReportRules(t *testing.T) {
	for name, probe := range map[string]struct {
		report string
		want   []string
	}{
		"key names are not indexed": {
			report: `{"disk": "the value, not the key"}`,
			want:   []string{"the value, not the key"},
		},
		"booleans and nulls are not indexed": {
			report: `{"ok": true, "note": null, "kept": "the only value"}`,
			want:   []string{"the only value"},
		},
		"values keep document order, not key order": {
			report: `{"b": "second", "a": "first"}`,
			want:   []string{"second\n\nfirst"},
		},
		"each object that is an element of an array opens a passage": {
			report: `{"findings": [
				{"title": "first finding", "body": "alpha"},
				{"title": "second finding", "body": "beta"}
			]}`,
			want: []string{"first finding\n\nalpha", "second finding\n\nbeta"},
		},
		"a plain array's elements do not each open a passage": {
			report: `{"tags": ["disk", "swap"]}`,
			want:   []string{"disk\n\nswap"},
		},
		"a report holding no values is no document": {
			report: `{"ok": true, "tags": []}`,
			want:   nil,
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := index.ReportPassages("run-1", []byte(probe.report))
			if len(got) != len(probe.want) {
				t.Fatalf("got %d passages, want %d:\n  got  %+v\n  want %+v",
					len(got), len(probe.want), got, probe.want)
			}
			var lastOffset int64 = -1
			for at, passage := range got {
				if passage.Text != probe.want[at] {
					t.Errorf("passage %d text = %q, want %q", at+1, passage.Text, probe.want[at])
				}
				if passage.Ordinal != at+1 {
					t.Errorf("passage %d carries ordinal %d", at+1, passage.Ordinal)
				}
				if passage.Source != "run-1" {
					t.Errorf("passage %d carries source %q", at+1, passage.Source)
				}
				if passage.Offset <= lastOffset {
					t.Errorf("passage %d's offset %d does not follow passage %d's", at+1, passage.Offset, at)
				}
				lastOffset = passage.Offset
				if len(passage.Text) > index.PassageBytes {
					t.Errorf("passage %d is %d bytes, over the cap of %d", at+1, len(passage.Text), index.PassageBytes)
				}
			}
		})
	}
}

// Indexed as JSON text, the escaped newline before "The" joins it into one token with the
// "n" before it — research.md §8's probe. Indexed as the decoded value, the newline is an
// actual line break, which the tokenizer splits on like any other.
func TestReportPassagesQueryDoesNotJoinAnEscapedNewline(t *testing.T) {
	report := []byte(`{"detail": "root partition\nThe disk is full"}`)
	ix := openReportsIndex(t, t.TempDir(), []index.Report{{Source: "run-1", Content: report}})

	found, err := ix.Search(t.Context(), "nthe", 50)
	if err != nil {
		t.Fatalf("the query errored: %v", err)
	}
	if len(found.Hits) != 0 {
		t.Errorf("query %q matched %d passages; indexing the JSON text would have matched "+
			"the escaped newline", "nthe", len(found.Hits))
	}

	found, err = ix.Search(t.Context(), "disk", 50)
	if err != nil {
		t.Fatalf("the query errored: %v", err)
	}
	if len(found.Hits) != 1 {
		t.Fatalf("query %q matched %d passages, want 1", "disk", len(found.Hits))
	}
}

// openReportsIndex opens a fresh index over a fixed list of reports, and updates it once.
func openReportsIndex(t *testing.T, dir string, reports []index.Report) *index.Index {
	t.Helper()
	names := make([]string, len(reports))
	for at, one := range reports {
		names[at] = one.Source
	}
	ix, err := index.Open(t.Context(), dir, "conclusions", index.ReportsConfiguration(names))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ix.Close() })

	walk, err := index.ReportsWalk(t.Context(), func(context.Context) ([]index.Report, error) {
		return reports, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Update(t.Context(), walk); err != nil {
		t.Fatal(err)
	}
	return ix
}
