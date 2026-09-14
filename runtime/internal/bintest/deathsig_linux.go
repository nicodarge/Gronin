//go:build linux

package bintest

import (
	"os/exec"
	"syscall"
)

// dieWithThisProcess has the kernel SIGKILL the child once the thread that started it is
// gone; see start.
func dieWithThisProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
}
