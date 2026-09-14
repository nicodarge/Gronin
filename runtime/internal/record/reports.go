package record

import (
	"context"
	"fmt"
	"strings"
)

// IndexableReport is one run whose report is a source document for a reports collection.
type IndexableReport struct {
	RunID     string
	ReportRef string
}

// IndexableReports is every run of the named playbooks whose report is a source document
// (data-model.md): its run recorded a report and derives from no other run. The rule is
// stated on those two facts — report_ref set, parent_run_id null — rather than on a list
// of trigger kinds, so a run begun by a trigger kind added later is indexed without this
// feature changing. Runs are ordered by identifier, FR-210's tie-break key.
func (s *Store) IndexableReports(ctx context.Context, playbooks []string) ([]IndexableReport, error) {
	if len(playbooks) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(playbooks))
	args := make([]any, len(playbooks))
	for at, name := range playbooks {
		placeholders[at] = "?"
		args[at] = s.redactor.Redact(name)
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT id, report_ref FROM runs
		 WHERE playbook_name IN (%s) AND report_ref IS NOT NULL AND parent_run_id IS NULL
		 ORDER BY id`, strings.Join(placeholders, ", ")), args...)
	if err != nil {
		return nil, fmt.Errorf("reading indexable reports: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var reports []IndexableReport
	for rows.Next() {
		var report IndexableReport
		if err := rows.Scan(&report.RunID, &report.ReportRef); err != nil {
			return nil, err
		}
		reports = append(reports, report)
	}
	return reports, rows.Err()
}
