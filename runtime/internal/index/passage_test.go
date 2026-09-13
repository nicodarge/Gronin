package index_test

import (
	"strings"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/index"
)

// research.md §8's rules for text, one row per rule. Each row states the passages it
// expects exactly, because a cut in the wrong place still produces passages that look
// reasonable, and only their boundaries say whether the rule was applied.
func TestPassagesFollowTheTextRules(t *testing.T) {
	long := strings.TrimSpace(strings.Repeat("abcdefghi ", 150))
	unbroken := strings.Repeat("a", index.PassageBytes-1) + "é" + "bbb"
	sixHundred := strings.Repeat("x", 600)

	for name, probe := range map[string]struct {
		content string
		want    []index.Passage
	}{
		"blank lines separate blocks, which pack while they fit": {
			content: "disk full\n\non /var\n",
			want:    []index.Passage{{Ordinal: 1, Offset: 0, Text: "disk full\n\non /var"}},
		},
		"blocks that do not fit together open a second passage": {
			content: sixHundred + "\n\n" + sixHundred + "\n",
			want: []index.Passage{
				{Ordinal: 1, Offset: 0, Text: sixHundred},
				{Ordinal: 2, Offset: 602, Text: sixHundred},
			},
		},
		"an ATX heading opens a passage": {
			content: "intro\n\n# Title\nbody\n",
			want: []index.Passage{
				{Ordinal: 1, Offset: 0, Text: "intro"},
				{Ordinal: 2, Offset: 7, Text: "# Title\nbody"},
			},
		},
		"an ATX heading inside a block opens a passage": {
			content: "intro\n## Next\nbody",
			want: []index.Passage{
				{Ordinal: 1, Offset: 0, Text: "intro"},
				{Ordinal: 2, Offset: 6, Text: "## Next\nbody"},
			},
		},
		"a hash with no space after it is not a heading": {
			content: "intro\n\n#hashtag\nbody",
			want:    []index.Passage{{Ordinal: 1, Offset: 0, Text: "intro\n\n#hashtag\nbody"}},
		},
		"a setext underline does not open a passage": {
			content: "intro\n\nTitle\n=====\nbody\n",
			want:    []index.Passage{{Ordinal: 1, Offset: 0, Text: "intro\n\nTitle\n=====\nbody"}},
		},
		"a longer block is cut at its last whitespace before the cap": {
			content: long,
			want: []index.Passage{
				{Ordinal: 1, Offset: 0, Text: long[:1019]},
				{Ordinal: 2, Offset: 1020, Text: long[1020:]},
			},
		},
		"a block with no whitespace is cut at a character boundary": {
			content: unbroken,
			want: []index.Passage{
				{Ordinal: 1, Offset: 0, Text: strings.Repeat("a", index.PassageBytes-1)},
				{Ordinal: 2, Offset: int64(index.PassageBytes - 1), Text: "ébbb"},
			},
		},
		"leading blank lines and indentation are not part of a passage": {
			content: "\n\n  indented\n",
			want:    []index.Passage{{Ordinal: 1, Offset: 4, Text: "indented"}},
		},
		"a document of blank lines has no passage": {
			content: "\n \n\t\n",
			want:    nil,
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := index.TextPassages("doc.md", []byte(probe.content))
			for at := range probe.want {
				probe.want[at].Source = "doc.md"
			}
			if len(got) != len(probe.want) {
				t.Fatalf("got %d passages, want %d:\n  got  %+v\n  want %+v",
					len(got), len(probe.want), got, probe.want)
			}
			for at := range got {
				if got[at] != probe.want[at] {
					t.Errorf("passage %d:\n  got  %+v\n  want %+v", at+1, got[at], probe.want[at])
				}
			}
			holdsTheInvariants(t, probe.content, got)
		})
	}
}

// What every passage of every document must be, whichever rule produced it: never
// empty, never over the cap, numbered from 1, and exactly the bytes the document holds
// at its offset.
func holdsTheInvariants(t *testing.T, content string, passages []index.Passage) {
	t.Helper()
	for at, passage := range passages {
		if strings.TrimSpace(passage.Text) == "" {
			t.Errorf("passage %d is empty", at+1)
		}
		if len(passage.Text) > index.PassageBytes {
			t.Errorf("passage %d is %d bytes, over the cap of %d", at+1, len(passage.Text), index.PassageBytes)
		}
		if passage.Ordinal != at+1 {
			t.Errorf("passage %d carries ordinal %d", at+1, passage.Ordinal)
		}
		end := passage.Offset + int64(len(passage.Text))
		if passage.Offset < 0 || end > int64(len(content)) || content[passage.Offset:end] != passage.Text {
			t.Errorf("passage %d is not the document's bytes at offset %d", at+1, passage.Offset)
		}
	}
}

// A document of many kinds of block, far past the cap, cut by every rule at once.
func TestPassagesOfALargeDocumentHoldTheInvariants(t *testing.T) {
	var document strings.Builder
	for section := range 40 {
		document.WriteString("# Section\n\n")
		document.WriteString(strings.Repeat("word ", 30+section*7))
		document.WriteString("\n\n")
		document.WriteString(strings.Repeat("é", 300+section*11))
		document.WriteString("\nunderlined\n---\n\n")
	}
	content := document.String()
	passages := index.TextPassages("large.md", []byte(content))
	if len(passages) < 40 {
		t.Fatalf("a document of 40 headed sections produced %d passages", len(passages))
	}
	holdsTheInvariants(t, content, passages)
}
