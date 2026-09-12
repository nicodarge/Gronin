package record

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// RetrievalMode is how a collection was searched. It is the collection's, derived from
// whether the deployment gave it an embeddings endpoint, and never a playbook's.
type RetrievalMode string

// The two modes a retrieval can run under.
const (
	RetrievalLexical  RetrievalMode = "lexical"
	RetrievalSemantic RetrievalMode = "semantic"
)

// RetrievalOutcome is what one search came to.
type RetrievalOutcome string

// Refused is not empty: nothing was searched, and the run never reached the agent.
const (
	RetrievalFound   RetrievalOutcome = "found"
	RetrievalEmpty   RetrievalOutcome = "empty"
	RetrievalRefused RetrievalOutcome = "refused"
)

// Retrieval is one search a run performed, a child of the run as its gathered inputs
// are. It holds what the run saw rather than a pointer into the index, which moves under
// a run's feet.
type Retrieval struct {
	// Sequence is the retrieval's position in the playbook's retrieve list, from 1.
	Sequence   int
	AsName     string
	Collection string
	Mode       RetrievalMode
	// Generation, GenerationBuiltAt and Identity are empty for a retrieval refused
	// before it read a generation.
	Generation        string
	GenerationBuiltAt time.Time
	Identity          string
	// Query is the query as searched: resolved, and cut to the runtime's bound.
	Query          string
	QueryTruncated bool
	Outcome        RetrievalOutcome
	// ResultsRef is the results file byte for byte as the agent received it, empty when
	// the retrieval was refused.
	ResultsRef     string
	ResultsBytes   int64
	CountTruncated bool
	BytesTruncated bool
	Error          string
	Items          []RetrievedItem
}

// RetrievedItem is one result, as the agent received it.
type RetrievedItem struct {
	Rank int
	// Score is the evidence of a rank, not a measure: it is comparable only within one
	// retrieval.
	Score      float64
	Source     string
	Ordinal    int
	Offset     int64
	ContentRef string
}

// AddRetrieval writes a retrieval and its items in one transaction, so a reader never
// sees a retrieval whose results are half there.
func (s *Store) AddRetrieval(ctx context.Context, runID string, retrieval Retrieval) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO retrievals (run_id, sequence, as_name, collection, mode, generation,
		                        generation_built_at, identity, query, query_truncated,
		                        outcome, results_ref, results_bytes, count_truncated,
		                        bytes_truncated, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		runID, retrieval.Sequence, s.redactor.Redact(retrieval.AsName),
		s.redactor.Redact(retrieval.Collection), string(retrieval.Mode),
		nullable(retrieval.Generation), formatTime(retrieval.GenerationBuiltAt),
		nullable(retrieval.Identity), s.redactor.Redact(retrieval.Query),
		retrieval.QueryTruncated, string(retrieval.Outcome), nullable(retrieval.ResultsRef),
		retrieval.ResultsBytes, retrieval.CountTruncated, retrieval.BytesTruncated,
		nullable(s.redactor.Redact(retrieval.Error)),
	); err != nil {
		return fmt.Errorf("recording retrieval %d of run %s: %w", retrieval.Sequence, runID, err)
	}

	for _, item := range retrieval.Items {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO retrieved_items (run_id, sequence, rank, score, source, ordinal,
			                             "offset", content_ref)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			runID, retrieval.Sequence, item.Rank, item.Score, s.redactor.Redact(item.Source),
			item.Ordinal, item.Offset, nullable(item.ContentRef),
		); err != nil {
			return fmt.Errorf("recording result %d of retrieval %d of run %s: %w",
				item.Rank, retrieval.Sequence, runID, err)
		}
	}
	return tx.Commit()
}

// Retrievals reads a run's retrievals back in declared order, each with its items in
// rank order. A record nobody can read back is not a record.
func (s *Store) Retrievals(ctx context.Context, runID string) ([]Retrieval, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT sequence, as_name, collection, mode, generation, generation_built_at,
		       identity, query, query_truncated, outcome, results_ref, results_bytes,
		       count_truncated, bytes_truncated, error
		  FROM retrievals WHERE run_id = ? ORDER BY sequence`, runID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var retrievals []Retrieval
	for rows.Next() {
		var (
			retrieval            Retrieval
			generation, builtAt  sql.NullString
			identity, resultsRef sql.NullString
			failure              sql.NullString
			mode, outcome        string
		)
		if err := rows.Scan(&retrieval.Sequence, &retrieval.AsName, &retrieval.Collection,
			&mode, &generation, &builtAt, &identity, &retrieval.Query,
			&retrieval.QueryTruncated, &outcome, &resultsRef, &retrieval.ResultsBytes,
			&retrieval.CountTruncated, &retrieval.BytesTruncated, &failure); err != nil {
			return nil, err
		}
		retrieval.Mode, retrieval.Outcome = RetrievalMode(mode), RetrievalOutcome(outcome)
		retrieval.Generation, retrieval.Identity = generation.String, identity.String
		retrieval.GenerationBuiltAt = parseTime(builtAt)
		retrieval.ResultsRef, retrieval.Error = resultsRef.String, failure.String
		retrievals = append(retrievals, retrieval)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for at := range retrievals {
		items, err := s.retrievedItems(ctx, runID, retrievals[at].Sequence)
		if err != nil {
			return nil, err
		}
		retrievals[at].Items = items
	}
	return retrievals, nil
}

func (s *Store) retrievedItems(ctx context.Context, runID string, sequence int) ([]RetrievedItem, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT rank, score, source, ordinal, "offset", content_ref
		  FROM retrieved_items WHERE run_id = ? AND sequence = ? ORDER BY rank`,
		runID, sequence)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var items []RetrievedItem
	for rows.Next() {
		var (
			item    RetrievedItem
			content sql.NullString
		)
		if err := rows.Scan(&item.Rank, &item.Score, &item.Source, &item.Ordinal,
			&item.Offset, &content); err != nil {
			return nil, err
		}
		item.ContentRef = content.String
		items = append(items, item)
	}
	return items, rows.Err()
}
