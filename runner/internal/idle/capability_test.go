package idle

import "testing"

// dellOutput is real `powercfg /a` output from a Modern Standby laptop (the
// project's Dell), captured 2026-09-26. The point of keeping it verbatim: it
// is the case that broke the original "just call SetSuspendState" assumption
// - S3 is listed under "not available", and Hibernate under "available".
const dellOutput = `The following sleep states are available on this system:
    Standby (S0 Low Power Idle) Network Connected
    Hibernate
    Fast Startup

The following sleep states are not available on this system:
    Standby (S1)
	The system firmware does not support this standby state.

    Standby (S2)
	The system firmware does not support this standby state.

    Standby (S3)
	This standby state is disabled when S0 low power idle is supported.

    Hybrid Sleep
	Standby (S3) is not available.
`

func TestParsePowercfgModernStandbyWithHibernate(t *testing.T) {
	got := parsePowercfg(dellOutput)
	if got.S3Available {
		t.Error("S3Available = true, want false (S3 is in the not-available section)")
	}
	if !got.HibernateEnabled {
		t.Error("HibernateEnabled = false, want true")
	}
	// Hibernation must win: SetSuspendState honours it over the caller's
	// intent, so reporting S0ix here would be a lie.
	if got.Reached != SleepHibernate {
		t.Errorf("Reached = %v, want %v", got.Reached, SleepHibernate)
	}
}

func TestParsePowercfgS3Machine(t *testing.T) {
	out := `The following sleep states are available on this system:
    Standby (S3)

The following sleep states are not available on this system:
    Standby (S0 Low Power Idle)
	The system firmware does not support this standby state.

    Hibernate
	Hibernation has not been enabled.
`
	got := parsePowercfg(out)
	if !got.S3Available {
		t.Error("S3Available = false, want true")
	}
	if got.HibernateEnabled {
		t.Error("HibernateEnabled = true, want false (listed as not available)")
	}
	if got.Reached != SleepS3 {
		t.Errorf("Reached = %v, want %v", got.Reached, SleepS3)
	}
	if !got.Reached.NeedsWakeOnLAN() {
		t.Error("NeedsWakeOnLAN() = false, want true for S3")
	}
}

func TestParsePowercfgModernStandbyNoHibernate(t *testing.T) {
	out := `The following sleep states are available on this system:
    Standby (S0 Low Power Idle) Network Connected

The following sleep states are not available on this system:
    Standby (S3)
	This standby state is disabled when S0 low power idle is supported.

    Hibernate
	Hibernation has not been enabled.
`
	got := parsePowercfg(out)
	if got.Reached != SleepModernStandby {
		t.Errorf("Reached = %v, want %v", got.Reached, SleepModernStandby)
	}
	if got.Reached.NeedsWakeOnLAN() {
		t.Error("NeedsWakeOnLAN() = true, want false for Modern Standby")
	}
}

func TestParsePowercfgGarbage(t *testing.T) {
	got := parsePowercfg("nonsense\n")
	if got.Reached != SleepUnknown {
		t.Errorf("Reached = %v, want %v", got.Reached, SleepUnknown)
	}
	if !got.Reached.NeedsWakeOnLAN() {
		t.Error("unknown must be treated as needing WoL (conservative)")
	}
}
