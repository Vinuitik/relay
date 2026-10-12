package vscodeperm

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// AddrFile is written by the runner at startup ($RELAY_HOME/listen-addr) so
// the hook finds the port without sharing the runner's environment.
const AddrFile = "listen-addr"

// HoldSec reads RELAY_PERMISSION_HOLD_SEC (default 120).
func HoldSec() int {
	if n, err := strconv.Atoi(os.Getenv("RELAY_PERMISSION_HOLD_SEC")); err == nil && n > 0 {
		return n
	}
	return DefaultHoldSec
}

// Output is the PermissionRequest hook's stdout JSON for a decision.
func Output(d Decision) []byte {
	dec := map[string]any{"behavior": d.Behavior}
	if d.Behavior == "deny" {
		msg := d.Message
		if msg == "" {
			msg = "Denied from the Relay phone app"
		}
		dec["message"] = msg
		dec["interrupt"] = false
	}
	b, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": "PermissionRequest",
		"decision":      dec,
	}})
	return b
}

// baseURL resolves the local runner: listen-addr file, else RELAY_LISTEN_ADDR,
// else 127.0.0.1:7777. A wildcard/empty host becomes loopback.
func baseURL(home string) string {
	addr := ""
	if b, err := os.ReadFile(filepath.Join(home, AddrFile)); err == nil {
		addr = strings.TrimSpace(string(b))
	}
	if addr == "" {
		addr = os.Getenv("RELAY_LISTEN_ADDR")
	}
	if addr == "" {
		addr = "127.0.0.1:7777"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// readKey reads the runner key READ-ONLY (never creates it).
func readKey(home string) string {
	b, err := os.ReadFile(filepath.Join(home, "key.txt"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// post sends the hook's stdin to a runner path; returns status and body.
func post(home, path string, body []byte, timeout time.Duration) (int, []byte, bool) {
	base, key := baseURL(home), readKey(home)
	if base == "" || key == "" {
		return 0, nil, false
	}
	req, err := http.NewRequest(http.MethodPost, base+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, false
	}
	req.Header.Set("X-Relay-Key", key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return 0, nil, false
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, data, true
}

// RunPermissionHook is `runnerd vscode-hook PermissionRequest`: forwards the
// hook input to the runner and returns the stdout JSON, or nil (print
// nothing) on timeout / any error.
func RunPermissionHook(stdin []byte, home string) []byte {
	if !json.Valid(stdin) {
		return nil
	}
	status, data, ok := post(home, "/v1/vscode/permissions", stdin, time.Duration(HoldSec()+15)*time.Second)
	if !ok || status != http.StatusOK {
		return nil
	}
	var d Decision
	if json.Unmarshal(data, &d) != nil || !d.Valid() {
		return nil
	}
	return Output(d)
}

// PostNotification forwards a Notification hook call (short timeout, best effort).
func PostNotification(stdin []byte, home string) {
	if json.Valid(stdin) {
		post(home, "/v1/vscode/notification", stdin, 3*time.Second)
	}
}
