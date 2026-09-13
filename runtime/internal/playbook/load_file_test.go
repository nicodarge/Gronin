package playbook_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/playbook"
)

const loadable = `name: drift-check
trigger: {type: manual}
agent: {model: m, prompt_file: p.md, tools: [Read], output_schema: {type: object}}
sinks: [{discord: {channel: "#ops"}}]
`

// FR-042 through the single-file load a waiting trigger reads its playbook with. Load
// refuses the whole directory when two files declare one name, so a file read alone has to
// be refused too once another file has taken its name — whichever of the two sorts first.
func TestLoadFileRefusesANameAnotherFileDeclares(t *testing.T) {
	for _, other := range []string{"a-copy.yaml", "zz-copy.yaml"} {
		t.Run(other, func(t *testing.T) {
			dir := t.TempDir()
			book := filepath.Join(dir, "book.yaml")
			for path, body := range map[string]string{book: loadable, filepath.Join(dir, "p.md"): "report"} {
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := playbook.LoadFile(book, deployment()); err != nil {
				t.Fatalf("the playbook alone was refused: %v", err)
			}

			if err := os.WriteFile(filepath.Join(dir, other), []byte(loadable), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := playbook.LoadFile(book, deployment())
			if err == nil || !strings.Contains(err.Error(), other) {
				t.Fatalf("a name %s also declares was not refused naming it: %v", other, err)
			}
		})
	}
}
