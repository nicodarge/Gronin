package main

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/bintest"
	"github.com/nicodarge/Gronin/runtime/internal/fakeagent"
)

// deploymentWithACollection writes a state directory holding a catalogue and a playbooks
// directory holding one playbook, and returns both paths.
func deploymentWithACollection(t *testing.T, catalogue, book string) (state, playbooks string) {
	t.Helper()
	state = t.TempDir()
	if catalogue != "" {
		if err := os.WriteFile(filepath.Join(state, "collections.json"),
			[]byte(catalogue), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	playbooks = filepath.Join(state, "playbooks")
	if err := os.MkdirAll(playbooks, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(playbooks, "prompt.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if book != "" {
		if err := os.WriteFile(filepath.Join(playbooks, "disk-check.yaml"),
			[]byte(book), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return state, playbooks
}

// A playbook retrieving from a collection its deployment does not declare fails at the
// first trigger and nowhere earlier, unless the gate reads the catalogue. SC-203, through
// the executable, where the catalogue is read once per invocation and handed to the gate.
func TestValidateRefusesAnUndeclaredCollection(t *testing.T) {
	const book = `name: disk-check
trigger: {type: manual}
retrieve:
  - collection: incidents
    as: incidents.md
    query: disk full on /var
agent: {model: m, prompt_file: prompt.md, output_schema: {type: object}}
sinks: [{discord: {channel: "#ops"}}]
`
	state, playbooks := deploymentWithACollection(t,
		`{"runbooks": {"directory": "/srv/runbooks"}}`, book)

	got := bintest.Run(t, "--state-dir", state, "validate", playbooks)

	if got.ExitCode == 0 {
		t.Fatalf("a retrieval from an undeclared collection was accepted; stdout = %q", got.Stdout)
	}
	output := got.Stdout + got.Stderr
	if !strings.Contains(output, "retrieve[0].collection") {
		t.Errorf("the refusal does not name the field: %q", output)
	}
	if !strings.Contains(output, "runbooks") {
		t.Errorf("the refusal does not say what this deployment does declare: %q", output)
	}
}

// outsideMarker is the content of a file outside the collection, reachable only through
// a symbolic link inside it.
const outsideMarker = "OUTSIDE-MARKER-9a03b7"

// SC-207, the operator's surface. The listing names every document and every skipped
// entry with its reason, in contracts/cli.md's lines. Then a run retrieves with a query made
// of the outside file's other words, and the marker is in no result, no row and no blob:
// the query never holds the marker, or the record would hold it for an innocent reason.
func TestAListingNamesEverySkippedFile(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	directory := filepath.Join(root, "collection")
	writeDocuments(t, outside, map[string]string{
		"elsewhere.md": "certificate rotation " + outsideMarker + "\n",
	})
	writeDocuments(t, directory, map[string]string{
		"notes.md":   "disk full on /var\n",
		"binary.dat": "disk\x00full",
		"large.txt":  strings.Repeat("a", 1<<20+1),
	})
	writeDocuments(t, filepath.Join(directory, "sub"), map[string]string{"deeper.md": "swap exhausted\n"})
	if err := os.Symlink(filepath.Join(outside, "elsewhere.md"), filepath.Join(directory, "outside-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..", filepath.Join(directory, "parent-link")); err != nil {
		t.Fatal(err)
	}
	deployment := newRetrievingDeployment(t, directory, oneRetrievalPlaybook)

	shown := bintest.Run(t, "collections", "show", "runbooks", "--state-dir", deployment.state)
	if shown.ExitCode != 0 {
		t.Fatalf("gronin collections show exited %d: %s", shown.ExitCode, shown.Stderr)
	}
	want := strings.Join([]string{
		"collection  runbooks",
		"mode        lexical",
		"source      directory " + directory,
		"generation  not indexed yet",
		"changed     not indexed yet",
		"",
		"document    notes.md                18 bytes  not indexed",
		"document    sub/deeper.md           15 bytes  not indexed",
		"skipped     binary.dat            not text",
		"skipped     large.txt             larger than 1 MiB",
		"skipped     outside-link          symbolic link, not followed",
		"skipped     parent-link           symbolic link, not followed",
		"",
	}, "\n")
	if shown.Stdout != want {
		t.Errorf("gronin collections show printed:\n%s\nwant:\n%s", shown.Stdout, want)
	}

	_, quoted := deployment.run(t, "runbooks.md", "--trigger", "symptom=certificate rotation")
	if text, _ := quoted["runbooks.md"].(string); strings.Contains(text, outsideMarker) {
		t.Errorf("the results file holds the outside file's content:\n%s", text)
	}
	err := filepath.WalkDir(deployment.state, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), outsideMarker) {
			t.Errorf("%s holds the outside file's content", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// digestCheckPlaybook records reports a reports collection can index.
const digestCheckPlaybook = `
name: digest-check
trigger:
  type: manual
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

// T055. `collections show` over a reports collection names each indexed run by its
// identifier, and says its sources changed once a further run has recorded a report —
// which the operator's listing can only say once the collection has been indexed at
// least once.
func TestAReportsCollectionListsItsRuns(t *testing.T) {
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(sink.Close)

	state := t.TempDir()
	playbooks := filepath.Join(state, "playbooks")
	if err := os.MkdirAll(playbooks, 0o700); err != nil {
		t.Fatal(err)
	}
	catalogue, err := json.Marshal(map[string]any{
		"conclusions": map[string]any{"reports": []string{"digest-check"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		filepath.Join(state, "collections.json"):      string(catalogue),
		filepath.Join(playbooks, "digest-check.yaml"): strings.ReplaceAll(digestCheckPlaybook, "SINK_URL", sink.URL),
		filepath.Join(playbooks, "prompt.md"):         "say something",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	runOnce := func(result string) string {
		t.Helper()
		agent := fakeagent.Wrapped(t, fakeagent.ResultVar+"="+result)
		got := bintest.Run(t, "run", "digest-check", "--state-dir", state, "--agent", agent)
		if got.ExitCode != 0 {
			t.Fatalf("gronin run exited %d:\n%s\n%s", got.ExitCode, got.Stdout, got.Stderr)
		}
		fields := strings.Fields(got.Stdout)
		if len(fields) == 0 {
			t.Fatalf("gronin run printed no run identifier: %q", got.Stderr)
		}
		return fields[0]
	}

	first := runOnce(`{"summary":"first conclusion"}`)

	if built := bintest.Run(t, "collections", "rebuild", "conclusions", "--state-dir", state); built.ExitCode != 0 {
		t.Fatalf("gronin collections rebuild exited %d: %s", built.ExitCode, built.Stderr)
	}

	shown := bintest.Run(t, "collections", "show", "conclusions", "--state-dir", state)
	if shown.ExitCode != 0 {
		t.Fatalf("gronin collections show exited %d: %s", shown.ExitCode, shown.Stderr)
	}
	if !strings.Contains(shown.Stdout, "document    "+first) {
		t.Errorf("the listing does not name the run by its identifier:\n%s", shown.Stdout)
	}
	if !strings.Contains(shown.Stdout, "changed     no") {
		t.Errorf("a freshly rebuilt collection reads as changed:\n%s", shown.Stdout)
	}

	runOnce(`{"summary":"second conclusion"}`)

	again := bintest.Run(t, "collections", "show", "conclusions", "--state-dir", state)
	if again.ExitCode != 0 {
		t.Fatalf("gronin collections show exited %d: %s", again.ExitCode, again.Stderr)
	}
	if !strings.Contains(again.Stdout, "changed     yes") {
		t.Errorf("the listing does not say the sources changed after a further run:\n%s", again.Stdout)
	}
}

// The rule openResolvableCatalog states for the MCP catalogue: a malformed catalogue
// refuses every command that loads playbooks, and none that only reads run history —
// which is what is most wanted right after something broke.
func TestAMalformedCollectionsFileRefusesTheDeployment(t *testing.T) {
	state, playbooks := deploymentWithACollection(t,
		`{"runbooks": {"directory": "/srv/runbooks", "tokenizer": "porter"}}`, "")

	refused := bintest.Run(t, "--state-dir", state, "validate", playbooks)
	if refused.ExitCode == 0 {
		t.Fatalf("a catalogue with an unknown key was accepted; stdout = %q", refused.Stdout)
	}
	if !strings.Contains(refused.Stderr, "tokenizer") {
		t.Errorf("the refusal does not name the key: %q", refused.Stderr)
	}

	history := bintest.Run(t, "--state-dir", state, "runs")
	if history.ExitCode != 0 {
		t.Fatalf("reading run history was refused for a catalogue it never needed: %q %q",
			history.Stdout, history.Stderr)
	}
}
