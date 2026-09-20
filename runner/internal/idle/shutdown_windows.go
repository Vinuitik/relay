//go:build windows

package idle

import (
	"fmt"
	"os/exec"
)

// osShutdowner is the real Shutdowner on Windows.
type osShutdowner struct{}

// DefaultShutdowner is the Shutdowner Monitor should be wired with in
// production.
var DefaultShutdowner Shutdowner = osShutdowner{}

// Shutdown suspends the machine (S3 / Modern Standby, not S5 poweroff - see
// shutdown_unix.go's Shutdown for why the project switched) via
// `rundll32.exe powrprof.dll,SetSuspendState 0,1,0`.
//
// NOTE: if hibernation is enabled on this machine, SetSuspendState
// hibernates (writes RAM to disk and powers off, S4) instead of suspending
// (S3) - Windows treats the first SetSuspendState parameter (hibernate:
// false here) as advisory, not a hard guarantee, once hibernation is
// available. Disable hibernation (`powercfg /hibernate off`) on the target
// machine if true S3/Modern Standby is required.
func (osShutdowner) Shutdown() error {
	if err := exec.Command("rundll32.exe", "powrprof.dll,SetSuspendState", "0,1,0").Run(); err != nil {
		return fmt.Errorf("run rundll32.exe powrprof.dll,SetSuspendState 0,1,0: %w", err)
	}
	return nil
}
