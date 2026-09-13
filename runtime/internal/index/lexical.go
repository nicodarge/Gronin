package index

import (
	"context"
	"database/sql"
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

// Search returns at most limit passages matching query, best first, and the generation
// they came from, read inside one transaction so the two cannot disagree (FR-219).
//
// Equal scores are ordered by source, then ordinal (FR-210), never by the order rows
// happen to have in the index.
func (ix *Index) Search(ctx context.Context, query string, limit int) (Found, error) {
	tx, err := ix.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Found{}, err
	}
	defer func() { _ = tx.Rollback() }()

	generation, err := readGeneration(ctx, tx)
	if err != nil {
		return Found{}, err
	}
	found := Found{Generation: generation}
	match := plainQuery(query)
	if match == "" {
		return found, nil
	}

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

// plainQuery turns text into an FTS5 query that holds no FTS5 syntax (FR-211): the words
// the tokenizer would index, each quoted, joined with OR.
//
// Words are split where the tokenizer splits them rather than at whitespace. Split at
// whitespace, `text:disk` is one quoted phrase, "text disk", which finds only those two
// words side by side — not what the same words find as plain terms.
func plainQuery(query string) string {
	words := strings.FieldsFunc(query, func(r rune) bool { return !indexedRune(r) })
	quoted := make([]string, 0, len(words))
	for _, word := range words {
		quoted = append(quoted, `"`+strings.ReplaceAll(word, `"`, `""`)+`"`)
	}
	return strings.Join(quoted, " OR ")
}

// indexedRune is a character unicode61 keeps in a token: a letter, a number, or a
// private-use character. Everything else separates tokens.
func indexedRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.Is(unicode.Co, r)
}
