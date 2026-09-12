//go:build linux

package main

import (
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"

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
