//go:build !windows

package idle

import (
	"os"
	"strings"
)

// Detect reports what state a Shutdown() will actually reach on this Linux
// machine, by reading /sys/power/state - the kernel's own list of supported
// sleep states. "mem" means suspend-to-RAM (S3) is available, which is what
// `systemctl suspend` targets.
//
// Note what this deliberately does NOT check: whether a wake path exists.
// On Linux the common failure is not the sleep state but the NIC - a WiFi
// card whose radio gets rfkill'd on suspend cannot receive a magic packet
// however well WoWLAN is armed, and Ethernet needs `ethtool -s <if> wol g`.
// Both are per-interface, outside this package's job, and documented in
// FLOWS.md "Per-machine wake matrix".
func Detect() Capability {
	b, err := os.ReadFile("/sys/power/state")
	if err != nil {
		return Capability{Detail: "cannot read /sys/power/state: " + err.Error()}
	}
	for _, s := range strings.Fields(string(b)) {
		if s == "mem" {
			return Capability{Reached: SleepS3, S3Available: true}
		}
	}
	return Capability{Detail: "/sys/power/state has no \"mem\" state - this kernel/firmware offers no suspend-to-RAM"}
}
