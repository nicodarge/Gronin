package collections

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Mode is how a collection is searched. It is derived and never declared: a key that
// could say `mode: lexical` beside an `embeddings` block would be a second statement of
// one fact, and the two could disagree.
type Mode string

// The two modes a collection can have.
const (
	Lexical  Mode = "lexical"
	Semantic Mode = "semantic"
)

// The bounds a collection gets when it declares none. Both belong to the deployment
// because they depend on how fast its endpoint answers, which a portable playbook cannot
// know.
const (
	DefaultRetrievalTimeout = 2 * time.Minute
	DefaultRequestTimeout   = 60 * time.Second
)

// Collection is one entry of the catalogue, as the runtime reads it: its defaults filled
// in, its source one of two, and its mode derived.
type Collection struct {
	Name string
	// Directory is an absolute path on this host. Exactly one of Directory and Reports.
	Directory string
	// Reports names the playbooks whose recorded reports are this collection's documents.
	Reports    []string
	Embeddings *Embeddings
	// RetrievalTimeout bounds everything one retrieval does: walking and digesting the
	// sources, embedding what changed, and searching.
	RetrievalTimeout time.Duration
}

// Embeddings is where a semantic collection's text is sent. Its presence is what makes
// the collection semantic.
type Embeddings struct {
	URL   string
	Model string
	// Credential is a ${config.x} reference to a value marked secret, never a literal.
	// Empty when the endpoint asks for none, which a server on the same host may not.
	Credential     string
	RequestTimeout time.Duration
}

// Mode is `semantic` when the deployment gave this collection an embeddings endpoint.
func (c Collection) Mode() Mode {
	if c.Embeddings != nil {
		return Semantic
	}
	return Lexical
}

// Source is what this collection is built from, as an operator's listing names it.
func (c Collection) Source() string {
	if c.Directory != "" {
		return "directory " + c.Directory
	}
	return "reports " + strings.Join(c.Reports, ", ")
}

// Catalog is what this deployment declares.
type Catalog struct {
	entries map[string]Collection
}

const catalogFile = "collections.json"

// Load reads the catalogue from the deployment's state directory, the way the MCP
// catalogue beside it is read. Absent is not an error: it is a deployment with no
// collection, and a playbook naming one is then refused at load.
//
// Every refusal is reported, not the first. Fixing a catalogue one round trip at a time
// is how an operator comes to distrust the message.
func Load(stateDir string) (*Catalog, error) {
	// The path is this deployment's own state directory and a fixed name; there is no
	// caller-supplied component in it.
	data, err := os.ReadFile(filepath.Join(stateDir, catalogFile)) //nolint:gosec
	if errors.Is(err, os.ErrNotExist) {
		return &Catalog{entries: map[string]Collection{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the collection catalogue: %w", err)
	}

	raw := map[string]entry{}
	decoder := json.NewDecoder(bytes.NewReader(data))
	// An unknown key is refused like an unknown playbook key: a misspelled one that is
	// ignored reads, in review, as a setting that applies.
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return nil, fmt.Errorf("parsing the collection catalogue: %w", err)
	}

	names := make([]string, 0, len(raw))
	for name := range raw {
		names = append(names, name)
	}
	sort.Strings(names)

	var problems []error
	entries := make(map[string]Collection, len(raw))
	for _, name := range names {
		collection, found := raw[name].resolve(name)
		problems = append(problems, found...)
		entries[name] = collection
	}
	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}
	return &Catalog{entries: entries}, nil
}

// Names is every collection this deployment declares, ordered. This is what the load gate
// (playbook.Deployment.Collections) checks a playbook's retrievals against.
func (c *Catalog) Names() []string {
	names := make([]string, 0, len(c.entries))
	for name := range c.entries {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Get is one collection by name.
func (c *Catalog) Get(name string) (Collection, bool) {
	collection, ok := c.entries[name]
	return collection, ok
}

// entry is the catalogue as it is written, before defaults and before refusals. The
// durations are strings here because "not a duration" is a refusal this reports rather
// than a decoding error that names no collection.
type entry struct {
	Directory        string          `json:"directory"`
	Reports          []string        `json:"reports"`
	Embeddings       *embeddingsJSON `json:"embeddings"`
	RetrievalTimeout string          `json:"retrieval_timeout"`
}

type embeddingsJSON struct {
	URL            string `json:"url"`
	Model          string `json:"model"`
	Credential     string `json:"credential"`
	RequestTimeout string `json:"request_timeout"`
}

// slug is what a collection may be called: the shape of a playbook name, because the name
// is also the index file's.
var slug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// configReference is the only form a credential may take.
var configReference = regexp.MustCompile(`^\$\{config\.[^{}.]+\}$`)

// appliedSources are the sources a retrieval can actually search. One whose mechanism has
// not landed is refused here, by name, because a declared collection nothing can search
// is a source that reads, in review, as available. A source is lifted by adding it.
var appliedSources = map[string]bool{"directory": true, "reports": true}

func (e entry) resolve(name string) (Collection, []error) {
	var problems []error
	refuse := func(field, found, accepted string) {
		problems = append(problems, fmt.Errorf(
			"collection %q: %s %s; accepted: %s", name, field, found, accepted))
	}

	if !slug.MatchString(name) {
		refuse("the name", "is not a lowercase slug",
			"letters, digits and hyphens, as a playbook name is")
	}

	switch {
	case e.Directory != "" && len(e.Reports) > 0:
		refuse("directory", "is given with reports", "exactly one source")
	case e.Directory == "" && e.Reports == nil:
		refuse("the entry", "names neither a directory nor reports",
			"exactly one source: a directory, or the reports of named playbooks")
	case e.Reports != nil && len(e.Reports) == 0:
		refuse("reports", "is empty", "the reports of at least one playbook this deployment records")
	case e.Directory != "" && !filepath.IsAbs(e.Directory):
		refuse("directory", fmt.Sprintf("%q is relative", e.Directory),
			"an absolute path; it is resolved on the host the runtime runs on")
	}

	if len(e.Reports) > 0 {
		if !appliedSources["reports"] {
			refuse("reports", "is declared, and this runtime does not search it yet",
				"a directory; a collection nothing can search reads as available")
		}
		for at, playbookName := range e.Reports {
			if !slug.MatchString(playbookName) {
				refuse(fmt.Sprintf("reports[%d]", at),
					fmt.Sprintf("%q is not the shape of a playbook name", playbookName),
					"letters, digits and hyphens")
			}
		}
	}

	collection := Collection{
		Name:             name,
		Directory:        e.Directory,
		Reports:          e.Reports,
		RetrievalTimeout: DefaultRetrievalTimeout,
	}
	if declared, found := duration(e.RetrievalTimeout, "retrieval_timeout", refuse); found {
		collection.RetrievalTimeout = declared
	}
	if e.Embeddings != nil {
		collection.Embeddings = e.Embeddings.resolve(refuse)
	}
	return collection, problems
}

func (e embeddingsJSON) resolve(refuse func(field, found, accepted string)) *Embeddings {
	if !appliedSources["embeddings"] {
		refuse("embeddings", "is declared, and this runtime does not search by meaning yet",
			"no embeddings block; a collection nothing can search reads as available")
	}

	switch parsed, err := url.Parse(e.URL); {
	case err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https"):
		refuse("embeddings.url", fmt.Sprintf("%q is not an http or https URL", e.URL),
			"the full URL of an OpenAI-compatible embeddings resource")
	case parsed.User != nil:
		// A credential goes where the redactor knows it, and a URL is written into the
		// record and into every refusal that names the endpoint.
		refuse("embeddings.url", "carries user information",
			"the URL alone; a credential goes in `credential`, where the redactor knows it")
	}
	if strings.TrimSpace(e.Model) == "" {
		refuse("embeddings.model", "is empty", "the model name the endpoint expects")
	}
	if e.Credential != "" && !configReference.MatchString(e.Credential) {
		refuse("embeddings.credential", "is not a single ${config.x} reference",
			"${config.x} naming a value set with `gronin config set --secret`, never a literal")
	}

	resolved := &Embeddings{
		URL: e.URL, Model: e.Model, Credential: e.Credential,
		RequestTimeout: DefaultRequestTimeout,
	}
	if declared, found := duration(e.RequestTimeout, "embeddings.request_timeout", refuse); found {
		resolved.RequestTimeout = declared
	}
	return resolved
}

// duration reads a declared bound, reporting what is wrong with it rather than falling
// back to the default in silence: a bound the deployment wrote and the runtime ignored is
// the shape of mistake this catalogue exists to refuse.
func duration(text, field string, refuse func(field, found, accepted string)) (time.Duration, bool) {
	if text == "" {
		return 0, false
	}
	parsed, err := time.ParseDuration(text)
	if err != nil {
		refuse(field, fmt.Sprintf("%q is not a duration", text),
			"a duration such as 30s, 60s or 2m")
		return 0, false
	}
	if parsed <= 0 {
		refuse(field, fmt.Sprintf("%q is not positive", text),
			"a duration above zero; a bound of nothing refuses every retrieval")
		return 0, false
	}
	return parsed, true
}
