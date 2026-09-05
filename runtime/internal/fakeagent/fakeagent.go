// Package fakeagent builds the stub agent from testdata and reports the knobs it
// understands, so every agent-stage test drives one binary rather than each test growing
// its own idea of what the agent process does.
//
// It sits beside bintest for the same reason: a test-support package under internal/,
// named so nothing production imports it by accident.
package fakeagent

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// The modes the stub understands, set through the FAKECLAUDE_MODE environment variable.
const (
	ModeVar = "FAKECLAUDE_MODE"

	// ModeSuccess is the default: a receipt, a message, and a successful result.
	ModeSuccess = "success"
	// ModeTimeout emits the receipt and then never terminates, so the runtime's timeout
	// is what ends it.
	ModeTimeout = "timeout"
	// ModeMalformed emits a line that is not JSON.
	ModeMalformed = "malformed"
	// ModeMismatch reports a tool set wider than it was given, which is what the receipt
	// check exists to catch.
	ModeMismatch = "mismatch"
	// ModeExitError emits a failing result and exits non-zero.
	ModeExitError = "exit-error"
	// ModeUnknown emits event types and fields the decoder has never seen, so a CLI
	// update degrades rather than breaks.
	ModeUnknown = "unknown"
	// ModeOversize emits a line past what the decoder will read, so the stream fails
	// after the receipt is already out — a failure of the stream and not of the report.
	ModeOversize = "oversize"

	// ResultVar hands the stub the report it should answer with.
	ResultVar = "FAKECLAUDE_RESULT"
	// KeySourceVar is the credential source it should claim.
	KeySourceVar = "FAKECLAUDE_KEY_SOURCE"
)

var (
	once     sync.Once
	buildDir string
	path     string
	buildErr error
)

// Build compiles the stub once per test binary and returns its path.
func Build(t *testing.T) string {
	t.Helper()
	once.Do(func() {
		dir, err := os.MkdirTemp("", "gronin-fakeagent-")
		if err != nil {
			buildErr = err
			return
		}
		buildDir = dir
		binary := filepath.Join(dir, "claude")

		// The stub lives under testdata, which the go tool leaves out of ./... — so it
		// is never linted or vetted with the rest, and it has to be named by path.
		cmd := exec.CommandContext(context.Background(),
			"go", "build", "-o", binary, sourceDir())
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			buildErr = errors.New("building fakeclaude: " + err.Error() + ": " + stderr.String())
			return
		}
		path = binary
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return path
}

// Cleanup removes the built stub. A package using Build calls it from TestMain.
func Cleanup() {
	if buildDir != "" {
		_ = os.RemoveAll(buildDir)
	}
}

// sourceDir locates testdata/fakeclaude from this file, so a test can run from whatever
// package directory it happens to live in.
func sourceDir() string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "./testdata/fakeclaude"
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "testdata", "fakeclaude")
}
