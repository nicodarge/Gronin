package retrieve

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nicodarge/Gronin/runtime/internal/config"
	"github.com/nicodarge/Gronin/runtime/internal/playbook"
)

// MaxQueryBytes is the longest query searched. It is the runtime's bound and no
// playbook's (research.md §9): a webhook's sender writes the query, and lexical cost grows
// faster than its length.
const MaxQueryBytes = 1024

// emptyQuery refuses a query that holds no word the index can search, whitespace alone
// included (FR-228).
func emptyQuery(truncated bool) error {
	if truncated {
		return errors.New("the query is empty once resolved: it holds no word the index can " +
			"search after it was cut to 1,024 bytes, so nothing was searched")
	}
	return errors.New("the query is empty once resolved: it holds no word the index can " +
		"search, so nothing was searched")
}

// queryOf is the query a retrieval searches: resolved, and cut to the bound.
func queryOf(
	cfg *config.Config, workDir string, declared playbook.Retrieval, trigger map[string]string,
) (query string, truncated bool, err error) {
	resolved, err := resolveQuery(cfg, workDir, declared, trigger)
	if err != nil {
		return "", false, err
	}
	query, truncated = cutQuery(resolved)
	return query, truncated, nil
}

// resolveQuery forms the query from `query`, interpolated against the trigger and the
// configuration as every playbook string is, or from `query_from`, the gathered file read
// whole. With query_from set nothing in the trigger is read: a gathered input is a file
// the run already wrote, and reading it adds no source to interpolation.
func resolveQuery(
	cfg *config.Config, workDir string, declared playbook.Retrieval, trigger map[string]string,
) (string, error) {
	if declared.QueryFrom != "" {
		data, err := os.ReadFile(filepath.Join(workDir, filepath.Base(declared.QueryFrom))) //nolint:gosec // a gather step's output in the run's working directory
		if err != nil {
			return "", fmt.Errorf("reading %s, the gathered input the query is formed from: %w",
				declared.QueryFrom, err)
		}
		return string(data), nil
	}
	if cfg == nil {
		return "", errors.New("this deployment has no configuration to resolve the query against")
	}
	return cfg.Interpolate(declared.Query, trigger)
}

// cutQuery holds a query to MaxQueryBytes, cut at its last whitespace before the bound, or
// at a character boundary when it has none.
func cutQuery(query string) (string, bool) {
	query = strings.TrimSpace(query)
	if len(query) <= MaxQueryBytes {
		return query, false
	}
	end := strings.LastIndexFunc(query[:MaxQueryBytes+1], unicode.IsSpace)
	if end <= 0 {
		end = MaxQueryBytes
		for end > 0 && !utf8.RuneStart(query[end]) {
			end--
		}
	}
	return strings.TrimRightFunc(query[:end], unicode.IsSpace), true
}
