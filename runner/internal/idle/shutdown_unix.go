//go:build !windows

package idle

import (
	"fmt"
	"os/exec"
)

// osShutdowner is the real Shutdowner on Linux (and other unix-likes).
type osShutdowner struct{}

// DefaultShutdowner is the Shutdowner Monitor should be wired with in
// production.
var DefaultShutdowner Shutdowner = osShutdowner{}

// Shutdown powers the machine off (S5), preferring `systemctl poweroff`
// since that's the standard systemd hook and works from a service with no
// controlling terminal. Falls back to `shutdown -h now` when systemctl
// isn't on PATH (non-systemd init, e.g. some minimal/embedded Linux setups)
// - that's the POSIX-standard shutdown invocation and should be present
// wherever systemctl isn't.
func (osShutdowner) Shutdown() error {
	name := "systemctl"
	args := []string{"poweroff"}
	if _, err := exec.LookPath(name); err != nil {
		name = "shutdown"
		args = []string{"-h", "now"}
	}

	if err := exec.Command(name, args...).Run(); err != nil {
		return fmt.Errorf("run %s %v: %w", name, args, err)
	}
	return nil
}
