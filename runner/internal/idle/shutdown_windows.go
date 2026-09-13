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

// Shutdown powers the machine off (S5) via `shutdown /s /t 0` (/s = shut
// down rather than restart, /t 0 = no delay).
func (osShutdowner) Shutdown() error {
	if err := exec.Command("shutdown", "/s", "/t", "0").Run(); err != nil {
		return fmt.Errorf("run shutdown /s /t 0: %w", err)
	}
	return nil
}
