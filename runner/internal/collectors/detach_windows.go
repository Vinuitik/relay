//go:build windows

package collectors

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// detach starts cmd without a console, in its own process group, so it
// outlives the runner.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS}
}
