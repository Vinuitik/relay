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

// Shutdown suspends the machine to S3 (suspend-to-RAM) via `systemctl
// suspend` - not `systemctl poweroff` (S5, full power-off). The project
// switched from S5 to sleep: the power delta between S3 and S5 is only
// about 1W, while S5 costs a ~60s boot to come back and is far more
// fragile to wake reliably than a machine already sitting in S3 with its
// NIC armed.
//
// CONFIRMED on the real target: the runner runs as a systemd *system*
// service (no login session/logind seat behind it), and its service user
// has NO passwordless sudo. Under those conditions `systemctl suspend` is
// refused by polkit, not silently retried or downgraded to poweroff - so
// that failure is surfaced here as a specific, actionable error (naming
// exactly what's missing: a polkit rule or sudoers entry granting this
// service user suspend rights) rather than a bare "exit status 1". This
// must NEVER fall back to `systemctl poweroff` on failure - S5 is a
// materially more disruptive action than what was actually asked for, and
// silently escalating to it on any suspend failure would be its own kind of
// bug.
func (osShutdowner) Shutdown() error {
	if err := exec.Command("systemctl", "suspend").Run(); err != nil {
		return fmt.Errorf("systemctl suspend failed: %w - this runner's service user likely lacks "+
			"passwordless permission to suspend (no login session/logind seat, no passwordless sudo); "+
			"grant it via a polkit rule (org.freedesktop.login1.suspend) or a sudoers entry, then retry "+
			"- see runner/FLOWS.md \"Idle-suspend\"", err)
	}
	return nil
}
