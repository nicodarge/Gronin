//go:build linux

package main

import (
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"

	"github.com/nicodarge/Gronin/runtime/internal/bintest"
)

// Reviewing this branch found a real defect: golang.org/x/term's ReadPassword leaves
// ISIG set, so a Ctrl-C during the no-echo terminal prompt used to kill the process
// before its own deferred echo-restore ran, leaving the operator's terminal without echo
// until they ran `stty sane`. promptTerminal in config_cmd.go now catches the signal and
// restores the terminal itself — this drives the built executable through an actual
// pseudo-terminal and confirms echo comes back after an interrupt lands mid-prompt.
func TestConfigSetRestoresTheTerminalOnInterrupt(t *testing.T) {
	stateDir := t.TempDir()

	cmd := exec.CommandContext(t.Context(), bintest.Build(t), "--state-dir", stateDir, "config", "set", "ttykey")
	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("starting under a pty: %v", err)
	}
	t.Cleanup(func() { _ = ptmx.Close() })

	// Waits for the prompt's own output rather than a fixed sleep: promptTerminal turns
	// echo off before it writes "value: ", so the first byte read here is proof it
	// reached that point, not a guess at how long that takes.
	buf := make([]byte, 64)
	deadline := time.Now().Add(5 * time.Second)
	var read int
	for read == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the prompt never wrote anything")
		}
		_ = ptmx.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, _ := ptmx.Read(buf)
		read += n
	}

	if echoOn(t, ptmx) {
		t.Fatal("echo was still on once the prompt had written its label")
	}

	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("signalling the process: %v", err)
	}

	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("the process did not exit after SIGINT")
	}

	if !echoOn(t, ptmx) {
		t.Fatal("echo was not restored after the interrupt")
	}
}

// echoOn reads the pty's current termios directly: golang.org/x/term's own State is
// opaque, and what this asserts on is exactly the bit it does not expose.
func echoOn(t *testing.T, ptmx interface{ Fd() uintptr }) bool {
	t.Helper()
	termios, err := unix.IoctlGetTermios(int(ptmx.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatalf("reading termios: %v", err)
	}
	return termios.Lflag&unix.ECHO != 0
}

type echoProbe struct {
	t      *testing.T
	tty    interface{ Fd() uintptr }
	echoed []bool
}

func (p *echoProbe) Write(b []byte) (int, error) {
	p.echoed = append(p.echoed, echoOn(p.t, p.tty))
	return len(b), nil
}

// The label is the operator's cue to type, so echo has to be off by the time it is
// written; checked at the write itself, which a timing-based check cannot pin down.
func TestConfigSetTurnsEchoOffBeforeItPrompts(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatalf("opening a pty: %v", err)
	}
	t.Cleanup(func() { _ = ptmx.Close(); _ = tty.Close() })
	if !echoOn(t, tty) {
		t.Fatal("a fresh pty should start with echo on")
	}

	go func() { _, _ = ptmx.Write([]byte("s3cret\n")) }()
	probe := &echoProbe{t: t, tty: tty}
	value, err := promptTerminal(probe, tty)
	if err != nil {
		t.Fatalf("prompting: %v", err)
	}

	if value != "s3cret" {
		t.Fatalf("read %q, want %q", value, "s3cret")
	}
	if len(probe.echoed) == 0 || probe.echoed[0] {
		t.Fatalf("echo when the label was written: %v, want off", probe.echoed)
	}
	if !echoOn(t, tty) {
		t.Fatal("echo was not restored after a value was read")
	}
}
