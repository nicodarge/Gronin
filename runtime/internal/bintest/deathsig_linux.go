//go:build linux

package bintest

import (
	"os/exec"
	"syscall"
)

// dieWithThisProcess has the kernel SIGKILL the child once the thread that started it is
// gone. A test binary killed from outside, or ended by go test's own -timeout panic, never
// reaches t.Cleanup, and the child it started would otherwise be reparented and keep
// running.
func dieWithThisProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
}
