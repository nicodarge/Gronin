// Package agent drives the bounded child process: the argument vector that carries the
// containment flags, the stream-json decode, the timeout, and the tool-set receipt check.
package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/proc"
)

// DefaultTimeout bounds the stage when a playbook declares none.
const DefaultTimeout = 30 * time.Minute

// Options are what the runtime supplies around the declaration.
type Options struct {
	// Executable is the agent CLI. Named rather than searched, so a deployment knows
	// which one it is driving.
	Executable string
	WorkDir    string
	Prompt     string
	Timeout    time.Duration

	// Env is the child's environment. Credentials travel here and never in argv
	// (FR-012): a command line is journalled and shipped to log aggregation, where the
	// value then sits for the whole retention window.
	Env []string

	// MCPServers is the strict configuration this run is bounded by, written to a file
	// in the working directory.
	MCPServers map[string]any

	// OnEvent, when set, is called for each decoded event as it arrives. The receipt
	// check uses it to abort before the run produces output.
	OnEvent func(Event) error
}

// Outcome is what the stage produced.
type Outcome struct {
	Stream   *Stream
	TimedOut bool
	ExitCode int
	Duration time.Duration
	Stderr   []byte
	Aborted  error // an OnEvent refusal, kept so the caller can tell it from a failure
	// DecodeErr is the stream itself failing — an oversized line, a broken pipe.
	// Separate from Aborted: reporting it as a refusal would tell a caller the bounds
	// stopped a run they did not.
	DecodeErr error
	ArgvUsed  []string
}

// ErrNoTerminalEvent is returned when the child ended without saying how it ended.
var ErrNoTerminalEvent = errors.New("the agent process produced no terminal event")

// Run drives one agent stage to completion, or to its timeout.
//
// A timeout is not an error here: the run is marked timed out, and whatever the child
// said before it was killed is kept. A partial transcript is the only evidence of what a
// run was doing when it stopped, and discarding it because the run failed is how a
// failure becomes unexplainable.
func Run(ctx context.Context, decl Declaration, opts Options) (*Outcome, error) {
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}

	mcpConfig, err := WriteMCPConfig(opts.WorkDir, opts.MCPServers)
	if err != nil {
		return nil, err
	}
	args, err := BuildArgs(decl, mcpConfig)
	if err != nil {
		return nil, err
	}
	outcome := &Outcome{ArgvUsed: args}

	stageCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	// Driving the agent process is the product. The vector comes from BuildArgs, which
	// is where the bounds are applied and what the receipt is checked against.
	cmd := exec.CommandContext(stageCtx, opts.Executable, args...) //nolint:gosec
	cmd.Dir = opts.WorkDir
	cmd.Env = opts.Env
	cmd.Stdin = strings.NewReader(opts.Prompt)
	proc.Isolate(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("reading the agent's output: %w", err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	started := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting the agent process: %w", err)
	}

	stream, decodeErr := Decode(stdout, opts.OnEvent)
	var refused *RefusedByCallback
	switch {
	case decodeErr == nil:
	case errors.As(decodeErr, &refused):
		// The receipt check aborting the run. The child is killed rather than left
		// running: it has already been told to do something the runtime has decided it
		// may not do.
		outcome.Aborted = refused.Unwrap()
		cancel()
	default:
		// The stream itself failed — an oversized line, a broken pipe. Reporting that as
		// Aborted would tell a caller the bounds refused a run they did not.
		outcome.DecodeErr = decodeErr
		cancel()
	}
	// Drain whatever is left so the child is not blocked writing into a pipe nobody
	// reads, which would make the kill above wait for its own WaitDelay.
	_, _ = io.Copy(io.Discard, stdout)

	waitErr := cmd.Wait()
	outcome.Duration = time.Since(started)
	outcome.Stream = stream
	outcome.Stderr = []byte(stderr.String())

	if errors.Is(stageCtx.Err(), context.DeadlineExceeded) {
		outcome.TimedOut = true
		return outcome, nil
	}

	var exit *exec.ExitError
	switch {
	case waitErr == nil:
		outcome.ExitCode = 0
	case errors.As(waitErr, &exit):
		outcome.ExitCode = exit.ExitCode()
	case outcome.Aborted != nil || outcome.DecodeErr != nil:
		// Killed on purpose, and the wait error describes how it died rather than a
		// failure of the runtime. It is not always an ExitError: a child killed while
		// writing can leave Wait returning exec.ErrWaitDelay instead, which made this
		// return an error and turned a deliberate kill into a flaky failure — once, out
		// of many runs, which is how it was found rather than reasoned about.
		outcome.ExitCode = -1
	default:
		return outcome, fmt.Errorf("the agent process: %w", waitErr)
	}
	return outcome, nil
}

// Report is the structured result the agent answered with, and the only thing a sink is
// allowed to act on.
func (o *Outcome) Report() ([]byte, error) {
	if o.Stream == nil || o.Stream.Result == nil {
		return nil, ErrNoTerminalEvent
	}
	return o.Stream.Result.Result, nil
}
