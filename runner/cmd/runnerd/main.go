// Command runnerd is the Relay runner: one instance per machine, exposing
// the v1 HTTP API defined in shared/API.md.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/mdp/qrterminal/v3"

	"relay/runner/internal/api"
	"relay/runner/internal/compose"
	"relay/runner/internal/config"
	"relay/runner/internal/history"
	"relay/runner/internal/idle"
	"relay/runner/internal/notify"
	"relay/runner/internal/project"
	"relay/runner/internal/session"
	"relay/runner/internal/wol"
)

// historyPurgeInterval controls how often the weekly-cleanup ticker fires.
// Deliberately not exposed via flag/env for this milestone - a single fixed
// interval is enough; the retention window itself (history.DefaultMaxAge)
// is the value ARCHITECTURE.md actually calls out as configurable.
const historyPurgeInterval = 1 * time.Hour

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	// -qr: print the pairing QR code (address + key) to the terminal and
	// exit, instead of starting the server. Lets a headless/CLI-only
	// server (see runner/FLOWS.md "Install bootstrap") hand its key to the
	// phone app via camera scan instead of copy-pasting 64 hex chars.
	if len(os.Args) > 1 && os.Args[1] == "-qr" {
		printPairingQR(cfg)
		return
	}

	if cfg.FirstRun {
		log.Printf("first run: generated new key, printed once below - copy it into the phone app")
		log.Printf("RELAY KEY: %s", cfg.Key)
	}

	projects, err := project.NewRegistry(cfg.ProjectsRoot, cfg.ProjectsFile)
	if err != nil {
		log.Fatalf("load project registry: %v", err)
	}

	sessions := session.NewManager(projects.Dir)

	devices := notify.NewRegistry()
	notifier := notify.NewNotifier(os.Getenv("RELAY_FCM_CREDENTIALS"))
	sessions.OnFinished = func(sess session.Session) {
		for _, d := range devices.List() {
			if err := notifier.NotifySessionFinished(d, notify.Session{ID: sess.ID, ProjectID: sess.ProjectID}); err != nil {
				log.Printf("notify device %s of session %s finish: %v", d.ID, sess.ID, err)
			}
		}
	}

	go runHistoryPurge(sessions)

	// Idle-suspend (S5) is opt-in only - see internal/idle's package doc.
	// Never starts unless RELAY_IDLE_SUSPEND_ENABLED=true is explicitly set,
	// so a plain local run (or a container running runnerd without that env
	// var) never tries to power its host off.
	idleCfg := idle.LoadConfig()
	if idleCfg.Enabled {
		log.Printf("idle: suspend-to-S5 enabled (timeout=%s, check interval=%s)", idleCfg.Timeout, idleCfg.CheckInterval)
		mon := idle.NewMonitor(sessions, idle.DefaultShutdowner, idleCfg)
		go mon.Run()
	}

	srv := api.NewServer(cfg.Key, projects, sessions, api.ComposeFuncs{
		Start: compose.Start,
		Stop:  compose.Stop,
	}, devices, wol.DefaultSender)

	// NOTE: cfg.ListenAddr defaults to loopback for local dev/tests. A
	// production deployment must bind only to the Tailscale interface,
	// never 0.0.0.0, per ARCHITECTURE.md's "Registration / connection"
	// section - set RELAY_LISTEN_ADDR to that interface's address when
	// deploying; enforcing that binding choice is outside this milestone.
	log.Printf("runner listening on %s (projects root: %s)", cfg.ListenAddr, cfg.ProjectsRoot)
	if err := http.ListenAndServe(cfg.ListenAddr, srv.Routes()); err != nil {
		log.Fatalf("serve: %v", err)
	}
}

// printPairingQR renders a relay://host:port?key=... QR code to stdout as
// terminal Unicode blocks - no image, no display server, just the SSH
// session's own terminal. cfg.ListenAddr is whatever RELAY_LISTEN_ADDR
// resolved to (install.sh sets it to the Tailscale IP once logged in); if
// Tailscale isn't up yet this still prints, just with the useless loopback
// default - the caller (install.sh) only invokes -qr after Tailscale login
// succeeds, so that's a manual `relay-runner -qr` misuse case, not a normal
// path.
func printPairingQR(cfg *config.Config) {
	uri := fmt.Sprintf("relay://%s?key=%s", cfg.ListenAddr, cfg.Key)
	fmt.Println("Scan this in the Relay Android app (Add Runner -> Scan QR):")
	fmt.Println()
	qrterminal.GenerateHalfBlock(uri, qrterminal.L, os.Stdout)
	fmt.Println()
	fmt.Printf("(runner address: %s)\n", cfg.ListenAddr)
}

// runHistoryPurge periodically drops finished/error sessions older than
// history.DefaultMaxAge, per ARCHITECTURE.md's weekly history cleanup.
func runHistoryPurge(sessions *session.Manager) {
	ticker := time.NewTicker(historyPurgeInterval)
	defer ticker.Stop()
	for range ticker.C {
		cutoff := time.Now().Add(-history.DefaultMaxAge)
		if n := sessions.PurgeFinishedBefore(cutoff); n > 0 {
			log.Printf("history: purged %d session(s) older than %s", n, history.DefaultMaxAge)
		}
	}
}
