// Command runnerd is the Relay runner: one instance per machine, exposing
// the v1 HTTP API defined in shared/API.md.
package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"os"

	"github.com/mdp/qrterminal/v3"
	"rsc.io/qr"

	"relay/runner/internal/api"
	"relay/runner/internal/compose"
	"relay/runner/internal/config"
	"relay/runner/internal/idle"
	"relay/runner/internal/notify"
	"relay/runner/internal/project"
	"relay/runner/internal/session"
)

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

	// -qr-png <file>: same pairing URI as -qr, written as a scannable PNG
	// instead of terminal blocks - for pairing from a phone that can't point
	// its camera at this machine's console. The file contains this runner's
	// key, so it is written 0600 and must never be committed (.gitignore
	// covers *-pairing-qr.png).
	if len(os.Args) > 2 && os.Args[1] == "-qr-png" {
		writePairingQRPNG(cfg, os.Args[2])
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

	// Idle-suspend (sleep, not poweroff) is opt-in only - see internal/idle's
	// package doc. Never starts unless RELAY_IDLE_SUSPEND_ENABLED=true is
	// explicitly set, so a plain local run (or a container running runnerd
	// without that env var) never tries to suspend its host.
	// beforeShutdown fires a best-effort "going down" push, shared by both
	// the automatic idle-suspend path (idle.Monitor.BeforeShutdown, called
	// right before it invokes Shutdowner.Shutdown itself) and the manual
	// /v1/suspend path below (which calls Shutdowner.Shutdown itself right
	// after).
	beforeShutdown := func() {
		hostname, err := os.Hostname()
		if err != nil {
			hostname = "unknown"
		}
		for _, d := range devices.List() {
			if err := notifier.NotifyRunnerSuspending(d, hostname); err != nil {
				log.Printf("notify device %s of suspend: %v", d.ID, err)
			}
		}
	}

	idleCfg := idle.LoadConfig()
	srv := api.NewServer(cfg.Key, projects, sessions, api.ComposeFuncs{
		Start: compose.Start,
		Stop:  compose.Stop,
	}, devices)

	if idleCfg.Enabled {
		log.Printf("idle: suspend-to-sleep enabled (timeout=%s, check interval=%s)", idleCfg.Timeout, idleCfg.CheckInterval)
		mon := idle.NewMonitor(sessions, idle.DefaultShutdowner, idleCfg)
		mon.BeforeShutdown = beforeShutdown
		go mon.Run()

		// Manual "I'm done, suspend now" from the phone - see
		// api.Server.Suspend's doc comment for why this reuses the same
		// RELAY_IDLE_SUSPEND_ENABLED gate as the automatic path instead of
		// being always available.
		srv.Suspend = func() error {
			beforeShutdown()
			return idle.DefaultShutdowner.Shutdown()
		}
	}

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
// pairingURI builds the relay:// URI both -qr and -qr-png encode, refusing
// addresses the phone could never reach. See printPairingQR.
func pairingURI(cfg *config.Config) string {
	// Refuse to encode an address the phone can never reach. RELAY_LISTEN_ADDR
	// defaults to 127.0.0.1:7777, so running `-qr` without setting it would
	// otherwise print a perfectly valid QR code pointing at the phone's own
	// loopback - the app stores it happily and every later call fails with a
	// connection error that looks like a firewall or Tailscale problem. Fail
	// loudly here instead of handing over a broken pairing.
	host, _, err := net.SplitHostPort(cfg.ListenAddr)
	if err != nil {
		log.Fatalf("qr: cannot parse RELAY_LISTEN_ADDR %q: %v", cfg.ListenAddr, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		log.Fatalf("qr: RELAY_LISTEN_ADDR %q has no specific host - set it to this machine's Tailscale IP (tailscale ip -4) so the phone has something to connect to", cfg.ListenAddr)
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		log.Fatalf("qr: RELAY_LISTEN_ADDR %q is loopback - the phone would try to connect to itself. Set it to this machine's Tailscale IP (tailscale ip -4) and restart the runner before pairing", cfg.ListenAddr)
	}

	return fmt.Sprintf("relay://%s?key=%s", cfg.ListenAddr, cfg.Key)
}

func printPairingQR(cfg *config.Config) {
	uri := pairingURI(cfg)
	fmt.Println("Scan this in the Relay Android app (Add Runner -> Scan QR):")
	fmt.Println()
	qrterminal.GenerateHalfBlock(uri, qrterminal.L, os.Stdout)
	fmt.Println()
	fmt.Printf("(runner address: %s)\n", cfg.ListenAddr)
	// Plain-text fallback: lets a script (or a human) grab the exact pairing
	// URI without needing to decode the terminal QR art above - e.g. to
	// render it as a real scannable image elsewhere.
	fmt.Printf("PAIRING URI: %s\n", uri)
}

// writePairingQRPNG renders the pairing URI to a PNG file. Mode 0600 because
// the QR image encodes this runner's key verbatim - anyone who can read the
// image can pair with this runner.
func writePairingQRPNG(cfg *config.Config, path string) {
	code, err := qr.Encode(pairingURI(cfg), qr.M)
	if err != nil {
		log.Fatalf("qr-png: encode: %v", err)
	}
	if err := os.WriteFile(path, code.PNG(), 0o600); err != nil {
		log.Fatalf("qr-png: write %q: %v", path, err)
	}
	fmt.Printf("Wrote pairing QR to %s (runner address: %s)\n", path, cfg.ListenAddr)
	fmt.Println("This image encodes the runner key - do not commit or share it.")
}
