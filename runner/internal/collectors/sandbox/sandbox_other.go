//go:build !linux && !windows

package sandbox

import (
	"context"
	"fmt"
	"runtime"
)

// No sandbox on this OS yet (macOS Seatbelt is [NOT IMPLEMENTED]), so
// collectors never run here.
func kind() string { return "none" }

func available() error {
	return fmt.Errorf("%w: no collector sandbox for %s", ErrUnavailable, runtime.GOOS)
}

func run(ctx context.Context, s Spec) (Result, error) { return Result{}, available() }
