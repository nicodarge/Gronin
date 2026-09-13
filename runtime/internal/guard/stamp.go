package guard

import (
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"syscall"
)

// fileStamp is what a playbook file's metadata says about whether it changed: enough to
// notice an edit, a replacement or a removal without reading the file. An edit that keeps
// the size and the inode within the filesystem's timestamp resolution goes unseen.
type fileStamp struct {
	modified int64
	size     int64
	inode    uint64
}

// directoryStamp is the stamp of every playbook file in a directory. A sibling that
// appears, changes or goes can refuse a re-read as surely as the file itself does (FR-042),
// so all of them are compared.
type directoryStamp map[string]fileStamp

func stampOf(path string) (directoryStamp, error) {
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	stamp := directoryStamp{}
	for _, entry := range entries {
		if ext := filepath.Ext(entry.Name()); entry.IsDir() || (ext != ".yaml" && ext != ".yml") {
			continue
		}
		info, err := entry.Info()
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		file := fileStamp{modified: info.ModTime().UnixNano(), size: info.Size()}
		if system, ok := info.Sys().(*syscall.Stat_t); ok {
			file.inode = system.Ino
		}
		stamp[entry.Name()] = file
	}
	return stamp, nil
}

func (s directoryStamp) equal(other directoryStamp) bool { return maps.Equal(s, other) }
