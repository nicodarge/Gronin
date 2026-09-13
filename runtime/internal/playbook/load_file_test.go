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

// directoryWith lays book.yaml, its prompt and the given siblings down, and returns book.yaml.
func directoryWith(t *testing.T, siblings map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	book := filepath.Join(dir, "book.yaml")
	files := map[string]string{book: loadable, filepath.Join(dir, "p.md"): "report"}
	for name, body := range siblings {
		files[filepath.Join(dir, name)] = body
	}
	for path, body := range files {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return book
}

// FR-042 through the single-file load a waiting trigger reads its playbook with. Load
// refuses the whole directory when two files declare one name, so a file read alone has to
// be refused too once another file has taken its name — whichever of the two sorts first.
func TestLoadFileRefusesANameAnotherFileDeclares(t *testing.T) {
	if _, err := playbook.LoadFile(directoryWith(t, nil), deployment()); err != nil {
		t.Fatalf("the playbook alone was refused: %v", err)
	}
	for _, other := range []string{"a-copy.yaml", "zz-copy.yaml"} {
		t.Run(other, func(t *testing.T) {
			_, err := playbook.LoadFile(directoryWith(t, map[string]string{other: loadable}), deployment())
			if err == nil || !strings.Contains(err.Error(), other) {
				t.Fatalf("a name %s also declares was not refused naming it: %v", other, err)
			}
		})
	}
}

// Load refuses the directory when any playbook in it is refused, whatever its name, and a file
// read alone is refused with it: a waiting trigger must never run what a start would refuse.
func TestLoadFileRefusesWhatItsDirectoryRefuses(t *testing.T) {
	shell := strings.Replace(strings.Replace(loadable, "drift-check", "other-check", 1),
		"tools: [Read]", "tools: [Bash]", 1)
	for sibling, body := range map[string]string{
		"broken.yaml": "name: [\n",
		"shell.yaml":  shell,
	} {
		t.Run(sibling, func(t *testing.T) {
			_, err := playbook.LoadFile(directoryWith(t, map[string]string{sibling: body}), deployment())
			if err == nil || !strings.Contains(err.Error(), sibling) {
				t.Fatalf("a directory whose %s is refused let the file be read alone: %v", sibling, err)
			}
		})
	}
}
