//go:build linux

// Linux sandbox: bubblewrap. --unshare-all gives the script its own
// (empty) network namespace, so no network at all; the new root holds only
// /usr (+ the /lib,/bin symlinks), the script dir at /script, the inputs at
// /in (both read-only) and the scratch dir at /tmp. $HOME is an empty
// tmpfs. Limits: a transient `systemd-run --user --scope` (MemoryMax,
// CPUQuota=25%, RuntimeMaxSec) when the user manager answers, else
// rlimits via prlimit (or `sh -c ulimit`). The timeout kills the whole
// process group through ctx.
//
// Paths ARE remapped: the caller passes "--inputs /in" in Spec.Args (see
// collectors.inputsArg), unlike the Windows sandbox, which uses host paths.
package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	defaultTimeout  = 60 * time.Second
	defaultMemoryMB = 128
	cpuQuota        = "25%"
	maxOutput       = 4 << 20 // per stream; the rest is discarded

	// probeRetryAfter: a failed availability probe is re-run after this,
	// so installing bubblewrap is picked up without a runner restart.
	probeRetryAfter = time.Minute
)

func kind() string { return "bwrap" }

var (
	probeMu   sync.Mutex
	probed    bool
	probeErr  error
	probeAt   time.Time
	scopeOK   bool // systemd-run --user --scope works here
	bwrapPath string
)

func available() error {
	probeMu.Lock()
	defer probeMu.Unlock()
	if probed && (probeErr == nil || time.Since(probeAt) < probeRetryAfter) {
		return probeErr
	}
	probeErr = probe()
	probed, probeAt = true, time.Now()
	return probeErr
}

// probe checks that bwrap exists and a trial run with the real mount set
// succeeds (Ubuntu's AppArmor userns restriction makes it fail otherwise),
// and whether systemd-run can apply limits.
func probe() error {
	bw, err := exec.LookPath("bwrap")
	if err != nil {
		return fmt.Errorf("%w: bwrap not found (install the bubblewrap package)", ErrUnavailable)
	}
	dir, err := os.MkdirTemp("", "relay-sbx-probe-")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer os.RemoveAll(dir)
	args := append(mountArgs(dir, dir, dir, nil), "--", "/usr/bin/true")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, bw, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("%w: bwrap trial run failed: %v: %s", ErrUnavailable, err, strings.TrimSpace(string(out)))
	}
	bwrapPath = bw
	scopeOK = false
	if sr, err := exec.LookPath("systemd-run"); err == nil {
		cmd := exec.CommandContext(ctx, sr, append(scopeArgs(defaultMemoryMB, defaultTimeout), "--", "/usr/bin/true")...)
		scopeOK = cmd.Run() == nil
	}
	return nil
}

func scopeArgs(memMB int, timeout time.Duration) []string {
	return []string{"--user", "--scope", "--quiet", "--collect",
		"-p", "MemoryMax=" + strconv.Itoa(memMB) + "M",
		// Without this the cap is soft: the scope swaps past MemoryMax.
		"-p", "MemorySwapMax=0",
		"-p", "CPUQuota=" + cpuQuota,
		"-p", "RuntimeMaxSec=" + strconv.Itoa(int(timeout/time.Second)+5)}
}

// mountArgs builds the bwrap namespace + filesystem arguments. extra are
// additional read-only binds (src == dst), e.g. a python outside /usr.
func mountArgs(scriptDir, inputsDir, scratchDir string, extra []string) []string {
	a := []string{
		"--unshare-all", "--die-with-parent", "--new-session",
		"--clearenv",
		"--setenv", "PATH", "/usr/local/bin:/usr/bin:/bin",
		"--setenv", "HOME", "/home",
		"--setenv", "TMPDIR", "/tmp",
		"--setenv", "LANG", "C.UTF-8",
		"--setenv", "PYTHONDONTWRITEBYTECODE", "1",
		"--setenv", "PYTHONIOENCODING", "utf-8",
		"--setenv", "PYTHONNOUSERSITE", "1",
		"--ro-bind", "/usr", "/usr",
	}
	for _, d := range []string{"/lib", "/lib64", "/lib32", "/libx32", "/bin", "/sbin"} {
		fi, err := os.Lstat(d)
		switch {
		case err != nil:
		case fi.Mode()&os.ModeSymlink != 0:
			if t, err := os.Readlink(d); err == nil {
				a = append(a, "--symlink", t, d)
			}
		case fi.IsDir():
			a = append(a, "--ro-bind", d, d)
		}
	}
	a = append(a,
		// Only the dynamic-linker cache and the timezone; no other /etc.
		"--ro-bind-try", "/etc/ld.so.cache", "/etc/ld.so.cache",
		"--ro-bind-try", "/etc/localtime", "/etc/localtime",
		"--proc", "/proc", "--dev", "/dev",
		"--tmpfs", "/home",
	)
	for _, e := range extra {
		a = append(a, "--ro-bind", e, e)
	}
	return append(a,
		"--ro-bind", scriptDir, "/script",
		"--ro-bind", inputsDir, "/in",
		"--bind", scratchDir, "/tmp",
		// The new root is a private tmpfs; make it read-only too, so /tmp
		// is the only writable place.
		"--remount-ro", "/",
		"--chdir", "/tmp",
	)
}

// pythonMount resolves the interpreter to its real path; one outside /usr
// gets its install prefix (the dir above bin/) bound read-only - never "/"
// or anything containing $HOME.
func pythonMount(py string) (path string, extra []string, err error) {
	real, err := filepath.EvalSymlinks(py)
	if err != nil {
		return "", nil, fmt.Errorf("python %q: %w", py, err)
	}
	if strings.HasPrefix(real, "/usr/") {
		return real, nil, nil
	}
	prefix := filepath.Dir(filepath.Dir(real))
	home, _ := os.UserHomeDir()
	if prefix == "/" || (home != "" && (prefix == home || strings.HasPrefix(home+"/", prefix+"/"))) {
		return "", nil, fmt.Errorf("python %q: install prefix %q is too broad to expose in the sandbox", py, prefix)
	}
	return real, []string{prefix}, nil
}

func run(ctx context.Context, s Spec) (Result, error) {
	if err := available(); err != nil {
		return Result{}, err
	}
	for name, p := range map[string]string{"Python": s.Python, "ScriptDir": s.ScriptDir, "InputsDir": s.InputsDir, "ScratchDir": s.ScratchDir} {
		if p == "" {
			return Result{}, fmt.Errorf("sandbox: %s is empty", name)
		}
	}
	timeout, mem := s.Timeout, s.MemoryMB
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	if mem <= 0 {
		mem = defaultMemoryMB
	}
	py, extra, err := pythonMount(s.Python)
	if err != nil {
		return Result{}, err
	}
	probeMu.Lock()
	bw, scope := bwrapPath, scopeOK
	probeMu.Unlock()

	inner := append(mountArgs(s.ScriptDir, s.InputsDir, s.ScratchDir, extra), "--", py, "/script/collector.py")
	inner = append(inner, s.Args...)
	var name string
	var argv []string
	switch {
	case scope:
		name, argv = "systemd-run", append(append(scopeArgs(mem, timeout), "--", bw), inner...)
	default:
		name, argv = rlimitWrap(bw, inner, mem, timeout)
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, argv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	var stdout, stderr capped
	stdout.max, stderr.max = maxOutput, maxOutput
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	res := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if ctx.Err() == context.DeadlineExceeded {
		res.ExitCode = -1
		return res, fmt.Errorf("sandbox: timed out after %s", timeout)
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.ExitCode = ee.ExitCode()
		return res, nil
	}
	if err != nil {
		res.ExitCode = -1
		return res, err
	}
	return res, nil
}

// rlimitWrap applies address-space and CPU-time rlimits when systemd-run
// can't: prlimit if present, else a POSIX sh ulimit. Address space is a
// virtual-memory cap, looser than cgroup MemoryMax; there's no CPU % cap.
func rlimitWrap(bw string, inner []string, memMB int, timeout time.Duration) (string, []string) {
	secs := int(timeout/time.Second) + 1
	if pl, err := exec.LookPath("prlimit"); err == nil {
		return pl, append([]string{
			"--as=" + strconv.Itoa(memMB<<20),
			"--cpu=" + strconv.Itoa(secs),
			"--nofile=256", "--", bw}, inner...)
	}
	script := fmt.Sprintf(`ulimit -v %d; ulimit -t %d; ulimit -n 256; exec "$@"`, memMB<<10, secs)
	return "/bin/sh", append([]string{"-c", script, "sh", bw}, inner...)
}

// capped is a bytes.Buffer that silently drops writes past max.
type capped struct {
	bytes.Buffer
	max int
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.Len(); room > 0 {
		if len(p) > room {
			c.Buffer.Write(p[:room])
		} else {
			c.Buffer.Write(p)
		}
	}
	return len(p), nil
}
