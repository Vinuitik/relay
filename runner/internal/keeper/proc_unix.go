//go:build !windows

package keeper

import (
	"os"
	"os/exec"
	"syscall"
)

// terminate asks the process to shut down gracefully (the runner then
// kills its agents and flushes sessions to disk).
func terminate(p *os.Process) { _ = p.Signal(syscall.SIGTERM) }

// RelaunchSelf hands over to a freshly installed keeperd binary. Under
// systemd (Restart=always) simply exiting is enough - systemd starts the
// new binary; spawning it ourselves would just be killed with our cgroup.
func RelaunchSelf(path string) error {
	os.Exit(3)
	return nil
}

func hideWindow(*exec.Cmd) {}

// bindToKeeper is a no-op: systemd stops the whole cgroup (keeperd, runner,
// agents) together, so the runner can't outlive keeperd.
func bindToKeeper(*os.Process) {}
