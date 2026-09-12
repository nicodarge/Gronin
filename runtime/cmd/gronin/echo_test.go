//go:build linux || darwin

package main

import (
	"testing"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

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

// echoOn reads the pty's current termios directly: golang.org/x/term's own State is
// opaque, and what this asserts on is exactly the bit it does not expose.
func echoOn(t *testing.T, ptmx interface{ Fd() uintptr }) bool {
	t.Helper()
	termios, err := unix.IoctlGetTermios(int(ptmx.Fd()), ioctlGetTermios)
	if err != nil {
		t.Fatalf("reading termios: %v", err)
	}
	return termios.Lflag&unix.ECHO != 0
}
