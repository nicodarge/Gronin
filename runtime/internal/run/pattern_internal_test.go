package run

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The pattern Begin checks a name against before it becomes a path is a copy of the
// contract's. Nothing but this keeps the copy honest.
func TestTheLockNamePatternMatchesTheContract(t *testing.T) {
	document, err := os.ReadFile(filepath.Join(
		"..", "..", "..", "specs", "001-runtime-core", "contracts", "playbook.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties struct {
			Name struct {
				Pattern string `json:"pattern"`
			} `json:"name"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(document, &schema); err != nil {
		t.Fatal(err)
	}
	if got := playbookNamePattern.String(); got != schema.Properties.Name.Pattern {
		t.Fatalf("Begin checks %q, the contract says %q", got, schema.Properties.Name.Pattern)
	}
}
