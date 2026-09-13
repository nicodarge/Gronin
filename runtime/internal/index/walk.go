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

// Document is one file a walk read.
type Document struct {
	// Source is the path relative to the collection's directory, with forward slashes.
	Source string
	// Digest is the SHA-256 of Content, in hex.
	Digest  string
	Bytes   int64
	Content []byte
}

// Skipped is an entry a walk did not read, and why.
type Skipped struct {
	Source string
	Reason string
}

// Walk is what a directory holds, as the index sees it.
type Walk struct {
	Documents []Document
	Skipped   []Skipped
}

// WalkDirectory reads every text file under dir, in lexical order.
//
// No symbolic link is followed, wherever it points, so nothing outside the directory is
// read through one and a loop cannot hold the walk. The directory itself is resolved
// first: it is the path the deployment declared, and a mount point is often a link.
//
// A directory that does not exist, and a file that cannot be read, are errors naming the
// path (FR-228). Neither reads as an empty collection, which the agent would be told
// holds nothing.
func WalkDirectory(ctx context.Context, dir string) (Walk, error) {
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return Walk{}, fmt.Errorf("the collection's directory %s cannot be read: %w", dir, err)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return Walk{}, fmt.Errorf("the collection's directory %s is not a directory", dir)
	}

	var walk Walk
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
			Source: source, Digest: hex.EncodeToString(sum[:]),
			Bytes: int64(len(content)), Content: content,
		})
		return nil
	})
	if err != nil {
		return Walk{}, err
	}
	return walk, nil
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
