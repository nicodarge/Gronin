package retrieve_test

import (
	"strings"
	"testing"
	"unicode"

	"github.com/nicodarge/Gronin/runtime/internal/playbook"
)

// Only a passage's own text may carry a character that moves a terminal or breaks a line.
// An escape sequence in a query sent by a webhook, or a line separator in a file's name,
// reaches the agent's file and an operator's terminal as its escape, not as itself.
func TestTheResultsFileHoldsNoControlCharacterFromTheQueryOrASource(t *testing.T) {
	stage, workDir := stageOver(t, map[string]string{
		"line\u2028separated.md": "journal rotation advice\n",
	})

	got, err := retrieveOne(t, stage, workDir, playbook.Retrieval{
		Collection: "runbooks", As: "runbooks.md", Query: "${trigger.symptom}",
	}, map[string]string{"symptom": "journal \x1b[2Jcleared\x07"})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range string(got.Results) {
		if r != '\n' && (unicode.IsControl(r) || unicode.In(r, unicode.Zl, unicode.Zp)) {
			t.Errorf("the results file holds %U as itself:\n%q", r, got.Results)
		}
	}
	for _, escaped := range []string{`\x1b[2Jcleared\a`, `line\u2028separated.md`} {
		if !strings.Contains(string(got.Results), escaped) {
			t.Errorf("the results file does not hold %s:\n%s", escaped, got.Results)
		}
	}
}
