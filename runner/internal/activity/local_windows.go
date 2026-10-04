//go:build windows

package activity

import (
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// user32/kernel32 are loaded lazily via golang.org/x/sys/windows - no cgo,
// and golang.org/x/sys/windows is already an indirect module dependency
// (promoted to direct by this file). GetLastInputInfo itself isn't wrapped
// by x/sys/windows, so it's called directly via NewProc, the same pattern
// x/sys/windows uses internally for syscalls it doesn't expose a typed
// wrapper for.
var (
	user32               = windows.NewLazySystemDLL("user32.dll")
	kernel32             = windows.NewLazySystemDLL("kernel32.dll")
	procGetLastInputInfo = user32.NewProc("GetLastInputInfo")
	procGetTickCount     = kernel32.NewProc("GetTickCount")
)

// lastInputInfo mirrors the Win32 LASTINPUTINFO struct.
type lastInputInfo struct {
	cbSize uint32
	dwTime uint32
}

// LocalIdleTime reports how long since the last local keyboard/mouse input
// on Windows, via Win32 GetLastInputInfo compared against GetTickCount.
func LocalIdleTime() (time.Duration, error) {
	var info lastInputInfo
	info.cbSize = uint32(unsafe.Sizeof(info))

	r, _, callErr := procGetLastInputInfo.Call(uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		return 0, fmt.Errorf("activity: GetLastInputInfo failed: %w", callErr)
	}

	tick, _, _ := procGetTickCount.Call()
	now := uint32(tick)

	// dwTime and now are both millisecond tick counts that wrap around
	// every ~49.7 days (uint32 ms). Unsigned subtraction handles that
	// wraparound correctly as long as the gap itself is under ~49.7 days,
	// which it always is for a check running every
	// idle.DefaultCheckInterval.
	elapsedMS := now - info.dwTime
	return time.Duration(elapsedMS) * time.Millisecond, nil
}
