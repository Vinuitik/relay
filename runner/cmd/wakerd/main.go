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
	"log"
	"net/http"

	"relay/runner/internal/waker"
)

func main() {
	cfg, err := waker.Load()
	if err != nil {
		log.Fatalf("waker: load config: %v", err)
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
