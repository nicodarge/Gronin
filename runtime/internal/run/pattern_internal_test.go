package run

import (
	"encoding/json"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/playbook"
)

// The pattern Begin checks a name against before it becomes a path is a copy of the
// contract's. This asserts it against the embedded schema rather than the contract file:
// the contract is not beside this module when the mutation harness copies it, and
// playbook.TestTheEmbeddedSchemaIsTheContract already holds the embedded copy to it.
func TestTheLockNamePatternMatchesTheContract(t *testing.T) {
	var schema struct {
		Properties struct {
			Name struct {
				Pattern string `json:"pattern"`
			} `json:"name"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(playbook.Schema, &schema); err != nil {
		t.Fatal(err)
	}
	if got := playbookNamePattern.String(); got != schema.Properties.Name.Pattern {
		t.Fatalf("Begin checks %q, the contract says %q", got, schema.Properties.Name.Pattern)
	}
}
