package retrieve_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/collections"
	"github.com/nicodarge/Gronin/runtime/internal/config"
	"github.com/nicodarge/Gronin/runtime/internal/playbook"
	"github.com/nicodarge/Gronin/runtime/internal/record"
	"github.com/nicodarge/Gronin/runtime/internal/stage/retrieve"
)

// stageOver is a stage whose deployment declares one lexical collection, runbooks, over a
// directory holding docs, and whose configuration holds mountpoint. It returns the stage
// and a working directory for a run.
func stageOver(t *testing.T, docs map[string]string) (*retrieve.Stage, string) {
	t.Helper()
	root := t.TempDir()
	state := filepath.Join(root, "state")
	directory := filepath.Join(root, "runbooks")
	for _, dir := range []string{state, directory} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range docs {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	catalogue, err := json.Marshal(map[string]any{"runbooks": map[string]any{"directory": directory}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "collections.json"), catalogue, 0o600); err != nil {
		t.Fatal(err)
	}
	declared, err := collections.Load(state)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Set("mountpoint", config.Value{Value: "/var"}); err != nil {
		t.Fatal(err)
	}
	return &retrieve.Stage{
		Catalog:  declared,
		IndexDir: filepath.Join(state, "index"),
		Config:   cfg,
		Redactor: record.NewRedactor(cfg.Secrets()),
	}, t.TempDir()
}

func retrieveOne(
	t *testing.T, stage *retrieve.Stage, workDir string, declared playbook.Retrieval,
	trigger map[string]string,
) (retrieve.Retrieved, error) {
	t.Helper()
	retrieved, err := stage.Run(t.Context(), workDir, []playbook.Retrieval{declared}, trigger)
	if len(retrieved) != 1 {
		t.Fatalf("one retrieval declared, %d returned (err %v)", len(retrieved), err)
	}
	return retrieved[0], err
}

var runbooks = map[string]string{
	"disk-full.md":   "# Disk full\n\nRotate the journal when /var fills.\n",
	"cert-expiry.md": "# Certificate expired\n\nRenew the certificate and reload the proxy.\n",
	"swap.md":        "# Swap exhausted\n\nFind the process holding memory.\n",
}

// SC-201: a query resolves against the trigger and the configuration, the two sources
// Principle IV allows, and against nothing else.
func TestTheQueryResolvesOnlyTheTriggerAndTheConfiguration(t *testing.T) {
	stage, workDir := stageOver(t, runbooks)

	got, err := retrieveOne(t, stage, workDir, playbook.Retrieval{
		Collection: "runbooks", As: "runbooks.md",
		Query: "journal on ${trigger.host} under ${config.mountpoint}",
	}, map[string]string{"host": "db1"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Record.Query != "journal on db1 under /var" {
		t.Errorf("the query was searched as %q", got.Record.Query)
	}

	t.Setenv("GRONIN_QUERY_PROBE", "from-the-environment")
	for _, query := range []string{"${env.GRONIN_QUERY_PROBE}", "${GRONIN_QUERY_PROBE}"} {
		refused, err := retrieveOne(t, stage, workDir, playbook.Retrieval{
			Collection: "runbooks", As: "runbooks.md", Query: query,
		}, nil)
		if err == nil {
			t.Errorf("%s resolved, and searched as %q", query, refused.Record.Query)
			continue
		}
		if refused.Record.Outcome != record.RetrievalRefused {
			t.Errorf("%s: outcome %q, want refused", query, refused.Record.Outcome)
		}
		if strings.Contains(refused.Record.Query+err.Error(), "from-the-environment") {
			t.Errorf("%s reached the process environment: %v", query, err)
		}
	}
}

// FR-228: a query holding no word to search is refused before the collection is walked or
// its index touched. Walked first, a blank query over a missing directory is refused for the
// directory, and one over a present directory creates and fills an index for nothing. Cut to
// the query bound, the refusal says so, since what was refused is not what was declared.
func TestTheQueryHoldingNoWordIsRefusedBeforeTheIndexIsTouched(t *testing.T) {
	stage, workDir := stageOver(t, runbooks)
	collection, _ := stage.Catalog.Get("runbooks")
	if err := os.RemoveAll(collection.Directory); err != nil {
		t.Fatal(err)
	}

	_, err := retrieveOne(t, stage, workDir, playbook.Retrieval{
		Collection: "runbooks", As: "runbooks.md", Query: "${trigger.symptom}",
	}, map[string]string{"symptom": "  \t "})
	switch {
	case err == nil:
		t.Fatal("a blank query was not refused")
	case !strings.Contains(err.Error(), "empty"):
		t.Errorf("a blank query was refused for another cause: %v", err)
	case strings.Contains(err.Error(), collection.Directory):
		t.Errorf("a blank query was refused for the directory rather than for itself: %v", err)
	}
	if _, err := os.Stat(stage.IndexDir); !os.IsNotExist(err) {
		t.Errorf("refusing a blank query touched the index directory %s", stage.IndexDir)
	}

	_, err = retrieveOne(t, stage, workDir, playbook.Retrieval{
		Collection: "runbooks", As: "runbooks.md", Query: strings.Repeat("*", 1100) + " journal",
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "after it was cut to 1,024 bytes") {
		t.Errorf("a query left with no word by the cut is not refused saying so: %v", err)
	}
}

// SC-201: query_from reads the named gathered file, whole, as the query.
func TestTheQueryFromAGatheredFileIsReadWhole(t *testing.T) {
	stage, workDir := stageOver(t, runbooks)
	const gathered = "certificate expired\non the proxy\n\nsince tuesday"
	if err := os.WriteFile(filepath.Join(workDir, "facts.txt"), []byte(gathered), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := retrieveOne(t, stage, workDir, playbook.Retrieval{
		Collection: "runbooks", As: "runbooks.md", QueryFrom: "facts.txt",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Record.Query != gathered {
		t.Errorf("the query was searched as %q, want the gathered file whole", got.Record.Query)
	}
}

// SC-201: with query_from set, nothing in the trigger is read. A trigger value named like
// the gathered file, or like a query, never becomes one — a webhook's sender writes it.
func TestTheQueryFromAGatheredFileIgnoresTheTrigger(t *testing.T) {
	stage, workDir := stageOver(t, runbooks)
	if err := os.WriteFile(filepath.Join(workDir, "facts.txt"), []byte("swap exhausted"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := retrieveOne(t, stage, workDir, playbook.Retrieval{
		Collection: "runbooks", As: "runbooks.md", QueryFrom: "facts.txt",
	}, map[string]string{"facts.txt": "certificate expired", "query": "certificate expired"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Record.Query != "swap exhausted" {
		t.Errorf("the query was searched as %q, want the gathered file's content", got.Record.Query)
	}
	if strings.Contains(string(got.Results), "Renew the certificate") {
		t.Errorf("the results hold what the trigger's value would find:\n%s", got.Results)
	}
}
