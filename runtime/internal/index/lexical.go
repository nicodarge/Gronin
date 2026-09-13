package index

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode"
)

// Hit is one passage a search returned, with its BM25 score. The score orders one
// search's hits and means nothing against another's (research.md §3).
type Hit struct {
	Passage
	Score float64
}

// Found is what one search read: the generation, and its hits in rank order.
type Found struct {
	Generation Generation
	Hits       []Hit
}

// ErrNoWords is a query holding nothing the index can search. No search runs, so what
// comes back is not an empty result: FR-228 keeps "empty" for a search that ran and
// matched nothing.
var ErrNoWords = errors.New("the query holds no word the index can search")

// Search returns at most limit passages matching query, best first, and the generation
// they came from, read inside one transaction so the two cannot disagree (FR-219).
//
// Equal scores are ordered by source, then ordinal (FR-210), never by the order rows
// happen to have in the index.
func (ix *Index) Search(ctx context.Context, query string, limit int) (Found, error) {
	var found Found
	err := ix.whileBusy(ctx, func() error {
		var err error
		found, err = ix.search(ctx, query, limit)
		return err
	})
	return found, err
}

// A read-only BeginTx is deferred: SQLite takes no lock at BEGIN, only at the first
// statement that actually reads. So a genuine BUSY before this returns — not only
// BeginTx's own — can be that first statement finding the index held, and is reported
// that way, the same as readStored (index.go); anything else stays exactly as it is.
func (ix *Index) search(ctx context.Context, query string, limit int) (found Found, err error) {
	defer func() { err = asBegin(err) }()

	tx, err := ix.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Found{}, err
	}
	defer func() { _ = tx.Rollback() }()
	ix.seam(ix.seams.duringSearch)

	generation, err := readGeneration(ctx, tx)
	if err != nil {
		return Found{}, err
	}
	found = Found{Generation: generation}
	if !Searchable(query) {
		return found, ErrNoWords
	}
	match := plainQuery(query)

	rows, err := tx.QueryContext(ctx, `
		SELECT source, ordinal, start, text, bm25(passages)
		  FROM passages WHERE passages MATCH ?
		 ORDER BY bm25(passages), source, ordinal
		 LIMIT ?`, match, limit)
	if err != nil {
		return Found{}, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var hit Hit
		if err := rows.Scan(&hit.Source, &hit.Ordinal, &hit.Offset, &hit.Text, &hit.Score); err != nil {
			return Found{}, err
		}
		found.Hits = append(found.Hits, hit)
	}
	return found, rows.Err()
}

// Searchable reports whether query holds a word the index can search. The retrieve stage
// asks before it walks a collection, and Search asks again, so both refuse one query for
// one reason.
func Searchable(query string) bool {
	return plainQuery(query) != ""
}

// plainQuery turns text into an FTS5 query that holds no FTS5 syntax (FR-211): its words,
// each quoted, joined with OR.
//
// Words are split where the tokenizer splits them rather than at whitespace. Split at
// whitespace, `text:disk` is one quoted phrase, "text disk", which finds only those two
// words side by side — not what the same words find as plain terms (research.md §2).
func plainQuery(query string) string {
	words := strings.FieldsFunc(query, func(r rune) bool { return !indexedRune(r) })
	quoted := make([]string, 0, len(words))
	for _, word := range words {
		quoted = append(quoted, `"`+strings.ReplaceAll(word, `"`, `""`)+`"`)
	}
	return strings.Join(quoted, " OR ")
}

// indexedRune is a letter, a number or a private-use character by Go's Unicode tables:
// the categories unicode61 keeps in a token. The tokenizer judges them by Unicode 6.1's
// tables and folds diacritics its own way, so a character newer than 6.1 can be split
// here where it would not be there; each side of such a split is still quoted plain text.
func indexedRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.Is(unicode.Co, r)
}
