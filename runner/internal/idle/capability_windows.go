//go:build windows

package idle

import "os/exec"

// Detect reports what state a Shutdown() will actually reach on this
// Windows machine, by parsing `powercfg /a` (the only authoritative source
// - the available states are firmware-dependent and not knowable from the
// OS version).
//
// Two traps this exists to catch, both seen on real hardware:
//
//	(1) Modern Standby machines have NO S3 at all. powercfg reports
//	    "Standby (S0 Low Power Idle)" as available and S3 as "disabled when
//	    S0 low power idle is supported". A wakerd aimed at such a machine is
//	    pointless, and the operator needs to know that before wiring one up.
//	(2) With hibernation enabled, SetSuspendState hibernates (S4) instead of
//	    suspending, so the machine lands in a deeper state than intended -
//	    see shutdown_windows.go. `powercfg /hibernate off` is the fix.
func Detect() Capability {
	out, err := exec.Command("powercfg", "/a").Output()
	if err != nil {
		return Capability{Detail: "powercfg /a failed: " + err.Error()}
	}
	return parsePowercfg(string(out))
}
