package index

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"unicode/utf8"
)

// MaxDocumentBytes is the largest file a directory walk reads. Every file is read and
// digested on every retrieval (research.md §11), so the read is a bound to state.
const MaxDocumentBytes = 1 << 20

// Why a walk left an entry out. Every skipped entry carries one (FR-213), so an operator
// listing a collection sees what an embeddings API would never receive and why.
const (
	ReasonSymlink    = "symbolic link, not followed"
	ReasonTooLarge   = "larger than 1 MiB"
	ReasonNotText    = "not text"
	ReasonNotRegular = "not a regular file"
)

// Document is one file a walk read. Its text is not kept: an update reads again each
// document it adds as it indexes that document, one at a time inside its write
// transaction, so neither a walk nor an update holds more than one file's text at once.
type Document struct {
	// Source is the path relative to the collection's directory, with forward slashes.
	Source string
	// Digest is the SHA-256 of the file's content, in hex.
	Digest string
	Bytes  int64
}

// Skipped is an entry a walk did not read, and why.
type Skipped struct {
	Source string
	Reason string
}

// Walk is what a source holds, as the index sees it: a directory (this file) or recorded
// reports (reports.go). The index itself knows nothing of runs — a reports source is
// handed to it as documents, the way a directory's are handed to it as files.
type Walk struct {
	Documents []Document
	Skipped   []Skipped
	root      string
	// held is a reports source's content, already in hand from the walk that found it:
	// nothing needs rereading from a run's record. Nil for a directory walk, whose
	// contentOf rereads from root instead.
	held map[string][]byte
	// passages cuts one of this walk's documents into what the index stores, text for a
	// directory and a report's values for reports.
	passages func(source string, content []byte) []Passage
	// rewalk repeats this walk from the same source, for an update that has to work its
	// difference out again because another update committed a generation meanwhile.
	rewalk func(ctx context.Context) (Walk, error)
}

// WalkDirectory reads every text file under dir, in lexical order.
//
// No symbolic link is followed, wherever it points, so nothing outside the directory is
// read through one and a loop cannot hold the walk. The directory itself is resolved
// first, every component of it: it is the path the deployment declared, and a mount point
// is often a link.
//
// A directory that does not exist, and a file that cannot be read, are errors naming the
// path (FR-228). Neither reads as an empty collection, which the agent would be told
// holds nothing.
func WalkDirectory(ctx context.Context, dir string) (Walk, error) {
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return Walk{}, fmt.Errorf("the collection's directory %s cannot be read: %w", dir, err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return Walk{}, fmt.Errorf("the collection's directory %s cannot be read: %w", dir, err)
	}
	if !info.IsDir() {
		return Walk{}, fmt.Errorf("the collection's directory %s is not a directory", dir)
	}

	walk := Walk{root: root, passages: TextPassages}
	walk.rewalk = func(ctx context.Context) (Walk, error) { return WalkDirectory(ctx, root) }
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		source := filepath.ToSlash(relative)
		skip := func(reason string) {
			walk.Skipped = append(walk.Skipped, Skipped{Source: source, Reason: reason})
		}

		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			skip(ReasonSymlink)
			return nil
		case info.IsDir():
			return nil
		case !info.Mode().IsRegular():
			skip(ReasonNotRegular)
			return nil
		case info.Size() > MaxDocumentBytes:
			skip(ReasonTooLarge)
			return nil
		}

		content, err := readDocument(path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}
		if bytes.IndexByte(content, 0) >= 0 || !utf8.Valid(content) {
			skip(ReasonNotText)
			return nil
		}
		sum := sha256.Sum256(content)
		walk.Documents = append(walk.Documents, Document{
			Source: source, Digest: hex.EncodeToString(sum[:]), Bytes: int64(len(content)),
		})
		return nil
	})
	if err != nil {
		return Walk{}, err
	}
	return walk, nil
}

// changedError is a document whose content no longer has the digest the walk took.
type changedError struct{ source string }

func (e *changedError) Error() string {
	return e.source + " changed while it was being indexed"
}

// errNoRoot is a Walk not made by WalkDirectory, whose documents cannot be read again.
var errNoRoot = errors.New("this walk was not made by WalkDirectory, so its documents cannot be read again")

// contentOf reads a walked document again to index it, and refuses one whose content no
// longer has the digest the walk took: indexed, it would sit under a generation naming
// content the index does not hold.
//
// read, when not nil, is called after each document's text is read.
func (w Walk) contentOf(document Document, read func(source string)) ([]byte, error) {
	if w.held != nil {
		content, found := w.held[document.Source]
		if !found {
			return nil, fmt.Errorf("%s is not among this walk's documents", document.Source)
		}
		if read != nil {
			read(document.Source)
		}
		sum := sha256.Sum256(content)
		if hex.EncodeToString(sum[:]) != document.Digest {
			return nil, &changedError{source: document.Source}
		}
		return content, nil
	}
	if w.root == "" {
		return nil, errNoRoot
	}
	content, err := readDocument(filepath.Join(w.root, filepath.FromSlash(document.Source)))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", document.Source, err)
	}
	if read != nil {
		read(document.Source)
	}
	sum := sha256.Sum256(content)
	if hex.EncodeToString(sum[:]) != document.Digest {
		return nil, &changedError{source: document.Source}
	}
	return content, nil
}

// errGrew is a file that was within the document bound when it was listed and past it
// when it was read.
var errGrew = errors.New("it grew past the document bound while it was read")

// readDocument reads a file the walk has already seen as regular. O_NOFOLLOW refuses one
// replaced by a link since, and O_NONBLOCK keeps one replaced by a pipe from holding the
// walk.
func readDocument(path string) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0) //nolint:gosec // a path under the collection's own directory, not followed
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	content, err := io.ReadAll(io.LimitReader(file, MaxDocumentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > MaxDocumentBytes {
		return nil, errGrew
	}
	return content, nil
}
