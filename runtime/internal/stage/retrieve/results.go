package retrieve

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/nicodarge/Gronin/runtime/internal/index"
	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// nothingMatched is what the agent is told when a search ran and matched nothing, rather
// than being handed an empty file it could read as a failure.
const nothingMatched = "Nothing in this collection matched the query."

// capResults keeps the first limit hits, and reports whether more matched.
func capResults(hits []index.Hit, limit int) ([]index.Hit, bool) {
	if len(hits) > limit {
		return hits[:limit], true
	}
	return hits, false
}

// heading is what the results file's first two lines name.
type heading struct {
	collection string
	mode       record.RetrievalMode
	generation string
	query      string
}

// written is one result as the file holds it: cut, when the byte bound cut it.
type written struct {
	rank int
	hit  index.Hit
	text string
}

// piece is one stretch of the results file: a heading or a result's opening line, and a
// result's passage.
type piece struct {
	head string
	body string
	rank int
	hit  *index.Hit
}

// render writes contracts/cli.md's results file within maxBytes. Every part is redacted
// before it is sized, so the bound holds for the file the agent reads and the record keeps.
//
// A result that does not fit what remains is cut at the last character boundary that
// fits, the results after it are dropped, and the file says so on its last line.
func render(head heading, hits []index.Hit, maxBytes int, redactor *record.Redactor) ([]byte, []written, bool) {
	pieces := []piece{{head: fmt.Sprintf("# Retrieved from %s (%s, generation %s)\n# Query: %s\n",
		head.collection, head.mode, head.generation,
		strings.Join(strings.Fields(redactor.Redact(head.query)), " "))}}
	if len(hits) == 0 {
		pieces = append(pieces, piece{head: "\n" + nothingMatched + "\n"})
	}
	for at := range hits {
		hit := &hits[at]
		pieces = append(pieces, piece{
			head: fmt.Sprintf("\n## %d. %s, passage %d (score %.4f)\n\n",
				at+1, redactor.Redact(hit.Source), hit.Ordinal, hit.Score),
			body: redactor.Redact(hit.Text) + "\n",
			rank: at + 1,
			hit:  hit,
		})
	}

	total := 0
	for _, one := range pieces {
		total += len(one.head) + len(one.body)
	}
	if total <= maxBytes {
		out, items := fill(pieces, total)
		return out, items, false
	}

	trailer := fmt.Sprintf("\n(cut to fit %d bytes)\n", maxBytes)
	out, items := fill(pieces, max(maxBytes-len(trailer), 0))
	out = append(out, trailer...)
	if len(out) > maxBytes {
		out = out[:cutAt(string(out), maxBytes)]
	}
	return out, items, true
}

// fill writes pieces in order until budget is spent, cutting the one that does not fit.
func fill(pieces []piece, budget int) ([]byte, []written) {
	var out []byte
	var items []written
	for _, one := range pieces {
		full := one.head + one.body
		keep := len(full)
		if keep > budget-len(out) {
			keep = cutAt(full, budget-len(out))
		}
		out = append(out, full[:keep]...)
		if one.hit != nil && keep > len(one.head) {
			text := strings.TrimSuffix(full[len(one.head):keep], "\n")
			items = append(items, written{rank: one.rank, hit: *one.hit, text: text})
		}
		if keep < len(full) {
			break
		}
	}
	return out, items
}

// cutAt is the largest length at or below limit that does not split a character.
func cutAt(text string, limit int) int {
	if limit >= len(text) {
		return len(text)
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return limit
}
