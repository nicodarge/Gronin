package index

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// PassageBytes is the most a passage holds. It is set by the smallest embeddings context
// observed rather than by ranking (research.md §8), and it is the runtime's, not a
// playbook's or a deployment's.
const PassageBytes = 1024

// PassageRule names the rules below. It is part of a collection's configuration identity,
// so changing how text is cut rebuilds every index rather than mixing two cuts in one.
const PassageRule = "text-1"

// Passage is the unit indexed and retrieved. Its Source and Ordinal are FR-210's
// tie-break key, and Text is exactly the document's bytes at Offset.
type Passage struct {
	Source  string
	Ordinal int
	Offset  int64
	Text    string
}

// atxHeading is a Markdown heading of the kind that opens a passage. Setext underlines are
// deliberately not recognised: a manual page's section name then stays attached to its
// first paragraph, which is where a reader meets it anyway.
var atxHeading = regexp.MustCompile(`^#{1,6} `)

// span is a stretch of a document's bytes, and whether it begins with a heading.
type span struct {
	start, end int
	heading    bool
}

// TextPassages cuts a text document by research.md §8's rules: blank lines separate
// blocks, an ATX heading opens a passage, blocks pack in order while they fit the cap,
// and a block longer than the cap is cut at its last whitespace before it, or at a
// character boundary when it has none.
func TextPassages(source string, content []byte) []Passage {
	text := string(content)
	var passages []Passage
	emit := func(start, end int) {
		passages = append(passages, Passage{
			Source: source, Ordinal: len(passages) + 1, Offset: int64(start), Text: text[start:end],
		})
	}

	open := false
	var start, end int
	for _, block := range blocks(text) {
		for _, piece := range cut(text, block) {
			switch {
			case !open:
				start, end, open = piece.start, piece.end, true
			case piece.heading || piece.end-start > PassageBytes:
				emit(start, end)
				start, end = piece.start, piece.end
			default:
				end = piece.end
			}
		}
	}
	if open {
		emit(start, end)
	}
	return passages
}

// blocks splits text at blank lines and before every ATX heading. A block starts at its
// first non-blank character and ends after its last, so no passage begins or ends in
// whitespace and none is empty.
func blocks(text string) []span {
	var out []span
	current := span{start: -1}
	flush := func() {
		if current.start >= 0 {
			out = append(out, current)
		}
		current = span{start: -1}
	}

	for at := 0; at < len(text); {
		lineEnd, next := len(text), len(text)
		if newline := strings.IndexByte(text[at:], '\n'); newline >= 0 {
			lineEnd, next = at+newline, at+newline+1
		}
		line := text[at:lineEnd]
		if strings.TrimSpace(line) == "" {
			flush()
			at = next
			continue
		}
		if atxHeading.MatchString(line) {
			flush()
			current.heading = true
		}
		if current.start < 0 {
			current.start = at + len(line) - len(strings.TrimLeftFunc(line, unicode.IsSpace))
		}
		current.end = at + len(strings.TrimRightFunc(line, unicode.IsSpace))
		at = next
	}
	flush()
	return out
}

// cut divides a block longer than the cap into pieces that each fit it. Only the first
// piece carries the block's heading.
func cut(text string, block span) []span {
	var pieces []span
	start, heading := block.start, block.heading
	for block.end-start > PassageBytes {
		window := text[start : start+PassageBytes+1]
		end := start + boundary(text[start:], PassageBytes)
		if space := lastWhitespaceRun(window); space > 0 {
			end = start + space
		}
		rest := text[end:block.end]
		resume := end + len(rest) - len(strings.TrimLeftFunc(rest, unicode.IsSpace))
		pieces = append(pieces, span{start: start, end: end, heading: heading})
		start, heading = resume, false
	}
	return append(pieces, span{start: start, end: block.end, heading: heading})
}

// lastWhitespaceRun is where the last run of whitespace in window begins, or 0 when
// there is none past its first byte.
func lastWhitespaceRun(window string) int {
	at := len(window)
	for at > 0 {
		r, size := utf8.DecodeLastRuneInString(window[:at])
		if unicode.IsSpace(r) {
			break
		}
		at -= size
	}
	if at == 0 {
		return 0
	}
	for at > 0 {
		r, size := utf8.DecodeLastRuneInString(window[:at])
		if !unicode.IsSpace(r) {
			break
		}
		at -= size
	}
	return at
}

// boundary is the largest cut at or below limit that does not split a character.
func boundary(text string, limit int) int {
	if limit >= len(text) {
		return len(text)
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return limit
}
