package retrieve_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nicodarge/Gronin/runtime/internal/playbook"
)

// SC-218, each fixture past its bound: a fixture inside a bound passes whether or not
// the bound exists.
func TestBoundsCutAQueryPastItsLength(t *testing.T) {
	stage, workDir := stageOver(t, runbooks)
	long := strings.TrimSpace(strings.Repeat("disk ", 300))
	if len(long) <= 1024 {
		t.Fatalf("the fixture is %d bytes, inside the bound it is meant to exceed", len(long))
	}

	got, err := retrieveOne(t, stage, workDir, playbook.Retrieval{
		Collection: "runbooks", As: "runbooks.md", Query: long,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	query := got.Record.Query
	if len(query) > 1024 {
		t.Fatalf("the query was searched at %d bytes, past the bound of 1024", len(query))
	}
	if !got.Record.QueryTruncated {
		t.Error("the query was cut and the record does not say so")
	}
	// Cut at the last whitespace before the bound: whole words, and nearly all of them.
	if !strings.HasPrefix(long, query) || !strings.HasSuffix(query, "disk") || len(query) < 1000 {
		t.Errorf("the query was not cut at its last whitespace before the bound: %d bytes ending %q",
			len(query), query[max(0, len(query)-12):])
	}
}

func TestBoundsCapTheResultCount(t *testing.T) {
	docs := map[string]string{}
	for at := range 30 {
		docs[fmt.Sprintf("note-%02d.md", at)] = fmt.Sprintf("disk incident %d\n", at)
	}
	stage, workDir := stageOver(t, docs)

	got, err := retrieveOne(t, stage, workDir, playbook.Retrieval{
		Collection: "runbooks", As: "runbooks.md", Query: "disk", MaxResults: 10,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Record.Items) != 10 {
		t.Errorf("30 passages matched and %d were returned, want 10", len(got.Record.Items))
	}
	if !got.Record.CountTruncated {
		t.Error("more matched than max_results and the record does not say so")
	}
	if !strings.Contains(string(got.Results), "## 10. ") || strings.Contains(string(got.Results), "## 11. ") {
		t.Errorf("the results file does not hold exactly ten results:\n%s", got.Results)
	}
}

func TestBoundsCutTheResultsFileToItsBytes(t *testing.T) {
	docs := map[string]string{}
	for at := range 10 {
		// Two-byte characters, so a cut that ignores character boundaries leaves half of one.
		docs[fmt.Sprintf("note-%02d.md", at)] = "disk " + strings.Repeat("é", 100) + "\n"
	}
	stage, workDir := stageOver(t, docs)

	got, err := retrieveOne(t, stage, workDir, playbook.Retrieval{
		Collection: "runbooks", As: "runbooks.md", Query: "disk", MaxBytes: 700,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(filepath.Join(workDir, "runbooks.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(written) > 700 {
		t.Fatalf("the results file is %d bytes, past max_bytes of 700", len(written))
	}
	if !strings.HasSuffix(string(written), "(cut to fit 700 bytes)\n") {
		t.Errorf("the results file does not end saying it was cut:\n%s", written)
	}
	if !utf8.Valid(written) {
		t.Error("the results file was cut inside a character")
	}
	if !got.Record.BytesTruncated {
		t.Error("the results file was cut and the record does not say so")
	}
	if string(got.Results) != string(written) {
		t.Error("what the stage returns for the record is not the file it wrote")
	}
}
