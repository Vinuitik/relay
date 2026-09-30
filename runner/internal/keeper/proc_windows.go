//go:build windows

package keeper

import (
	"os"
	"os/exec"
	"syscall"
)

// terminate on Windows is a hard kill - there's no SIGTERM. The runner
// tolerates it: sessions are saved every 2s and agents exit when their
// stdin closes (see internal/session/store.go).
func terminate(p *os.Process) { _ = p.Kill() }

// RelaunchSelf starts the freshly installed keeperd as a detached process
// and exits. Nothing restarts keeperd on Windows (the scheduled task only
// fires at boot), so it must hand over to its successor itself.
func RelaunchSelf(path string) error {
	const detachedProcess = 0x00000008
	const createNoWindow = 0x08000000
	// Task Scheduler runs its task inside a job object; breaking away keeps
	// the successor alive when this process (the task's) exits. Not every
	// job allows breakaway, so fall back to a plain detached start.
	const breakawayFromJob = 0x01000000
	base := uint32(detachedProcess | createNoWindow | syscall.CREATE_NEW_PROCESS_GROUP)
	var err error
	for _, flags := range []uint32{base | breakawayFromJob, base} {
		cmd := exec.Command(path)
		cmd.Env = os.Environ()
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags}
		if err = cmd.Start(); err == nil {
			break
		}
	}
	if err != nil {
		return err
	}
	os.Exit(0)
	return nil
}

// hideWindow keeps the runner child from opening a console window.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
