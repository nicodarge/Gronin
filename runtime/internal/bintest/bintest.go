// Package bintest builds the gronin executable and runs it, so a test can assert
// against the artifact that ships rather than the packages behind it.
//
// Three things are real only in the executable: the argument vector that carries the
// containment flags, the embedded playbook schema, and the static linkage. A package
// test passes while each of them is broken.
package bintest

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Result is what one invocation of the executable produced.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

var (
	once     sync.Once
	buildDir string
	binPath  string
	buildErr error
)

// Build compiles the executable once per test binary and returns its path.
func Build(t *testing.T) string {
	t.Helper()
	once.Do(func() {
		dir, err := os.MkdirTemp("", "gronin-bintest-")
		if err != nil {
			buildErr = err
			return
		}
		buildDir = dir
		path := filepath.Join(dir, "gronin")
		cmd := exec.CommandContext(context.Background(),
			"go", "build", "-o", path, "github.com/nicodarge/Gronin/runtime/cmd/gronin")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			buildErr = errors.New("building gronin: " + err.Error() + ": " + stderr.String())
			return
		}
		binPath = path
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return binPath
}

// Run invokes the executable with args and returns what it produced. A non-zero exit
// is a Result, not a failure: refusing is a behaviour tests here assert on.
//
// Standard input is empty rather than unset: `config set` reads its value from it, and a
// test exercising a command that does not touch stdin must not block on a terminal that
// happens to be attached to the test binary.
func Run(t *testing.T, args ...string) Result {
	t.Helper()
	return RunWithStdin(t, "", args...)
}

// RunWithStdin is Run with stdin content supplied. A pipe, never a terminal: the same
// path `printf '%s' "$VALUE" | gronin config set key` takes, so the value never has to
// appear in the argument vector this asserts against.
func RunWithStdin(t *testing.T, stdin string, args ...string) Result {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), Build(t), args...)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
	default:
		t.Fatalf("running gronin %v: %v", args, err)
	}
	return Result{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: cmd.ProcessState.ExitCode(),
	}
}
