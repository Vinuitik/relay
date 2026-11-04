package sandbox

import (
	"context"
	"errors"
	"time"
)

// ErrUnavailable means this OS/machine cannot sandbox; callers must refuse to run.
var ErrUnavailable = errors.New("sandbox unavailable")

// Spec describes one sandboxed run: no network, InputsDir read-only, ScratchDir writable.
type Spec struct {
	Python     string        // python interpreter path on the host
	ScriptDir  string        // dir holding collector.py, mounted read-only
	InputsDir  string        // mounted read-only as the script's --inputs
	ScratchDir string        // the only writable dir
	Args       []string      // args after the script path
	Timeout    time.Duration // hard kill
	MemoryMB   int
}

type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// Kind returns "bwrap", "appcontainer" or "none".
func Kind() string { return kind() }

// Available reports whether Run can work here, with a reason if not.
func Available() error { return available() }

// Run executes the spec sandboxed. Implementations: sandbox_linux.go (bwrap),
// sandbox_windows.go (AppContainer), sandbox_other.go (returns ErrUnavailable).
func Run(ctx context.Context, s Spec) (Result, error) { return run(ctx, s) }
