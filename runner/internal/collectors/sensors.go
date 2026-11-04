package collectors

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ActivityWatch release the installer fetches (official GitHub release).
const AWVersion = "0.14.1"

var (
	awLinuxZip     = fmt.Sprintf("https://github.com/ActivityWatch/activitywatch/releases/download/v%[1]s/activitywatch-v%[1]s-linux-x86_64.zip", AWVersion)
	awWinSetup     = fmt.Sprintf("https://github.com/ActivityWatch/activitywatch/releases/download/v%[1]s/activitywatch-v%[1]s-windows-x86_64-setup.exe", AWVersion)
	ErrBusy        = errors.New("an install is already running")
	ErrUnsupported = errors.New("not supported on this OS")
)

// SensorsState is GET /v1/sensors.
type SensorsState struct {
	ActivityWatch AWState      `json:"activitywatch"`
	Sandbox       SandboxState `json:"sandbox"`
	Operation     string       `json:"operation"` // "" | "installing"
}

type AWState struct {
	Installed bool     `json:"installed"`
	Running   bool     `json:"running"`
	Buckets   []string `json:"buckets"`
	// Error is the last install's error (or note), cleared by the next install.
	Error string `json:"error,omitempty"`
}

type SandboxState struct {
	Available bool   `json:"available"`
	Kind      string `json:"kind"`
	Error     string `json:"error,omitempty"`
}

// Sensors reports and installs the local data sources collectors read.
type Sensors struct {
	Engine *Engine // AW base URL, HTTP client, sandbox
	Dir    string  // install dir: <dataDir>/sensors
	// Install does the actual install (tests replace it); default
	// InstallActivityWatch.
	Install func(ctx context.Context, dir string) error

	mu        sync.Mutex
	operation string
	lastErr   string
}

func NewSensors(e *Engine) *Sensors {
	return &Sensors{Engine: e, Dir: filepath.Join(e.DataDir, "sensors"), Install: InstallActivityWatch}
}

func (s *Sensors) State(ctx context.Context) SensorsState {
	s.Engine.init()
	st := SensorsState{ActivityWatch: AWState{Buckets: []string{}}, Sandbox: SandboxState{Kind: s.Engine.Sandbox.Kind()}}
	s.mu.Lock()
	st.Operation, st.ActivityWatch.Error = s.operation, s.lastErr
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if ids, err := s.Engine.awBuckets(ctx); err == nil {
		st.ActivityWatch.Running, st.ActivityWatch.Installed, st.ActivityWatch.Buckets = true, true, ids
	} else {
		st.ActivityWatch.Installed = awBinary(s.Dir) != ""
	}
	if err := s.Engine.Sandbox.Available(); err != nil {
		st.Sandbox.Error = err.Error()
	} else {
		st.Sandbox.Available = true
	}
	return st
}

// StartInstall begins an async install; poll State until Operation == "".
func (s *Sensors) StartInstall(name string) error {
	if name != "activitywatch" {
		return badf(`unknown sensor %q (only "activitywatch")`, name)
	}
	if err := installSupported(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.operation != "" {
		return ErrBusy
	}
	s.operation, s.lastErr = "installing", ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		err := s.Install(ctx, s.Dir)
		s.mu.Lock()
		s.operation = ""
		if err != nil {
			s.lastErr = err.Error()
		}
		s.mu.Unlock()
	}()
	return nil
}

func installSupported() error {
	switch {
	case runtime.GOOS == "linux" && runtime.GOARCH == "amd64", runtime.GOOS == "windows":
		return nil
	default:
		return fmt.Errorf("%w: ActivityWatch install on %s/%s (install it by hand from activitywatch.net)", ErrUnsupported, runtime.GOOS, runtime.GOARCH)
	}
}

// awBinary finds an installed aw-qt (ours first), "" if none.
func awBinary(dir string) string {
	exe := "aw-qt"
	if runtime.GOOS == "windows" {
		exe = "aw-qt.exe"
	}
	cands := []string{filepath.Join(dir, "activitywatch", "activitywatch", exe)}
	if runtime.GOOS == "windows" {
		for _, base := range []string{os.Getenv("LOCALAPPDATA") + `\Programs`, os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)")} {
			if base != "" && base != `\Programs` {
				cands = append(cands, filepath.Join(base, "ActivityWatch", exe))
			}
		}
	}
	for _, c := range cands {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			return c
		}
	}
	for _, n := range []string{"aw-qt", "aw-server", "aw-server-rust"} {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	return ""
}

// InstallActivityWatch installs ActivityWatch for the current user, sets
// it to start at login and starts it. Linux: official release zip →
// <dir>/activitywatch + ~/.config/autostart entry. Windows: winget, else
// the official setup.exe silently + HKCU Run key.
func InstallActivityWatch(ctx context.Context, dir string) error {
	if err := installSupported(); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return installWindows(ctx, dir)
	}
	return installLinux(ctx, dir)
}

func download(ctx context.Context, url, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, io.LimitReader(resp.Body, 500<<20))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func installLinux(ctx context.Context, dir string) error {
	zipPath := filepath.Join(dir, "activitywatch.zip")
	defer os.Remove(zipPath)
	if err := download(ctx, awLinuxZip, zipPath); err != nil {
		return err
	}
	dest := filepath.Join(dir, "activitywatch")
	tmp := dest + ".new"
	_ = os.RemoveAll(tmp)
	if err := unzip(zipPath, tmp); err != nil {
		return fmt.Errorf("unpack ActivityWatch: %w", err)
	}
	_ = os.RemoveAll(dest)
	if err := os.Rename(tmp, dest); err != nil {
		return err
	}
	bin := filepath.Join(dest, "activitywatch", "aw-qt")
	if _, err := os.Stat(bin); err != nil {
		return fmt.Errorf("ActivityWatch zip has no activitywatch/aw-qt: %w", err)
	}
	if err := writeAutostart(bin); err != nil {
		return fmt.Errorf("autostart: %w", err)
	}
	// aw-qt's watchers need the desktop session; a runner started outside
	// it (systemd service, ssh) can't start them now.
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return errors.New("installed; it starts at your next desktop login (the runner has no display to start it now)")
	}
	return startDetached(bin)
}

// writeAutostart adds an XDG autostart entry (runs inside the graphical
// session, which aw-qt's window watcher needs - a systemd user unit
// wouldn't have DISPLAY).
func writeAutostart(bin string) error {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	d := filepath.Join(cfg, "autostart")
	if err := os.MkdirAll(d, 0o755); err != nil {
		return err
	}
	entry := "[Desktop Entry]\nType=Application\nName=ActivityWatch\nComment=Installed by Relay runner (KPI sensors)\n" +
		"Exec=\"" + bin + "\"\nX-GNOME-Autostart-enabled=true\nTerminal=false\n"
	return os.WriteFile(filepath.Join(d, "relay-activitywatch.desktop"), []byte(entry), 0o644)
}

func startDetached(bin string, args ...string) error {
	cmd := exec.Command(bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", bin, err)
	}
	go cmd.Wait() //nolint:errcheck // reap; it runs for the session
	return nil
}

func unzip(src, dst string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	root := filepath.Clean(dst) + string(os.PathSeparator)
	for _, f := range r.File {
		p := filepath.Join(dst, f.Name)
		if !strings.HasPrefix(p, root) {
			return fmt.Errorf("zip entry %q escapes the target", f.Name)
		}
		mode := f.Mode()
		switch {
		case mode.IsDir():
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
			continue
		case mode&os.ModeSymlink != 0:
			rc, err := f.Open()
			if err != nil {
				return err
			}
			t, _ := io.ReadAll(io.LimitReader(rc, 4096))
			rc.Close()
			target := string(t)
			if filepath.IsAbs(target) || !strings.HasPrefix(filepath.Join(filepath.Dir(p), target), root) {
				return fmt.Errorf("zip symlink %q escapes the target", f.Name)
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(target, p); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(p, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode.Perm()|0o600)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		rc.Close()
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func installWindows(ctx context.Context, dir string) error {
	if wg, err := exec.LookPath("winget"); err == nil {
		out, err := exec.CommandContext(ctx, wg, "install", "-e", "--id", "ActivityWatch.ActivityWatch",
			"--silent", "--accept-package-agreements", "--accept-source-agreements", "--disable-interactivity").CombinedOutput()
		// winget exits non-zero when the package is already installed; the
		// presence check below decides.
		if err != nil && awBinary(dir) == "" {
			return fmt.Errorf("winget install failed: %v: %s", err, tail(out, 500))
		}
	} else {
		setup := filepath.Join(dir, "activitywatch-setup.exe")
		defer os.Remove(setup)
		if err := download(ctx, awWinSetup, setup); err != nil {
			return err
		}
		// Inno Setup silent flags; /CURRENTUSER avoids an elevation prompt.
		if out, err := exec.CommandContext(ctx, setup, "/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART", "/CURRENTUSER").CombinedOutput(); err != nil {
			return fmt.Errorf("ActivityWatch setup failed: %v: %s", err, tail(out, 500))
		}
	}
	bin := awBinary(dir)
	if bin == "" {
		return errors.New("ActivityWatch installed but aw-qt.exe was not found")
	}
	if out, err := exec.CommandContext(ctx, "reg", "add", `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`,
		"/v", "ActivityWatch", "/t", "REG_SZ", "/d", `"`+bin+`"`, "/f").CombinedOutput(); err != nil {
		return fmt.Errorf("autostart (HKCU Run): %v: %s", err, tail(out, 300))
	}
	return startDetached(bin)
}
