// Package compose starts/stops a project's docker compose stack.
package compose

import "os/exec"

// Runner runs an external command in dir. It's an interface so tests can
// fake command execution without invoking a real docker binary or needing
// real compose files on disk.
type Runner interface {
	Run(name string, args []string, dir string) error
}

// execRunner is the real Runner, backed by os/exec.
type execRunner struct{}

func (execRunner) Run(name string, args []string, dir string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	return cmd.Run()
}

// DefaultRunner is the Runner used by Start/Stop.
var DefaultRunner Runner = execRunner{}

// Start runs `docker compose up -d` in projectDir.
func Start(projectDir string) error {
	return StartWith(DefaultRunner, projectDir)
}

// Stop runs `docker compose down` in projectDir.
func Stop(projectDir string) error {
	return StopWith(DefaultRunner, projectDir)
}

// StartWith is Start with an injected Runner, for testing.
func StartWith(r Runner, projectDir string) error {
	return r.Run("docker", []string{"compose", "up", "-d"}, projectDir)
}

// StopWith is Stop with an injected Runner, for testing.
func StopWith(r Runner, projectDir string) error {
	return r.Run("docker", []string{"compose", "down"}, projectDir)
}
