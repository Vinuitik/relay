// Command keeperd keeps the Relay runner running and up to date. It is the
// process started at boot (systemd on Linux, an at-startup scheduled task
// on Windows); it starts relay-runner as its child, restarts it if it dies,
// and swaps in new releases while the runner is idle. See
// internal/keeper and runner/FLOWS.md "keeperd".
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"relay/runner/internal/activity"
	"relay/runner/internal/config"
	"relay/runner/internal/keeper"
)

// version is this binary's release tag, stamped by the release workflow
// with -ldflags "-X main.version=...". "dev" builds never self-update.
var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "-version" {
		fmt.Println(version)
		return
	}
	cfg := keeper.LoadConfig()
	watchInput := len(os.Args) > 1 && os.Args[1] == "-watch-input"
	// Optional settings file in the install dir (the Windows task can't set
	// env vars); then re-read config in case it set RELAY_UPDATE_* too.
	envFile := filepath.Join(cfg.Dir, "relay.env")
	if err := keeper.LoadEnvFile(envFile); err != nil {
		log.Printf("keeper: read %s: %v", envFile, err)
	}
	cfg = keeper.LoadConfig()
	if runtime.GOOS == "windows" {
		// Started hidden by Task Scheduler: no console, so keeperd's and the
		// runner's output go to a file (Linux has the systemd journal).
		name := "keeper.log"
		if watchInput {
			name = "input-watcher.log"
		}
		logToFile(filepath.Join(cfg.Dir, name))
	}
	// Same config the runner child will load: its address and key are how
	// keeperd asks it whether it's busy.
	rcfg, err := config.Load()
	if err != nil {
		log.Fatalf("keeper: load runner config: %v", err)
	}
	if watchInput {
		watchLocalInput(rcfg, cfg.KeeperPath())
		return
	}
	log.Printf("keeper %s: dir %s, runner at %s", version, cfg.Dir, rcfg.ListenAddr)

	env := os.Environ()
	if os.Getenv("RELAY_ACP_CLAUDE") == "" {
		// The adapter keeperd manages, unless one was configured explicitly.
		env = append(env, "RELAY_ACP_CLAUDE="+cfg.AdapterBin())
	}
	sup := &keeper.Supervisor{Path: cfg.RunnerPath(), Env: env}

	client := &http.Client{Timeout: 60 * time.Second}
	base := "http://" + rcfg.ListenAddr
	upd := &keeper.Updater{
		Cfg:        cfg,
		Version:    version,
		Supervisor: sup,
		HTTP:       client,
		APIBase:    "https://api.github.com",
		Now:        time.Now,
		Relaunch:   keeper.RelaunchSelf,
		Health: func() bool {
			resp, err := client.Get(base + "/v1/health")
			if err != nil {
				return false
			}
			resp.Body.Close()
			return resp.StatusCode == http.StatusOK
		},
		Status: func() (keeper.RunnerStatus, error) {
			var st keeper.RunnerStatus
			req, _ := http.NewRequest(http.MethodGet, base+"/v1/runner/info", nil)
			req.Header.Set("X-Relay-Key", rcfg.Key)
			resp, err := client.Do(req)
			if err != nil {
				return st, err
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return st, fmt.Errorf("runner info: %s", resp.Status)
			}
			return st, json.NewDecoder(resp.Body).Decode(&st)
		},
	}

	if !cfg.Disabled {
		upd.Bootstrap()
	}
	sup.Start()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)

	next := 2 * time.Minute // first check soon after boot
	for {
		if cfg.Disabled {
			next = 24 * time.Hour
		}
		select {
		case sig := <-sigs:
			log.Printf("keeper: %v, stopping runner", sig)
			sup.Stop()
			return
		case <-time.After(next):
		}
		if cfg.Disabled {
			continue
		}
		next = cfg.CheckEvery
		if err := upd.Check(); err != nil {
			if errors.Is(err, keeper.ErrNotIdle) {
				next = 3 * time.Minute // retry soon once work is done; each Check calls the GitHub API (60/h unauthenticated, shared per public IP)
			} else {
				log.Printf("keeper: update check: %v", err)
			}
		}
	}
}

// logToFile sends log output and the runner child's stdout/stderr to path,
// starting the file afresh once it passes 10MB.
func logToFile(path string) {
	if fi, err := os.Stat(path); err == nil && fi.Size() > 10<<20 {
		_ = os.Rename(path, path+".1")
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	log.SetOutput(f)
	os.Stdout = f
	os.Stderr = f
}

// watchLocalInput is `keeperd -watch-input`, run at logon inside the desktop
// session. keeperd itself starts at boot in a background session where
// Windows hides the desktop's keyboard/mouse activity, so the runner can't
// tell someone is using the machine; this reports it for it, as the same
// POST /v1/activity ping the phone app sends while open - keeping
// idle-suspend from sleeping the laptop under its user.
//
// When an update replaces keeperd's binary the watcher restarts itself from
// the new one: otherwise it keeps the old file (renamed .old) open forever
// and the next update can't clear it - see keeper.clearOld.
func watchLocalInput(rcfg *config.Config, keeperPath string) {
	client := &http.Client{Timeout: 10 * time.Second}
	url := "http://" + rcfg.ListenAddr + "/v1/activity"
	started, _ := os.Stat(keeperPath)
	for {
		if now, err := os.Stat(keeperPath); err == nil && started != nil &&
			(!now.ModTime().Equal(started.ModTime()) || now.Size() != started.Size()) {
			log.Printf("input watcher: keeperd was updated, restarting from it")
			if err := keeper.RelaunchWatcher(keeperPath); err != nil {
				log.Printf("input watcher: restart: %v", err)
			}
		}
		if idle, err := activity.LocalIdleTime(); err == nil && idle < time.Minute {
			req, _ := http.NewRequest(http.MethodPost, url, nil)
			req.Header.Set("X-Relay-Key", rcfg.Key)
			if resp, err := client.Do(req); err == nil {
				resp.Body.Close()
			}
		} else if err != nil {
			log.Printf("input watcher: %v", err)
			return
		}
		time.Sleep(30 * time.Second)
	}
}
