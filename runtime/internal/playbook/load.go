package playbook

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Refusal is one playbook the loader would not accept, and why.
type Refusal struct {
	Path   string
	Reason error
}

func (r Refusal) Error() string { return r.Path + ": " + r.Reason.Error() }

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
func Load(dir string) (Loaded, error) {
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
		seen[book.Name] = filepath.Base(path)
		loaded.Playbooks = append(loaded.Playbooks, book)
	}
	return loaded, nil
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
