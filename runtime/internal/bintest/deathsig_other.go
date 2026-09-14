//go:build !linux

package bintest

import "os/exec"

// dieWithThisProcess does nothing here: only Linux has a parent-death signal. A test binary
// killed before its cleanups run leaves what it started running.
func dieWithThisProcess(*exec.Cmd) {}
