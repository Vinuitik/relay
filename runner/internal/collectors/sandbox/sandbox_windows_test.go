//go:build windows

package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These need a real non-Store Python: set RELAY_COLLECTOR_PYTHON to e.g. the
// embedded python's python.exe. Without it they are skipped.

func requireSandbox(t *testing.T) string {
	t.Helper()
	py := os.Getenv(PythonEnv)
	if py == "" {
		t.Skipf("%s not set", PythonEnv)
	}
	if err := Available(); err != nil {
		t.Fatalf("Available: %v", err)
	}
	return py
}

// runScript writes collector.py and runs it with "--inputs <InputsDir>" plus extra args.
func runScript(t *testing.T, py, script string, extra ...string) Result {
	t.Helper()
	root := t.TempDir()
	dirs := map[string]string{}
	for _, d := range []string{"script", "in", "scratch"} {
		dirs[d] = filepath.Join(root, d)
		if err := os.MkdirAll(dirs[d], 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dirs["script"], "collector.py"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirs["in"], "data.txt"), []byte("42"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), Spec{
		Python: py, ScriptDir: dirs["script"], InputsDir: dirs["in"], ScratchDir: dirs["scratch"],
		Args:    append([]string{"--inputs", dirs["in"]}, extra...),
		Timeout: 30 * time.Second, MemoryMB: 128,
	})
	if err != nil {
		t.Fatalf("Run: %v (stderr %q)", err, res.Stderr)
	}
	return res
}

func TestValuePrint(t *testing.T) {
	py := requireSandbox(t)
	res := runScript(t, py, `import sys, os, json
inp = sys.argv[sys.argv.index("--inputs") + 1]
v = int(open(os.path.join(inp, "data.txt")).read())
open("scratch.tmp", "w").write("ok")  # cwd = ScratchDir is writable
print(json.dumps({"value": v, "unit": "n", "evidence": []}))
`)
	if res.ExitCode != 0 || !strings.Contains(string(res.Stdout), `"value": 42`) {
		t.Fatalf("exit %d stdout %q stderr %q", res.ExitCode, res.Stdout, res.Stderr)
	}
}

func TestNoNetwork(t *testing.T) {
	py := requireSandbox(t)
	res := runScript(t, py, `import socket
try:
    socket.create_connection(("1.1.1.1", 443), timeout=5).close()
    print("CONNECTED")
except Exception as e:
    print("BLOCKED", repr(e))
`)
	if !strings.HasPrefix(string(res.Stdout), "BLOCKED") {
		t.Fatalf("network reachable from sandbox: %q %q", res.Stdout, res.Stderr)
	}
}

func TestCannotReadSSH(t *testing.T) {
	py := requireSandbox(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	ssh := filepath.Join(home, ".ssh")
	created := false
	if _, err := os.Stat(ssh); os.IsNotExist(err) {
		if err := os.Mkdir(ssh, 0o700); err != nil {
			t.Fatal(err)
		}
		created = true
	}
	secret := filepath.Join(ssh, "relay-sandbox-test-secret")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.Remove(secret)
		if created {
			os.Remove(ssh)
		}
	})
	res := runScript(t, py, `import sys, os
d, f = sys.argv[-2], sys.argv[-1]
leaks = []
try:
    leaks.append("list:" + ",".join(os.listdir(d)))
except Exception:
    pass
try:
    leaks.append("read:" + open(f).read())
except Exception:
    pass
print("LEAK " + ";".join(leaks) if leaks else "DENIED")
`, ssh, secret)
	if strings.TrimSpace(string(res.Stdout)) != "DENIED" {
		t.Fatalf("sandbox read ~/.ssh: %q %q", res.Stdout, res.Stderr)
	}
}

func TestCannotWriteOutsideScratch(t *testing.T) {
	py := requireSandbox(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	targets := []string{
		filepath.Join(home, "relay-sandbox-escape.txt"),
		filepath.Join(os.TempDir(), "relay-sandbox-escape.txt"),
	}
	for _, p := range targets {
		os.Remove(p)
	}
	res := runScript(t, py, `import sys
wrote = []
for p in sys.argv[-2:]:
    try:
        open(p, "w").write("x")
        wrote.append(p)
    except Exception:
        pass
print("WROTE " + ";".join(wrote) if wrote else "DENIED")
`, targets...)
	for _, p := range targets {
		if _, err := os.Stat(p); err == nil {
			os.Remove(p)
			t.Errorf("sandbox created %s", p)
		}
	}
	if strings.TrimSpace(string(res.Stdout)) != "DENIED" {
		t.Fatalf("sandbox wrote outside scratch: %q %q", res.Stdout, res.Stderr)
	}
}

func TestTimeoutKills(t *testing.T) {
	py := requireSandbox(t)
	root := t.TempDir()
	for _, d := range []string{"script", "in", "scratch"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	os.WriteFile(filepath.Join(root, "script", "collector.py"), []byte("while True: pass\n"), 0o644)
	start := time.Now()
	_, err := Run(context.Background(), Spec{
		Python: py, ScriptDir: filepath.Join(root, "script"), InputsDir: filepath.Join(root, "in"),
		ScratchDir: filepath.Join(root, "scratch"), Timeout: 2 * time.Second, MemoryMB: 64,
	})
	if err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("want deadline error, got %v", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("kill took %s", d)
	}
}
