// Package proc gives a child process a boundary the runtime can actually end.
//
// Killing a command is not killing what it started. A gather step is a shell line, so
// `sh -c "sleep 30"` makes the runtime the parent of the shell and the shell the parent
// of the sleep: cancelling the context kills the shell, the sleep inherits the pipe, and
// Wait blocks on that pipe for the full thirty seconds. Measured exactly that way — a
// 150 millisecond timeout took thirty seconds to return.
//
// Two things fix it, and both are needed. The child gets its own process group so the
// signal reaches everything it started, and Wait is given a deadline of its own so a
// process that ignores the signal, or a grandchild holding a pipe open, cannot hold the
// runtime after it.
package proc

import (
	"os/exec"
	"syscall"
	"time"
)

// WaitDelay is how long Wait keeps waiting for output after the process was told to
// stop. Long enough for a child that is exiting to flush, short enough that a run's
// timeout means something.
const WaitDelay = 5 * time.Second

// Isolate puts cmd in its own process group and makes cancellation reach the group.
// Call it before starting the command.
func Isolate(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true

	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// The negative pid is the group. Without it the signal reaches the shell and
		// not what the shell started, which is the whole failure this package exists for.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = WaitDelay
}
