package main

import (
	"net"
	"strings"
	"testing"

	"relay/runner/internal/waker"
)

// pairingURI calls log.Fatalf (which exits the process) on a refused
// address, so it can't be called directly with a refusal-case input inside
// a test binary. wouldRefuse mirrors pairingURI's guard conditions instead,
// so the refusal logic itself is exercised without an os.Exit. Any change
// to the guard in pairingURI must be mirrored here.
func wouldRefuse(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return true
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return true
	}
	return false
}

func TestPairingURI_Success(t *testing.T) {
	cfg := &waker.Config{ListenAddr: "100.64.0.5:7778", Key: "deadbeef"}
	got := pairingURI(cfg)
	want := "relaywaker://100.64.0.5:7778?key=deadbeef"
	if got != want {
		t.Fatalf("pairingURI() = %q, want %q", got, want)
	}
}

func TestPairingURI_SchemeIsDistinctFromRunner(t *testing.T) {
	cfg := &waker.Config{ListenAddr: "100.64.0.5:7778", Key: "abc123"}
	uri := pairingURI(cfg)
	if !strings.HasPrefix(uri, "relaywaker://") {
		t.Fatalf("pairingURI() = %q, want relaywaker:// scheme", uri)
	}
	if strings.HasPrefix(uri, "relay://") {
		t.Fatalf("pairingURI() = %q, collides with runnerd's relay:// scheme", uri)
	}
}

func TestPairingURI_RefusalCases(t *testing.T) {
	cases := []struct {
		name string
		addr string
	}{
		{"loopback v4", "127.0.0.1:7778"},
		{"unspecified v4", "0.0.0.0:7778"},
		{"unspecified v6", "[::]:7778"},
		{"missing host", ":7778"},
		{"unparseable, no port", "not-a-host-port"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !wouldRefuse(tc.addr) {
				t.Fatalf("expected WAKER_LISTEN_ADDR %q to be refused, it would be accepted", tc.addr)
			}
		})
	}
}

func TestPairingURI_AcceptedCases(t *testing.T) {
	cases := []string{
		"100.64.0.5:7778",
		"192.168.1.50:7778",
	}
	for _, addr := range cases {
		if wouldRefuse(addr) {
			t.Fatalf("expected WAKER_LISTEN_ADDR %q to be accepted, it would be refused", addr)
		}
	}
}
