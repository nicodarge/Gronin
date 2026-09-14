// Package bintest builds the gronin executable and runs it, so a test can assert
// against the artifact that ships rather than the packages behind it.
//
// Three things are real only in the executable: the argument vector that carries the
// containment flags, the embedded playbook schema, and the static linkage. A package
// test passes while each of them is broken.
package bintest

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
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
// Standard input is a pipe reading nothing, the same as an unset exec.Cmd.Stdin
// defaults to (the null device) — named explicitly here because `config set` reads its
// value from stdin, and RunWithStdin below is what a test reaches for once it needs
// something other than that default.
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

	err := start(cmd)
	if err == nil {
		err = cmd.Wait()
	}
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

// Process is a built gronin held open: a `serve` kept up, or a `run` a test freezes or
// kills mid-run. Every such test goes through it, so that none leaves a process behind.
type Process struct {
	cmd *exec.Cmd

	mu      sync.Mutex
	lines   []string
	closed  bool
	arrived chan struct{} // closed and replaced whenever a line arrives or output closes
	stderr  bytes.Buffer

	waited   chan struct{}
	exitCode int
	waitErr  error
}

// Start runs the executable with args and returns at once. Standard output is read line
// by line through Next and Expect; standard error is kept whole for Stderr. The process
// inherits this one's environment, so t.Setenv reaches it. It is killed at cleanup
// whatever state it is in — a stopped process included, since SIGKILL ends one.
func Start(t *testing.T, args ...string) *Process {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), Build(t), args...)
	p := &Process{cmd: cmd, arrived: make(chan struct{}), waited: make(chan struct{})}
	cmd.Stderr = &lockedWriter{p: p}
	// A child of gronin still holding its standard error would otherwise keep Wait from
	// ever returning.
	cmd.WaitDelay = 5 * time.Second

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := start(cmd); err != nil {
		t.Fatalf("starting gronin %v: %v", args, err)
	}

	// Read as the lines arrive rather than on demand, into a buffer with no bound: a
	// process whose pipe fills blocks on its next write, and a test that had merely
	// stopped reading would see a hang of its own making.
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			p.push(scanner.Text(), false)
		}
		p.push("", true)
	}()
	// exec.Cmd.Wait closes the pipe once the process exits, so it is called only after
	// everything has been read from it; a line still in the pipe would otherwise be lost.
	go func() {
		<-drained
		err := cmd.Wait()
		p.exitCode = cmd.ProcessState.ExitCode()
		var exitErr *exec.ExitError
		if err != nil && !errors.As(err, &exitErr) {
			p.waitErr = err
		}
		close(p.waited)
	}()

	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGKILL)
		_, _ = p.Wait(10 * time.Second)
	})
	return p
}

// start starts cmd so that, on Linux, it does not outlive this process: a test binary
// killed from outside or by go test's -timeout panic never runs the cleanup that would
// have ended it.
//
// The parent-death signal fires when the starting OS thread exits, and the runtime ends a
// thread when a goroutine locked to it returns still locked. A new goroutine is never
// locked, so the thread it starts from lives as long as the process whatever the caller
// holds.
func start(cmd *exec.Cmd) error {
	dieWithThisProcess(cmd)
	started := make(chan error, 1)
	go func() { started <- cmd.Start() }()
	return <-started
}

func (p *Process) push(line string, closed bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if closed {
		p.closed = true
	} else {
		p.lines = append(p.lines, line)
	}
	close(p.arrived)
	p.arrived = make(chan struct{})
}

// next returns the next unread line, or reports that output has closed, or hands back
// the channel that is closed when either changes.
func (p *Process) next() (line string, ok bool, closed bool, arrived <-chan struct{}) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.lines) > 0 {
		line, p.lines = p.lines[0], p.lines[1:]
		return line, true, false, nil
	}
	return "", false, p.closed, p.arrived
}

// Next returns the next line the process printed, failing the test if none arrives
// within the bound or the process closed its output first.
func (p *Process) Next(t *testing.T, within time.Duration) string {
	t.Helper()
	return p.Expect(t, "", within)
}

// Expect reads lines until one contains want and returns it, failing the test if none
// does within the bound. The lines read past on the way are consumed.
func (p *Process) Expect(t *testing.T, want string, within time.Duration) string {
	t.Helper()
	timeout := time.NewTimer(within)
	defer timeout.Stop()
	var seen []string
	for {
		line, ok, closed, arrived := p.next()
		switch {
		case ok && strings.Contains(line, want):
			return line
		case ok:
			seen = append(seen, line)
			continue
		case closed:
			t.Fatalf("gronin closed its output before printing a line containing %q; printed %q; stderr = %q",
				want, seen, p.Stderr())
		}
		select {
		case <-arrived:
		case <-timeout.C:
			t.Fatalf("gronin printed no line containing %q within %s; printed %q; stderr = %q",
				want, within, seen, p.Stderr())
		}
	}
}

// Signal delivers sig: SIGSTOP freezes the process, SIGCONT resumes it, SIGTERM asks it
// to stop, SIGKILL ends it with no chance to write anything on the way out.
func (p *Process) Signal(sig syscall.Signal) error {
	return p.cmd.Process.Signal(sig)
}

// Wait returns the exit code once the process has ended. It gives up after the bound
// rather than hanging the test, and says so: a process that should have exited and did
// not is a finding, not a timeout of the suite.
func (p *Process) Wait(within time.Duration) (int, error) {
	select {
	case <-p.waited:
		return p.exitCode, p.waitErr
	case <-time.After(within):
		return -1, errors.New("gronin had not exited within " + within.String())
	}
}

// Stderr is everything the process has written to standard error so far.
func (p *Process) Stderr() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stderr.String()
}

type lockedWriter struct{ p *Process }

func (w *lockedWriter) Write(data []byte) (int, error) {
	w.p.mu.Lock()
	defer w.p.mu.Unlock()
	return w.p.stderr.Write(data)
}
