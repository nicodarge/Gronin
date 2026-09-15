package collections_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/collections"
)

// write puts a catalogue in a state directory of its own and returns it.
func write(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "collections.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A deployment with no catalogue is a deployment with no collection, which is what every
// deployment is before someone declares one.
func TestCollectionsAbsentIsAnEmptyCatalogue(t *testing.T) {
	catalog, err := collections.Load(t.TempDir())
	if err != nil {
		t.Fatalf("an absent catalogue was an error: %v", err)
	}
	if names := catalog.Names(); len(names) != 0 {
		t.Fatalf("an absent catalogue holds %v", names)
	}
}

// The accepting half. A gate probed only on its refusals is one whose false positives
// are what get it switched off rather than fixed.
func TestCollectionsAcceptsADirectoryWithItsDefaults(t *testing.T) {
	dir := write(t, `{
	  "runbooks": {"directory": "/srv/runbooks"},
	  "slower": {"directory": "/srv/slow", "retrieval_timeout": "5m"}
	}`)

	catalog, err := collections.Load(dir)
	if err != nil {
		t.Fatalf("a well-formed catalogue was refused: %v", err)
	}
	if names := catalog.Names(); len(names) != 2 || names[0] != "runbooks" || names[1] != "slower" {
		t.Fatalf("the catalogue holds %v", names)
	}

	runbooks, found := catalog.Get("runbooks")
	if !found {
		t.Fatal("a declared collection cannot be found by name")
	}
	if runbooks.Mode() != collections.Lexical {
		t.Errorf("a collection with no embeddings block is %q", runbooks.Mode())
	}
	if runbooks.Source() != "directory /srv/runbooks" {
		t.Errorf("its source reads %q", runbooks.Source())
	}
	if runbooks.RetrievalTimeout != collections.DefaultRetrievalTimeout {
		t.Errorf("a collection declaring no bound got %v", runbooks.RetrievalTimeout)
	}

	slower, _ := catalog.Get("slower")
	if slower.RetrievalTimeout != 5*time.Minute {
		t.Errorf("a declared bound read back as %v", slower.RetrievalTimeout)
	}
	if _, found := catalog.Get("nowhere"); found {
		t.Error("a collection nothing declares was found")
	}
}

// A collection over reports is the other source (FR-212, FR-214): still lexical, since
// mode is derived from an embeddings block, not from the source.
func TestCollectionsAcceptsReportsWithItsDefaults(t *testing.T) {
	dir := write(t, `{"disk-history": {"reports": ["disk-space", "disk-check"]}}`)

	catalog, err := collections.Load(dir)
	if err != nil {
		t.Fatalf("a well-formed reports collection was refused: %v", err)
	}
	history, found := catalog.Get("disk-history")
	if !found {
		t.Fatal("a declared reports collection cannot be found by name")
	}
	if history.Mode() != collections.Lexical {
		t.Errorf("a reports collection with no embeddings block is %q", history.Mode())
	}
	if history.Source() != "reports disk-space, disk-check" {
		t.Errorf("its source reads %q", history.Source())
	}
	if len(history.Reports) != 2 || history.Reports[0] != "disk-space" || history.Reports[1] != "disk-check" {
		t.Errorf("its reports read back as %v", history.Reports)
	}
}

// Each refusal is pinned to what it is about. Asserting only that a catalogue was refused
// would let one drift into being refused for an unrelated reason while the table stayed
// green.
//
// The last row is the one source whose mechanism has not landed yet. It is refused rather
// than accepted and left unsearchable, and lifting it is one line of this table.
func TestCollectionsRefusesEachForItsOwnReason(t *testing.T) {
	for name, probe := range map[string]struct{ body, says string }{
		"an unknown key": {
			`{"runbooks": {"directory": "/srv/runbooks", "tokenizer": "porter"}}`,
			`unknown field "tokenizer"`,
		},
		"both sources": {
			`{"runbooks": {"directory": "/srv/runbooks", "reports": ["disk-space"]}}`,
			"directory is given with reports",
		},
		"neither source": {
			`{"runbooks": {}}`,
			"names neither a directory nor reports",
		},
		"a relative directory": {
			`{"runbooks": {"directory": "runbooks"}}`,
			`directory "runbooks" is relative`,
		},
		"a name that is not a slug": {
			`{"Run Books": {"directory": "/srv/runbooks"}}`,
			"the name is not a lowercase slug",
		},
		"a duration that does not parse": {
			`{"runbooks": {"directory": "/srv/runbooks", "retrieval_timeout": "soon"}}`,
			`retrieval_timeout "soon" is not a duration`,
		},
		"a duration of nothing": {
			`{"runbooks": {"directory": "/srv/runbooks", "retrieval_timeout": "0s"}}`,
			`retrieval_timeout "0s" is not positive`,
		},
		"an empty reports list": {
			`{"disk-history": {"reports": []}}`,
			"reports is empty",
		},
		"a report name that is not a slug": {
			`{"disk-history": {"reports": ["Disk Space"]}}`,
			`reports[0] "Disk Space" is not the shape of a playbook name`,
		},
		"embeddings, which nothing searches yet": {
			`{"by-meaning": {"directory": "/srv/runbooks", "embeddings": {
			    "url": "https://embeddings.example.com/v1/embeddings", "model": "REPLACE_ME"}}}`,
			"embeddings is declared, and this runtime does not search by meaning yet",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := collections.Load(write(t, probe.body))
			if err == nil {
				t.Fatalf("%s was accepted", name)
			}
			if !strings.Contains(err.Error(), probe.says) {
				t.Fatalf("refused, but not for its reason.\n  want: %s\n  got:  %v", probe.says, err)
			}
		})
	}
}

// A catalogue refused one fault at a time costs an operator one round trip per mistake.
func TestCollectionsReportsEveryRefusalAtOnce(t *testing.T) {
	_, err := collections.Load(write(t, `{
	  "runbooks": {"directory": "runbooks", "retrieval_timeout": "soon"},
	  "Other": {"directory": "/srv/other"}
	}`))
	if err == nil {
		t.Fatal("a catalogue with three faults was accepted")
	}
	for _, want := range []string{"is relative", "is not a duration", "is not a lowercase slug"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q was not reported; the catalogue stopped short:\n%v", want, err)
		}
	}
}
