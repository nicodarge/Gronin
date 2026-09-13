package main

import (
	"path/filepath"
	"regexp"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/bintest"
)

var shownGeneration = regexp.MustCompile(`from runbooks \(lexical\) generation ([0-9a-f]{12}) built (\S+):`)

// SC-214. From the record alone, through the operator's surface, the results file the
// agent read comes back byte for byte and names its generation. After the source changes
// and the index is rebuilt, the earlier run still shows what it retrieved then: a record
// holding a pointer into the index would show what the index says now.
func TestTheRecordHoldsWhatWasRetrieved(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "runbooks")
	writeDocuments(t, directory, threeRunbooks)
	deployment := newRetrievingDeployment(t, directory, oneRetrievalPlaybook)

	id, quoted := deployment.run(t, "runbooks.md", "--trigger", "symptom=journal")
	read, ok := quoted["runbooks.md"].(string)
	if !ok || read == "" {
		t.Fatalf("the agent read no results file: %v", quoted)
	}

	shownFile := func() string {
		t.Helper()
		got := bintest.Run(t, "show", id, "--retrieval", "runbooks.md", "--state-dir", deployment.state)
		if got.ExitCode != 0 {
			t.Fatalf("gronin show --retrieval exited %d: %s", got.ExitCode, got.Stderr)
		}
		return got.Stdout
	}
	shownGenerationLine := func() []string {
		t.Helper()
		got := bintest.Run(t, "show", id, "--state-dir", deployment.state)
		match := shownGeneration.FindStringSubmatch(got.Stdout)
		if match == nil {
			t.Fatalf("gronin show names no generation for the retrieval:\n%s", got.Stdout)
		}
		return match[1:]
	}

	if got := shownFile(); got != read {
		t.Fatalf("the recorded results file is not what the agent read:\n  agent  %q\n  record %q", read, got)
	}
	generation := shownGenerationLine()

	writeDocuments(t, directory, map[string]string{
		"disk-full.md": "# Disk full\n\nThe journal is rotated nightly now; look at the backups instead.\n",
	})
	rebuilt := bintest.Run(t, "collections", "rebuild", "runbooks", "--state-dir", deployment.state)
	if rebuilt.ExitCode != 0 {
		t.Fatalf("gronin collections rebuild exited %d: %s", rebuilt.ExitCode, rebuilt.Stderr)
	}
	listing := bintest.Run(t, "collections", "list", "--state-dir", deployment.state)
	if regexp.MustCompile(`generation ` + generation[0] + ` `).MatchString(listing.Stdout) {
		t.Fatalf("the rebuild left the generation the run searched, so nothing moved:\n%s", listing.Stdout)
	}

	if got := shownFile(); got != read {
		t.Errorf("after a rebuild the record shows another results file:\n  then %q\n  now  %q", read, got)
	}
	if got := shownGenerationLine(); got[0] != generation[0] || got[1] != generation[1] {
		t.Errorf("after a rebuild the record names generation %v, want %v", got, generation)
	}

	missing := bintest.Run(t, "show", id, "--retrieval", "incidents.md", "--state-dir", deployment.state)
	if missing.ExitCode == 0 {
		t.Errorf("gronin show --retrieval exited 0 for a name the run did not retrieve: %q", missing.Stdout)
	}
}
