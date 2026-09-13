package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/bintest"
)

const oneRetrievalPlaybook = `
name: disk-check
trigger:
  type: manual
retrieve:
  - collection: runbooks
    as: runbooks.md
    query: ${trigger.symptom}
agent:
  model: claude-sonnet-5
  prompt_file: prompt.md
  tools: [Read]
  output_schema:
    type: object
sinks:
  - discord:
      webhook: SINK_URL
`

const removedMarker = "REMOVED-MARKER-51c2e0"

var listedBuilt = regexp.MustCompile(`^runbooks\s+lexical\s+directory \S+\s+generation [0-9a-f]{12} built (\S+)\s+(\S.*)$`)

// listed runs `gronin collections list` and returns runbooks' build time and status.
func listed(t *testing.T, state string) (time.Time, string) {
	t.Helper()
	got := bintest.Run(t, "collections", "list", "--state-dir", state)
	if got.ExitCode != 0 {
		t.Fatalf("gronin collections list exited %d: %s", got.ExitCode, got.Stderr)
	}
	for _, line := range strings.Split(got.Stdout, "\n") {
		if match := listedBuilt.FindStringSubmatch(line); match != nil {
			built, err := time.Parse(time.RFC3339, match[1])
			if err != nil {
				t.Fatalf("the listing's build time does not parse: %v", err)
			}
			return built, match[2]
		}
	}
	t.Fatalf("the listing names no indexed runbooks collection:\n%s", got.Stdout)
	return time.Time{}, ""
}

// SC-209. One document added, one changed and one removed between two retrievals: the
// listing says the sources changed, and the second retrieval reflects all three — the
// removed one carries a marker, so an update that only adds fails. Its results equal those
// after a full rebuild, and those after the index is deleted and the next retrieval builds
// it again; the listing then shows a build after the deletion, which an index kept
// anywhere but the state directory survives to fail.
func TestAnUpdateMatchesARebuild(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "runbooks")
	writeDocuments(t, directory, map[string]string{
		"keep.md":   "disk kept steady\n",
		"change.md": "disk before the change\n",
		"remove.md": "disk " + removedMarker + "\n",
	})
	deployment := newRetrievingDeployment(t, directory, oneRetrievalPlaybook)
	symptom := []string{"--trigger", "symptom=disk"}

	_, first := deployment.run(t, "runbooks.md", symptom...)
	if text, _ := first["runbooks.md"].(string); !strings.Contains(text, removedMarker) {
		t.Fatalf("the first retrieval did not find the document about to be removed:\n%q", text)
	}

	writeDocuments(t, directory, map[string]string{
		"added.md":  "disk added later\n",
		"change.md": "disk after the change\n",
	})
	if err := os.Remove(filepath.Join(directory, "remove.md")); err != nil {
		t.Fatal(err)
	}
	if _, status := listed(t, deployment.state); status != "changed since" {
		t.Errorf("the listing says %q after the sources changed", status)
	}

	_, second := deployment.run(t, "runbooks.md", symptom...)
	updated, _ := second["runbooks.md"].(string)
	for _, want := range []string{"disk kept steady", "disk added later", "disk after the change"} {
		if !strings.Contains(updated, want) {
			t.Errorf("the second retrieval does not reflect %q:\n%s", want, updated)
		}
	}
	for _, gone := range []string{removedMarker, "before the change"} {
		if strings.Contains(updated, gone) {
			t.Errorf("the second retrieval still holds %q:\n%s", gone, updated)
		}
	}

	rebuilt := bintest.Run(t, "collections", "rebuild", "runbooks", "--state-dir", deployment.state)
	if rebuilt.ExitCode != 0 {
		t.Fatalf("gronin collections rebuild exited %d: %s", rebuilt.ExitCode, rebuilt.Stderr)
	}
	_, third := deployment.run(t, "runbooks.md", symptom...)
	if third["runbooks.md"] != updated {
		t.Errorf("results after a rebuild differ from the updated index's:\n  updated %q\n  rebuilt %v",
			updated, third["runbooks.md"])
	}

	if err := os.RemoveAll(filepath.Join(deployment.state, "index")); err != nil {
		t.Fatal(err)
	}
	deleted := time.Now()
	// The listing states build times to the second. Waiting for the clock to leave the
	// deletion's second is what lets "built after the deletion" be read from it at all.
	for deadline := deleted.Add(5 * time.Second); !time.Now().Truncate(time.Second).After(deleted.Truncate(time.Second)); {
		if time.Now().After(deadline) {
			t.Fatal("the clock did not advance past the deletion's second")
		}
		time.Sleep(20 * time.Millisecond)
	}

	_, fourth := deployment.run(t, "runbooks.md", symptom...)
	if fourth["runbooks.md"] != updated {
		t.Errorf("results after the index was deleted differ from the updated index's:\n  updated %q\n  rebuilt %v",
			updated, fourth["runbooks.md"])
	}
	built, status := listed(t, deployment.state)
	if !built.After(deleted.Truncate(time.Second)) {
		t.Errorf("the listing shows a generation built at %s, not after the index was deleted at %s",
			built.Format(time.RFC3339), deleted.UTC().Format(time.RFC3339))
	}
	if status != "unchanged" {
		t.Errorf("the listing says %q straight after a retrieval", status)
	}
}
