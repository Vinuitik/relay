//go:build linux

package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These run the REAL sandbox. They skip when bwrap is missing or can't
// create namespaces here (scripts/test-collectors.sh installs it in a
// container with the needed seccomp/apparmor relaxations).

type env struct{ py, script, in, scratch string }

func setup(t *testing.T, code string) env {
	t.Helper()
	if err := Available(); err != nil {
		t.Skipf("sandbox unavailable: %v", err)
	}
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not on PATH")
	}
	e := env{py: py, script: t.TempDir(), in: t.TempDir(), scratch: t.TempDir()}
	if err := os.WriteFile(filepath.Join(e.script, "collector.py"), []byte(code), 0o644); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e env) run(t *testing.T, timeout time.Duration, args ...string) (Result, error) {
	t.Helper()
	return Run(context.Background(), Spec{Python: e.py, ScriptDir: e.script, InputsDir: e.in,
		ScratchDir: e.scratch, Args: append([]string{"--inputs", "/in"}, args...), Timeout: timeout, MemoryMB: 128})
}

const valueScript = `
import json, sys, os
inputs = sys.argv[sys.argv.index("--inputs") + 1]
n = int(open(os.path.join(inputs, "n.txt")).read())
print(json.dumps({"value": n * 2, "unit": "min", "evidence": [{"label": "argv", "value": " ".join(sys.argv[3:])}]}))
`

func checkValue(t *testing.T) {
	e := setup(t, valueScript)
	if err := os.WriteFile(filepath.Join(e.in, "n.txt"), []byte("21"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := e.run(t, 20*time.Second, "--from", "F", "--to", "T")
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("run: err=%v exit=%d stderr=%s", err, res.ExitCode, res.Stderr)
	}
	if !strings.Contains(string(res.Stdout), `"value": 42`) || !strings.Contains(string(res.Stdout), "--from F --to T") {
		t.Fatalf("stdout = %s", res.Stdout)
	}
}

func TestRealSandboxValue(t *testing.T) { checkValue(t) }

func TestRealSandboxNoNetwork(t *testing.T) {
	e := setup(t, `
import socket, urllib.request
out = []
for target in [("1.1.1.1", 53), ("127.0.0.1", 5600), ("8.8.8.8", 443)]:
    try:
        s = socket.create_connection(target, timeout=3); s.close(); out.append("OPEN %s:%d" % target)
    except OSError as ex:
        out.append("blocked %s:%d %s" % (target[0], target[1], ex))
try:
    socket.getaddrinfo("example.com", 80); out.append("OPEN dns")
except OSError as ex:
    out.append("blocked dns %s" % ex)
try:
    urllib.request.urlopen("http://example.com", timeout=3); out.append("OPEN http")
except Exception as ex:
    out.append("blocked http %s" % ex)
print("\n".join(out))
`)
	res, err := e.run(t, 30*time.Second)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("run: err=%v exit=%d stderr=%s", err, res.ExitCode, res.Stderr)
	}
	if strings.Contains(string(res.Stdout), "OPEN") || strings.Count(string(res.Stdout), "blocked") != 5 {
		t.Fatalf("network reachable from sandbox:\n%s", res.Stdout)
	}
}

func TestRealSandboxFilesystem(t *testing.T) {
	home, _ := os.UserHomeDir()
	e := setup(t, `
import os, sys
home = sys.argv[-1]
checks = []
def must_fail(name, fn):
    try:
        fn(); checks.append("ESCAPED " + name)
    except OSError:
        checks.append("ok " + name)
must_fail("read /etc/passwd", lambda: open("/etc/passwd").read())
must_fail("read /etc/shadow", lambda: open("/etc/shadow").read())
must_fail("list host home", lambda: os.listdir(home) if home not in ("/", "/home", "") else (_ for _ in ()).throw(OSError("skip")))
must_fail("write /in", lambda: open("/in/x", "w").write("x"))
must_fail("write /script", lambda: open("/script/x", "w").write("x"))
must_fail("write /", lambda: open("/x", "w").write("x"))
must_fail("write /usr", lambda: open("/usr/x", "w").write("x"))
must_fail("write /home tmpfs then host", lambda: os.listdir("/home/" + os.path.basename(home)))
open("/tmp/scratch.txt", "w").write("ok")
print("\n".join(checks))
`)
	res, err := e.run(t, 20*time.Second, home)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("run: err=%v exit=%d stderr=%s", err, res.ExitCode, res.Stderr)
	}
	if strings.Contains(string(res.Stdout), "ESCAPED") || strings.Count(string(res.Stdout), "ok ") != 8 {
		t.Fatalf("filesystem escape:\n%s", res.Stdout)
	}
	if b, err := os.ReadFile(filepath.Join(e.scratch, "scratch.txt")); err != nil || string(b) != "ok" {
		t.Fatalf("scratch write not visible on host: %v %q", err, b)
	}
}

func TestRealSandboxTimeout(t *testing.T) {
	e := setup(t, "while True:\n    pass\n")
	start := time.Now()
	_, err := e.run(t, 2*time.Second)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want timeout", err)
	}
	if d := time.Since(start); d > 8*time.Second {
		t.Fatalf("timeout took %s", d)
	}
}

const memScript = "b = bytearray(512 * 1024 * 1024)\nfor i in range(0, len(b), 4096):\n    b[i] = 1\nprint('ALLOCATED')\n"

func TestRealSandboxMemoryCap(t *testing.T) {
	e := setup(t, memScript)
	res, err := e.run(t, 20*time.Second)
	if err == nil && res.ExitCode == 0 {
		t.Fatalf("512MB allocation succeeded under a 128MB cap: %s", res.Stdout)
	}
}

func TestRealSandboxRlimitFallback(t *testing.T) {
	if err := Available(); err != nil {
		t.Skipf("sandbox unavailable: %v", err)
	}
	probeMu.Lock()
	saved := scopeOK
	scopeOK = false
	probeMu.Unlock()
	defer func() { probeMu.Lock(); scopeOK = saved; probeMu.Unlock() }()
	checkValue(t)
	e := setup(t, memScript)
	if res, err := e.run(t, 20*time.Second); err == nil && res.ExitCode == 0 {
		t.Fatalf("rlimit fallback: 512MB allocation succeeded: %s", res.Stdout)
	}
}
