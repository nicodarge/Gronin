package index

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
)

// Report is one document of a reports source, handed to the index the way a directory's
// files are: Source identifies it — a run, for the only caller there is — and Content is
// exactly what was recorded. The index knows nothing of runs; the caller is what reads a
// record store and a playbook's name (data-model.md's Source document, and research.md
// §8 for what of it is indexed).
type Report struct {
	Source  string
	Content []byte
}

// ReportsWalk is a reports source's Walk (FR-212): each report digested over its content
// as recorded, and cut by ReportPassages's rules. fetch is called again if the update has
// to work its difference out afresh because another update committed a generation
// meanwhile (FR-219); a retrieval's caller reads the record store there, since the index
// itself never does.
func ReportsWalk(ctx context.Context, fetch func(ctx context.Context) ([]Report, error)) (Walk, error) {
	reports, err := fetch(ctx)
	if err != nil {
		return Walk{}, err
	}
	return reportsWalkOf(reports, fetch), nil
}

func reportsWalkOf(reports []Report, fetch func(ctx context.Context) ([]Report, error)) Walk {
	held := make(map[string][]byte, len(reports))
	documents := make([]Document, 0, len(reports))
	for _, report := range reports {
		sum := sha256.Sum256(report.Content)
		documents = append(documents, Document{
			Source: report.Source, Digest: hex.EncodeToString(sum[:]), Bytes: int64(len(report.Content)),
		})
		held[report.Source] = report.Content
	}
	return Walk{
		Documents: documents,
		held:      held,
		passages:  ReportPassages,
		rewalk: func(ctx context.Context) (Walk, error) {
			return ReportsWalk(ctx, fetch)
		},
	}
}
