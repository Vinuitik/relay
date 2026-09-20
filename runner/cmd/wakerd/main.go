// Command wakerd is the Relay wake daemon. It runs on an always-on device
// that sits on the LAN (a Raspberry Pi Zero 2 W over WiFi) and does exactly
// one thing: send Wake-on-LAN magic packets to machines on that LAN, and
// report whether they're up.
//
// It is not a runner. It has no projects, no sessions and no agent CLI - it
// exists because a magic packet is an L2 broadcast, so only a device
// physically on the target's broadcast domain can send one, and every other
// machine on that LAN is asleep.
package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"os"

	"github.com/mdp/qrterminal/v3"
	"rsc.io/qr"

	"relay/runner/internal/waker"
)

func main() {
	cfg, err := waker.Load()
	if err != nil {
		log.Fatalf("waker: load config: %v", err)
	}

	// -qr: print the pairing QR code (address + key) to the terminal and
	// exit, instead of starting the server. The Pi is headless (no screen,
	// no keyboard, ever - see package doc), so this terminal render over SSH
	// is the primary pairing path, not a fallback like it is for runnerd.
	if len(os.Args) > 1 && os.Args[1] == "-qr" {
		printPairingQR(cfg)
		return
	}

	// -qr-png <file>: same pairing URI as -qr, written as a scannable PNG -
	// for pairing from a phone that can't point its camera at an SSH
	// terminal. The file contains this waker's key, so it is written 0600
	// and must never be committed.
	if len(os.Args) > 2 && os.Args[1] == "-qr-png" {
		writePairingQRPNG(cfg, os.Args[2])
		return
	}

	if cfg.FirstRun {
		log.Printf("waker: created %s", cfg.ConfigFile)
		log.Printf("waker: key (shown once): %s", cfg.Key)
		log.Printf("waker: add machines by hand to %s, then restart", cfg.ConfigFile)
	}
	log.Printf("waker: %d machine(s) configured", len(cfg.Machines))
	for _, m := range cfg.Machines {
		log.Printf("waker:   %s (%s) mac=%s probe=%s:%d", m.ID, m.Name, m.MAC, m.Host(), m.Port())
	}

	srv := waker.NewServer(cfg)
	log.Printf("waker: listening on %s", cfg.ListenAddr)
	if err := http.ListenAndServe(cfg.ListenAddr, srv.Routes()); err != nil {
		log.Fatalf("waker: server: %v", err)
	}
}

// pairingURI builds the relaywaker:// URI both -qr and -qr-png encode,
// refusing addresses the phone could never reach. Distinct scheme from the
// runner's relay:// so the phone app can tell a Pi apart from a runner
// before it even connects. See runnerd's own pairingURI, which this mirrors.
func pairingURI(cfg *waker.Config) string {
	// Refuse to encode an address the phone can never reach. WAKER_LISTEN_ADDR
	// defaults to 127.0.0.1:7778, so running `-qr` without setting it would
	// otherwise print a QR code pointing at the phone's own loopback. Fail
	// loudly here instead of handing over a broken pairing.
	host, _, err := net.SplitHostPort(cfg.ListenAddr)
	if err != nil {
		log.Fatalf("qr: cannot parse WAKER_LISTEN_ADDR %q: %v", cfg.ListenAddr, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		log.Fatalf("qr: WAKER_LISTEN_ADDR %q has no specific host - set it to this Pi's Tailscale IP (tailscale ip -4) so the phone has something to connect to", cfg.ListenAddr)
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		log.Fatalf("qr: WAKER_LISTEN_ADDR %q is loopback - the phone would try to connect to itself. Set it to this Pi's Tailscale IP (tailscale ip -4) and restart wakerd before pairing", cfg.ListenAddr)
	}

	return fmt.Sprintf("relaywaker://%s?key=%s", cfg.ListenAddr, cfg.Key)
}

func printPairingQR(cfg *waker.Config) {
	uri := pairingURI(cfg)
	fmt.Println("Scan this in the Relay Android app (Add Waker -> Scan QR):")
	fmt.Println()
	qrterminal.GenerateHalfBlock(uri, qrterminal.L, os.Stdout)
	fmt.Println()
	fmt.Printf("(waker address: %s)\n", cfg.ListenAddr)
	// Plain-text fallback: lets a script (or a human) grab the exact pairing
	// URI without needing to decode the terminal QR art above.
	fmt.Printf("PAIRING URI: %s\n", uri)
}

// writePairingQRPNG renders the pairing URI to a PNG file. Mode 0600 because
// the QR image encodes this waker's key verbatim - anyone who can read the
// image can wake machines through this daemon.
func writePairingQRPNG(cfg *waker.Config, path string) {
	code, err := qr.Encode(pairingURI(cfg), qr.M)
	if err != nil {
		log.Fatalf("qr-png: encode: %v", err)
	}
	if err := os.WriteFile(path, code.PNG(), 0o600); err != nil {
		log.Fatalf("qr-png: write %q: %v", path, err)
	}
	fmt.Printf("Wrote pairing QR to %s (waker address: %s)\n", path, cfg.ListenAddr)
	fmt.Println("This image encodes the waker key - do not commit or share it.")
}
