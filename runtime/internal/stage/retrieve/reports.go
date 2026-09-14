package retrieve

import (
	"context"
	"fmt"

	"github.com/nicodarge/Gronin/runtime/internal/collections"
	"github.com/nicodarge/Gronin/runtime/internal/index"
	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// SourceWalk builds a collection's Walk and configuration identity, from its directory or
// from its playbooks' recorded reports (FR-212). The stage and the operator's collections
// commands share this, so a listing and a retrieval see the same sources.
func SourceWalk(
	ctx context.Context, store *record.Store, collection collections.Collection,
) (index.Walk, index.Configuration, error) {
	if collection.Directory != "" {
		walk, err := index.WalkDirectory(ctx, collection.Directory)
		return walk, index.DirectoryConfiguration(collection.Directory), err
	}
	fetch := func(ctx context.Context) ([]index.Report, error) {
		return ReportsOf(ctx, store, collection.Reports)
	}
	walk, err := index.ReportsWalk(ctx, fetch)
	return walk, index.ReportsConfiguration(collection.Reports), err
}

// ReportsOf reads the reports a reports collection indexes from the record store: the
// runs of its playbooks whose report is a source document (data-model.md), each fetched
// as an index.Report by its blob reference. The index package never reads the record
// store itself — it is handed documents, the way a directory's files are.
func ReportsOf(ctx context.Context, store *record.Store, playbooks []string) ([]index.Report, error) {
	if store == nil {
		return nil, fmt.Errorf("this deployment was given no record store to read reports from")
	}
	indexable, err := store.IndexableReports(ctx, playbooks)
	if err != nil {
		return nil, err
	}
	reports := make([]index.Report, 0, len(indexable))
	for _, one := range indexable {
		content, err := store.Blobs().Get(one.ReportRef)
		if err != nil {
			return nil, fmt.Errorf("reading the report of run %s: %w", one.RunID, err)
		}
		reports = append(reports, index.Report{Source: one.RunID, Content: content})
	}
	return reports, nil
}
