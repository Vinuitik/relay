package idle

import (
	"fmt"
	"strings"
)

// SleepState is the deepest sleep state this machine can actually reach.
// It exists because "suspend" is not one thing across the fleet: an S3
// machine freezes and can only be revived by a magic packet, while a
// Modern Standby machine has no S3 at all and reaches S0ix instead. Which
// one a machine reaches decides whether a wakerd on its LAN is required or
// useless, so the runner reports it at startup rather than leaving the
// operator to guess.
type SleepState int

const (
	// SleepUnknown means detection failed (command missing, unparseable
	// output). Treated as "assume S3-like", the conservative choice: it
	// keeps the warning about needing a wake path.
	SleepUnknown SleepState = iota
	// SleepS3 is suspend-to-RAM. The OS is frozen, the network stack is
	// down, the machine is off the tailnet. Only a Wake-on-LAN magic
	// packet from a device on its own LAN segment can revive it.
	SleepS3
	// SleepModernStandby (S0ix / "S0 Low Power Idle") is the state Windows
	// machines with no S3 reach. The OS keeps nominally running, but
	// ordinary processes are frozen in DRIPS, so runnerd does NOT reliably
	// keep serving HTTP while in it. Do not treat it as "still reachable"
	// without measuring that machine.
	SleepModernStandby
	// SleepHibernate (S4) is RAM written to disk, machine powered off. On
	// Windows, SetSuspendState silently lands here instead of S3/S0ix when
	// hibernation is enabled - see shutdown_windows.go.
	SleepHibernate
)

func (s SleepState) String() string {
	switch s {
	case SleepS3:
		return "S3 (suspend-to-RAM)"
	case SleepModernStandby:
		return "S0ix (Modern Standby)"
	case SleepHibernate:
		return "S4 (hibernate)"
	default:
		return "unknown"
	}
}

// NeedsWakeOnLAN reports whether reviving a machine in this state requires a
// magic packet from a wakerd on its LAN. True for every state except
// Modern Standby, where the machine may (unverified per-machine) come back
// on its own network activity.
func (s SleepState) NeedsWakeOnLAN() bool {
	return s != SleepModernStandby
}

// Capability is what Detect reports about this machine's sleep options.
type Capability struct {
	// Reached is the state a Shutdown() call will actually land in.
	Reached SleepState
	// S3Available is whether plain suspend-to-RAM exists at all. False on
	// Modern Standby hardware, where firmware offers S0ix instead.
	S3Available bool
	// HibernateEnabled matters only on Windows, where an enabled
	// hibernation file makes SetSuspendState hibernate rather than
	// suspend, regardless of its "don't hibernate" argument.
	HibernateEnabled bool
	// Detail is the raw reason/summary, for logging.
	Detail string
}

// String renders a Capability for a one-line startup log.
func (c Capability) String() string {
	s := fmt.Sprintf("suspend lands in %s", c.Reached)
	if c.Reached.NeedsWakeOnLAN() {
		s += "; needs a wakerd on this LAN to come back"
	} else {
		s += "; wake path unverified - measure before relying on it"
	}
	if c.Detail != "" {
		s += " (" + c.Detail + ")"
	}
	return s
}

// parsePowercfg is split out from Detect so it can be tested against
// captured powercfg output without a Windows host. It lives here rather
// than in capability_windows.go precisely so those tests run on every
// platform - it is pure string handling with no Windows API in it.
//
// powercfg prints an "available" section followed by a "not available"
// section, so a bare substring search would happily match a state listed as
// unavailable. Instead we track which section we're in: everything before
// the "not available" heading is available.
func parsePowercfg(out string) Capability {
	cap := Capability{}
	available := true
	for _, line := range strings.Split(out, "\n") {
		l := strings.ToLower(strings.TrimSpace(line))
		switch {
		case strings.Contains(l, "following sleep states are not available"):
			available = false
			continue
		case strings.Contains(l, "following sleep states are available"):
			available = true
			continue
		}
		if !available || l == "" {
			continue
		}
		if strings.HasPrefix(l, "standby (s3)") {
			cap.S3Available = true
		}
		if strings.HasPrefix(l, "standby (s0 low power idle)") {
			cap.Reached = SleepModernStandby
		}
		if strings.HasPrefix(l, "hibernate") {
			cap.HibernateEnabled = true
		}
	}

	// Order matters: hibernation wins, because SetSuspendState honours it
	// over the caller's intent.
	switch {
	case cap.HibernateEnabled:
		cap.Reached = SleepHibernate
		cap.Detail = "hibernation is enabled, so SetSuspendState will hibernate instead of suspend - run `powercfg /hibernate off` if you want a lighter state"
	case cap.Reached == SleepModernStandby:
		cap.Detail = "no S3 on this firmware; processes freeze in DRIPS, so this runner will not reliably answer HTTP while asleep"
	case cap.S3Available:
		cap.Reached = SleepS3
	default:
		cap.Reached = SleepUnknown
		cap.Detail = "powercfg /a reported no usable sleep state"
	}
	return cap
}
