//go:build !windows

package activity

import "time"

// LocalIdleTime always returns ErrUnsupported on non-Windows platforms.
//
// There is no portable, dependency-free way to read local keyboard/mouse
// input on Linux for a headless/sessionless service: it would require
// either reading from a specific display server's input stack (X11/Wayland,
// which assumes a logged-in graphical session that this runner's real
// deployment target - a systemd *system* service with no login
// session/logind seat, see shutdown_unix.go's Shutdown doc comment - does
// not have) or shelling out to a tool like xprintidle that may not even be
// installed. Rather than invent something fragile, this is left explicitly
// unsupported: a Linux server has no console user in the first place,
// which is exactly the case where local input detection doesn't matter -
// idle.Monitor treats ErrUnsupported as "no local input signal" and falls
// back to session state (a) and phone activity pings (b) alone.
func LocalIdleTime() (time.Duration, error) {
	return 0, ErrUnsupported
}
