package playbook

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Refusal is one playbook the loader would not accept, and why.
//
// A shape-layer failure carries a Reason; the semantic gate carries Problems, one per
// rule it broke, because a playbook usually breaks more than one and an author should
// see them together.
type Refusal struct {
	Path     string
	Reason   error
	Problems []Problem
}

func (r Refusal) Error() string {
	var out strings.Builder
	out.WriteString(r.Path)
	if r.Reason != nil {
		out.WriteString(": " + r.Reason.Error())
	}
	for _, problem := range r.Problems {
		out.WriteString("\n  " + problem.Error())
	}
	return out.String()
}

// Loaded is the result of reading a directory: what was accepted, and what was not.
//
// Both halves are returned together on purpose. The gate refuses the whole set rather
// than arming the valid remainder — a runtime that refuses two playbooks out of six and
// starts anyway is the failure this design exists to prevent — and the caller needs the
// accepted list to say how many there were.
type Loaded struct {
	Playbooks []*Playbook
	Refusals  []Refusal
}

// OK reports whether anything may be armed.
func (l Loaded) OK() bool { return len(l.Refusals) == 0 }

// Err joins every refusal, or nil.
func (l Loaded) Err() error {
	if len(l.Refusals) == 0 {
		return nil
	}
	problems := make([]error, 0, len(l.Refusals))
	for _, refusal := range l.Refusals {
		problems = append(problems, refusal)
	}
	return errors.Join(problems...)
}

// Load reads every playbook in a directory.
//
// It reads the whole directory before deciding anything: every refusal is collected, so
// an author fixes them in one pass rather than one round trip at a time, which is how a
// gate gets switched off.
func Load(dir string, dep Deployment) (Loaded, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Loaded{}, fmt.Errorf("reading the playbook directory: %w", err)
	}

	var loaded Loaded
	seen := map[string]string{}

	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if ext := filepath.Ext(entry.Name()); ext != ".yaml" && ext != ".yml" {
			continue
		}
		paths = append(paths, filepath.Join(dir, entry.Name()))
	}
	sort.Strings(paths)

	for _, path := range paths {
		book, err := ParseFile(path)
		if err != nil {
			loaded.Refusals = append(loaded.Refusals, Refusal{Path: path, Reason: err})
			continue
		}
		// FR-042. Two playbooks with one name make every record ambiguous, and the
		// single-flight guard would treat them as the same playbook.
		if first, duplicate := seen[book.Name]; duplicate {
			loaded.Refusals = append(loaded.Refusals, Refusal{
				Path:   path,
				Reason: fmt.Errorf("name: %q is already declared by %s", book.Name, first),
			})
			continue
		}
		// The semantic gate. The schema is the shape layer and is not the gate: a
		// document it accepts may still be refused here, by design.
		if problems := Validate(book, dep); len(problems) > 0 {
			loaded.Refusals = append(loaded.Refusals, Refusal{Path: path, Problems: problems})
			continue
		}

		seen[book.Name] = filepath.Base(path)
		loaded.Playbooks = append(loaded.Playbooks, book)
	}
	return loaded, nil
}

// LoadFile reads one playbook through the same layers Load puts each file through, and
// returns the refusal when any refuses it. It is how a trigger that waited reads its
// playbook again (FR-121). Another file in the directory declaring the same name refuses it
// whichever sorts first: Load refuses the directory for it, so accepting the one file here
// would run what a start would refuse (FR-042).
func LoadFile(path string, dep Deployment) (*Playbook, error) {
	book, err := ParseFile(path)
	if err != nil {
		return nil, Refusal{Path: path, Reason: err}
	}
	if problems := Validate(book, dep); len(problems) > 0 {
		return nil, Refusal{Path: path, Problems: problems}
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		return nil, Refusal{Path: path, Reason: fmt.Errorf("reading the playbook directory: %w", err)}
	}
	for _, entry := range entries {
		sibling := filepath.Join(filepath.Dir(path), entry.Name())
		if ext := filepath.Ext(entry.Name()); entry.IsDir() || (ext != ".yaml" && ext != ".yml") ||
			filepath.Clean(sibling) == filepath.Clean(path) {
			continue
		}
		other, err := ParseFile(sibling)
		if err != nil {
			continue
		}
		if other.Name == book.Name {
			return nil, Refusal{Path: path, Reason: fmt.Errorf(
				"name: %q is also declared by %s", book.Name, entry.Name())}
		}
	}
	return book, nil
}

// Find returns the loaded playbook with a name.
func (l Loaded) Find(name string) (*Playbook, bool) {
	for _, book := range l.Playbooks {
		if book.Name == name {
			return book, true
		}
	}
	return nil, false
}

// Names lists the loaded playbooks.
func (l Loaded) Names() []string {
	names := make([]string, 0, len(l.Playbooks))
	for _, book := range l.Playbooks {
		names = append(names, book.Name)
	}
	sort.Strings(names)
	return names
}
