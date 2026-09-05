// Package gather executes a playbook's gather steps into the run working directory,
// bounded by the declared output limits.
package gather

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"

	"strings"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/proc"
)

// DefaultMaxBytes is how much output one step may write before it is truncated. A step
// that produces more than this is almost always a mistake — a log tail with no limit, a
// recursive listing — and the agent stage is charged by the token for reading it.
const DefaultMaxBytes int64 = 1 << 20

// DefaultStepTimeout bounds one step. Without it a gather step that hangs holds the
// playbook's single-flight claim forever, and the run never reaches the timeout that
// belongs to the agent stage.
const DefaultStepTimeout = 2 * time.Minute

// Result is what one step produced.
type Result struct {
	Name      string
	Path      string
	Bytes     int64
	Truncated bool
	ExitCode  int
	Stderr    []byte
	Duration  time.Duration
}

// Step is one command and the name it writes.
type Step struct {
	Run string
	As  string
}

// Options bound the stage.
type Options struct {
	WorkDir     string
	MaxBytes    int64
	StepTimeout time.Duration
	// Env is the environment every step runs with. It is a deliberate, short list rather
	// than the runtime's own environment: a gather step is a command a playbook author
	// wrote, and handing it this process's environment hands it the deployment's
	// credentials — the same reasoning FR-008 applies to interpolation.
	Env []string
}

// ErrStepFailed is returned when a step exits non-zero. FR-010: the run aborts here,
// before the agent stage, so a broken input costs nothing.
var ErrStepFailed = errors.New("a gather step failed")

// safeName is what may become a file in the working directory. The schema constrains it
// too, but a name that reaches this far unchecked is a path traversal into the record.
var safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// Run executes the steps in order, writing each one's output into the working directory.
//
// It stops at the first failure and returns what it has. The results so far are worth
// keeping: they are what the record shows an operator who asks why the run never reached
// the agent.
func Run(ctx context.Context, steps []Step, opts Options) ([]Result, error) {
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultMaxBytes
	}
	if opts.StepTimeout <= 0 {
		opts.StepTimeout = DefaultStepTimeout
	}

	results := make([]Result, 0, len(steps))
	for _, step := range steps {
		result, err := runStep(ctx, step, opts)
		results = append(results, result)
		if err != nil {
			return results, err
		}
	}
	return results, nil
}

func runStep(ctx context.Context, step Step, opts Options) (Result, error) {
	result := Result{Name: step.As}

	if !safeName.MatchString(step.As) {
		return result, fmt.Errorf("gather step writes %q, which is not a name it may write", step.As)
	}
	path := filepath.Join(opts.WorkDir, step.As)
	result.Path = path

	// safeName above is what bounds this path: the name cannot traverse, and the
	// directory is the run's own.
	output, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_EXCL, 0o600) //nolint:gosec
	if errors.Is(err, os.ErrExist) {
		return result, fmt.Errorf("two gather steps write %q", step.As)
	}
	if err != nil {
		return result, fmt.Errorf("creating %s: %w", step.As, err)
	}
	defer func() { _ = output.Close() }()

	stepCtx, cancel := context.WithTimeout(ctx, opts.StepTimeout)
	defer cancel()

	// A step is a shell line, which is what a playbook author writes and what the
	// contract documents. The bound on it is the working directory, the environment and
	// the timeout, not the syntax.
	// Running the playbook's command is the stage. What bounds it is the working
	// directory, the environment it is given and the timeout — never the syntax, which
	// is a shell line by contract.
	cmd := exec.CommandContext(stepCtx, "/bin/sh", "-c", step.Run) //nolint:gosec
	cmd.Dir = opts.WorkDir
	cmd.Env = opts.Env
	// The step is a shell line, so what has to be killed is the group, not the shell.
	proc.Isolate(cmd)

	counter := &limitedWriter{to: output, remaining: opts.MaxBytes}
	var stderr strings.Builder
	cmd.Stdout = counter
	cmd.Stderr = &limitedWriter{to: &stderr, remaining: opts.MaxBytes}

	started := time.Now()
	runErr := cmd.Run()
	result.Duration = time.Since(started)
	result.Bytes = counter.written
	result.Truncated = counter.truncated
	result.Stderr = []byte(stderr.String())

	var exit *exec.ExitError
	switch {
	case runErr == nil:
		result.ExitCode = 0
	case errors.As(runErr, &exit):
		result.ExitCode = exit.ExitCode()
		return result, fmt.Errorf("%w: %s exited %d", ErrStepFailed, step.As, result.ExitCode)
	default:
		result.ExitCode = -1
		return result, fmt.Errorf("%w: %s: %w", ErrStepFailed, step.As, runErr)
	}
	if stepCtx.Err() != nil {
		return result, fmt.Errorf("%w: %s: %w", ErrStepFailed, step.As, stepCtx.Err())
	}
	return result, nil
}

// limitedWriter stops writing at its limit and remembers that it did. It does not fail
// the step: a truncated input is usable, and a step that produced too much is worth
// recording rather than losing.
type limitedWriter struct {
	to        io.Writer
	remaining int64
	written   int64
	truncated bool
}

// It reports the full length as written so the command is never told its output was
// short, which would make a step fail for having produced too much rather than have it
// truncated.
//
// There was a fast path here for a writer with nothing left, and the mutation harness
// found it did nothing: the branch below produces the same result. It was also wrong on
// one input — an empty write against an exhausted limit marked the output truncated,
// which the branch below correctly does not.
func (w *limitedWriter) Write(p []byte) (int, error) {
	chunk := p
	if int64(len(chunk)) > w.remaining {
		chunk = chunk[:max(w.remaining, 0)]
		w.truncated = true
	}
	written, err := w.to.Write(chunk)
	w.written += int64(written)
	w.remaining -= int64(written)
	if err != nil {
		return written, err
	}
	return len(p), nil
}
