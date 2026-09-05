package record

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// Blobs holds what is only ever read whole: gathered inputs, the prompt as sent, the raw
// transcript, the agent's report. A full transcript in a database row makes the store
// unreadable with ordinary tools and unbackupable with ordinary habits, and nothing ever
// selects on it.
type Blobs struct {
	dir      string
	redactor *Redactor
}

// OpenBlobs opens or creates the blob directory.
func OpenBlobs(dir string, redactor *Redactor) (*Blobs, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating the blob directory: %w", err)
	}
	return &Blobs{dir: dir, redactor: redactor}, nil
}

// safeName is what may become a path segment. Anything else is hashed instead of
// sanitised: a name that has been sanitised can still collide with another that
// sanitises to the same thing, and the record would then quietly overwrite itself.
var safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// Put writes data for a run under name and returns the reference stored in the row.
// Redaction happens here, at the write boundary, not in the caller.
func (b *Blobs) Put(runID, name string, data []byte) (string, error) {
	if !safeName.MatchString(runID) {
		return "", fmt.Errorf("run id %q cannot name a directory", runID)
	}
	if !safeName.MatchString(name) {
		sum := sha256.Sum256([]byte(name))
		name = "x-" + hex.EncodeToString(sum[:8])
	}

	dir := filepath.Join(b.dir, runID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating the run's blob directory: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), b.redactor.RedactBytes(data), 0o600); err != nil {
		return "", fmt.Errorf("writing blob %s: %w", name, err)
	}
	return filepath.Join(runID, name), nil
}

// Get reads a blob back by the reference Put returned.
func (b *Blobs) Get(ref string) ([]byte, error) {
	path, err := b.resolve(ref)
	if err != nil {
		return nil, err
	}
	// resolve above is what makes this safe, and it is the reason it exists.
	return os.ReadFile(path) //nolint:gosec
}

// resolve refuses a reference that leaves the blob directory. References come from the
// database, which the runtime writes — but a store is a file an operator can edit, and a
// path is resolved before it is trusted rather than after.
func (b *Blobs) resolve(ref string) (string, error) {
	root, err := filepath.Abs(b.dir)
	if err != nil {
		return "", err
	}
	path, err := filepath.Abs(filepath.Join(root, ref))
	if err != nil {
		return "", err
	}
	inside, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}
	if inside == ".." || len(inside) >= 3 && inside[:3] == ".."+string(filepath.Separator) {
		return "", fmt.Errorf("blob reference %q resolves outside the blob directory", ref)
	}
	return path, nil
}

// Remove deletes every blob belonging to a run.
func (b *Blobs) Remove(runID string) error {
	if !safeName.MatchString(runID) {
		return fmt.Errorf("run id %q cannot name a directory", runID)
	}
	return os.RemoveAll(filepath.Join(b.dir, runID))
}
