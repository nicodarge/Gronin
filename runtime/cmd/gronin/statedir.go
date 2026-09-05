package main

import (
	"os"
	"path/filepath"
)

// stateDirEnv names the state directory for a deployment that does not pass the flag.
// This is the runtime's own configuration, not a playbook's: FR-008 keeps the process
// environment out of interpolation, and this is not interpolation.
const stateDirEnv = "GRONIN_STATE_DIR"

// defaultStateDir is where a deployment writes when it says nothing. Everything the
// runtime persists lives under one configurable directory, so nothing is written
// outside it and a deployment can be moved by moving one path.
func defaultStateDir() string {
	if dir := os.Getenv(stateDirEnv); dir != "" {
		return dir
	}
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "gronin")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".local", "state", "gronin")
	}
	return filepath.Join(os.TempDir(), "gronin")
}
