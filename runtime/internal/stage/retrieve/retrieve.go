package retrieve

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nicodarge/Gronin/runtime/internal/collections"
	"github.com/nicodarge/Gronin/runtime/internal/config"
	"github.com/nicodarge/Gronin/runtime/internal/index"
	"github.com/nicodarge/Gronin/runtime/internal/playbook"
	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// Stage retrieves for one deployment.
type Stage struct {
	// Catalog is the collections this deployment declares.
	Catalog *collections.Catalog
	// IndexDir holds one index per collection, in the state directory (FR-216).
	IndexDir string
	// Config resolves a query's ${config.x} references.
	Config *config.Config
	// Redactor is the record store's own, applied to the results file before the agent
	// reads it, so the agent and the record hold one text.
	Redactor *record.Redactor
}

// Retrieved is what one retrieval did, as the record needs it: the row, the results file
// the agent read, and each item's content as the file holds it. Results is nil for a
// refused retrieval.
type Retrieved struct {
	Record   record.Retrieval
	Results  []byte
	Contents []string
}

// Refused is a retrieval that could not run as declared, and it refuses the run
// (FR-202). It names the retrieval by its position and its collection.
type Refused struct {
	Position   int
	Collection string
	Cause      error
}

func (r *Refused) Error() string {
	return fmt.Sprintf("retrieve[%d] from %s: %v", r.Position, r.Collection, r.Cause)
}

func (r *Refused) Unwrap() error { return r.Cause }

// Run performs a playbook's retrievals in declared order and stops at the first refusal.
// Every retrieval it reached is returned, the refused one included, so the record says
// which one refused and why.
func (s *Stage) Run(
	ctx context.Context, workDir string, retrievals []playbook.Retrieval, trigger map[string]string,
) ([]Retrieved, error) {
	done := make([]Retrieved, 0, len(retrievals))
	for at, declared := range retrievals {
		one, err := s.retrieve(ctx, workDir, at, declared, trigger)
		done = append(done, one)
		if err != nil {
			return done, &Refused{Position: at, Collection: declared.Collection, Cause: err}
		}
	}
	return done, nil
}

func (s *Stage) retrieve(
	ctx context.Context, workDir string, at int, declared playbook.Retrieval,
	trigger map[string]string,
) (Retrieved, error) {
	row := record.Retrieval{
		Sequence: at + 1, AsName: declared.As, Collection: declared.Collection,
		Mode: record.RetrievalLexical,
	}
	refuse := func(err error) (Retrieved, error) {
		row.Outcome, row.Error = record.RetrievalRefused, err.Error()
		return Retrieved{Record: row}, err
	}

	collection, err := s.collection(declared.Collection)
	if collection.Mode() == collections.Semantic {
		row.Mode = record.RetrievalSemantic
	}
	if err != nil {
		return refuse(err)
	}

	query, truncated, err := queryOf(s.Config, workDir, declared, trigger)
	row.Query, row.QueryTruncated = query, truncated
	if err != nil {
		return refuse(err)
	}
	// Before the collection is walked or its index opened: a query with nothing to search
	// is refused for itself, not for a directory it never needed.
	if !index.Searchable(query) {
		return refuse(emptyQuery(truncated))
	}

	// FR-209: one bound over the walk, the update and the search.
	bounded, cancel := context.WithTimeout(ctx, collection.RetrievalTimeout)
	defer cancel()
	found, err := s.search(bounded, collection, query, declared.ResultCount()+1)
	switch {
	case errors.Is(err, index.ErrNoWords):
		return refuse(emptyQuery(truncated))
	case err != nil:
		if errors.Is(bounded.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			err = fmt.Errorf("it did not complete within %s, the collection's retrieval_timeout: %w",
				collection.RetrievalTimeout, err)
		}
		return refuse(err)
	}
	row.Generation, row.Identity = found.Generation.ID, found.Generation.Identity
	row.GenerationBuiltAt = found.Generation.BuiltAt

	hits, countTruncated := capResults(found.Hits, declared.ResultCount())
	file, items, bytesTruncated := render(heading{
		collection: collection.Name, mode: row.Mode,
		generation: found.Generation.Short(), query: query,
	}, hits, declared.ByteBound(), s.Redactor)
	if err := os.WriteFile(filepath.Join(workDir, filepath.Base(declared.As)), file, 0o600); err != nil {
		return refuse(fmt.Errorf("writing the results into the working directory: %w", err))
	}

	// What the agent was handed decides the outcome, not what matched: a byte bound too
	// small for any passage hands it none.
	row.Outcome = record.RetrievalFound
	if len(items) == 0 {
		row.Outcome = record.RetrievalEmpty
	}
	row.ResultsBytes = int64(len(file))
	row.CountTruncated, row.BytesTruncated = countTruncated, bytesTruncated
	retrieved := Retrieved{Results: file}
	for _, item := range items {
		row.Items = append(row.Items, record.RetrievedItem{
			Rank: item.rank, Score: item.hit.Score, Source: item.hit.Source,
			Ordinal: item.hit.Ordinal, Offset: item.hit.Offset,
		})
		retrieved.Contents = append(retrieved.Contents, item.text)
	}
	retrieved.Record = row
	return retrieved, nil
}

// collection is the declared collection a retrieval names, refused when this runtime
// cannot search it: a collection nothing can search reads, in review, as available.
func (s *Stage) collection(name string) (collections.Collection, error) {
	if s.Catalog == nil {
		return collections.Collection{}, fmt.Errorf("%q is not a collection this deployment declares", name)
	}
	collection, found := s.Catalog.Get(name)
	if !found {
		return collections.Collection{}, fmt.Errorf("%q is not a collection this deployment declares", name)
	}
	if collection.Mode() != collections.Lexical || collection.Directory == "" {
		return collection, fmt.Errorf("%q is not a lexical collection over a directory, "+
			"the only kind this runtime searches", name)
	}
	return collection, nil
}

// search brings the collection's index up to date with its directory and searches it.
func (s *Stage) search(
	ctx context.Context, collection collections.Collection, query string, limit int,
) (index.Found, error) {
	walk, err := index.WalkDirectory(ctx, collection.Directory)
	if err != nil {
		return index.Found{}, err
	}
	ix, err := index.Open(ctx, s.IndexDir, collection.Name, index.DirectoryConfiguration(collection.Directory))
	if err != nil {
		return index.Found{}, err
	}
	defer func() { _ = ix.Close() }()
	if _, err := ix.Update(ctx, walk); err != nil {
		return index.Found{}, fmt.Errorf("updating the index of %s: %w", collection.Name, err)
	}
	return ix.Search(ctx, query, limit)
}
