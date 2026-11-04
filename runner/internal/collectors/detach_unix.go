//go:build !windows

package collectors

import (
	"os/exec"
	"syscall"
)

// detach starts cmd in its own session so it outlives the runner.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
