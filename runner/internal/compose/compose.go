// Package compose detects a project's docker compose files and starts/stops
// the stack defined by one of them (e.g. docker-compose.dev.yml vs
// docker-compose.prod.yml - one runs at a time, see api.handleContainersStart).
package compose

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Runner runs an external command in dir and returns its combined output.
// It's an interface so tests can fake command execution without invoking a
// real docker binary or needing real compose files on disk.
type Runner interface {
	Run(name string, args []string, dir string) ([]byte, error)
}

// execRunner is the real Runner, backed by os/exec.
type execRunner struct{}

func (execRunner) Run(name string, args []string, dir string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

// DefaultRunner is the Runner used by Up/Down/Status.
var DefaultRunner Runner = execRunner{}

// composeFileRe matches the compose file naming styles in common use:
// docker-compose.yml, compose.yaml, docker-compose.dev.yml,
// docker-compose-prod.yml, docker-compose_staging.yaml, compose.override.yml.
var composeFileRe = regexp.MustCompile(`(?i)^(docker-)?compose([._-][a-z0-9._-]+)?\.ya?ml$`)

// dockerfileRe matches Dockerfile, Dockerfile.dev, and app.dockerfile.
var dockerfileRe = regexp.MustCompile(`(?i)^(dockerfile(\..+)?|.+\.dockerfile)$`)

// Detected is what Detect found at the top level of a project directory.
type Detected struct {
	ComposeFiles  []string // file names (not paths), sorted
	HasDockerfile bool
}

// Detect scans dir's top level (not subdirectories) for compose files and
// Dockerfiles.
func Detect(dir string) (Detected, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Detected{}, fmt.Errorf("read dir %q: %w", dir, err)
	}
	d := Detected{ComposeFiles: []string{}}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch name := e.Name(); {
		case composeFileRe.MatchString(name):
			d.ComposeFiles = append(d.ComposeFiles, name)
		case dockerfileRe.MatchString(name):
			d.HasDockerfile = true
		}
	}
	sort.Strings(d.ComposeFiles)
	return d, nil
}

// defaultNames is the preference order when a project has several compose
// files and none was ever chosen - the same names `docker compose` itself
// picks up with no -f flag.
var defaultNames = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

// DefaultFile picks which of files to use when nothing was chosen yet: the
// only one if there's exactly one, else the plain unsuffixed name if present,
// else "" (the caller must ask the user to choose).
func DefaultFile(files []string) string {
	if len(files) == 1 {
		return files[0]
	}
	for _, want := range defaultNames {
		for _, f := range files {
			if strings.EqualFold(f, want) {
				return f
			}
		}
	}
	return ""
}

// Container is one container belonging to a project's compose stack.
type Container struct {
	Service     string `json:"service"`
	State       string `json:"state"`       // docker's state: running, exited, created, ...
	ComposeFile string `json:"composeFile"` // base name of the file it was started from
}

// Up runs `docker compose -f file up -d` in projectDir.
func Up(projectDir, file string) error { return UpWith(DefaultRunner, projectDir, file) }

// Down runs `docker compose -f file down --remove-orphans` in projectDir.
func Down(projectDir, file string) error { return DownWith(DefaultRunner, projectDir, file) }

// Status lists every container compose started from projectDir.
func Status(projectDir string) ([]Container, error) { return StatusWith(DefaultRunner, projectDir) }

// UpWith is Up with an injected Runner, for testing.
func UpWith(r Runner, projectDir, file string) error {
	return run(r, projectDir, "compose", "-f", file, "up", "-d")
}

// DownWith is Down with an injected Runner, for testing. --remove-orphans
// also removes containers of services that exist only in a different compose
// file of the same project (all files in one directory share one compose
// project name), so "stop" really means everything from this project is off.
func DownWith(r Runner, projectDir, file string) error {
	return run(r, projectDir, "compose", "-f", file, "down", "--remove-orphans")
}

func run(r Runner, dir string, args ...string) error {
	out, err := r.Run("docker", args, dir)
	if err != nil {
		return fmt.Errorf("docker %s: %v: %s", strings.Join(args, " "), err, lastLines(string(out), 5))
	}
	return nil
}

// StatusWith is Status with an injected Runner, for testing. It asks docker
// (not compose) for containers labelled with this working dir, so it sees
// containers from any compose file in the project, not just one.
func StatusWith(r Runner, projectDir string) ([]Container, error) {
	out, err := r.Run("docker", []string{
		"ps", "-a",
		"--filter", "label=com.docker.compose.project.working_dir=" + projectDir,
		"--format", `{{.Label "com.docker.compose.service"}}|{{.State}}|{{.Label "com.docker.compose.project.config_files"}}`,
	}, projectDir)
	if err != nil {
		return nil, fmt.Errorf("docker ps: %v: %s", err, lastLines(string(out), 5))
	}
	containers := []Container{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "|", 3)
		if len(parts) != 3 {
			continue
		}
		// config_files is a comma-separated list of absolute paths; the
		// first is the one we passed with -f.
		file := ""
		if first := strings.SplitN(parts[2], ",", 2)[0]; first != "" {
			file = filepath.Base(first)
		}
		containers = append(containers, Container{
			Service:     parts[0],
			State:       parts[1],
			ComposeFile: file,
		})
	}
	return containers, nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " / ")
}
