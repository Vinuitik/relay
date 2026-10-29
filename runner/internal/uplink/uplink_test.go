package uplink

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

const testKey = "runner-key"
const testToken = "device-token"

// runnerHandler stands in for api.Server.Routes(): it enforces X-Relay-Key
// like the real auth middleware and offers a few test endpoints.
func runnerHandler(slowGate chan struct{}) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Relay-Key") != testKey {
			http.Error(w, "bad key", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1/echo":
			b, _ := io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprintf(w, `{"method":%q,"q":%q,"body":%s}`, r.Method, r.URL.Query().Get("x"), b)
		case "/v1/huge":
			w.Write([]byte(strings.Repeat("a", MaxBody+1)))
		case "/v1/slow":
			<-slowGate
			w.Write([]byte("slow"))
		case "/v1/fast":
			close(slowGate)
			w.Write([]byte("fast"))
		default:
			http.NotFound(w, r)
		}
	})
	return mux
}

// fakeHub accepts uplinks; onConn runs per connection with its index.
type fakeHub struct {
	srv     *httptest.Server
	conns   atomic.Int32
	authBad atomic.Int32
}

func newFakeHub(t *testing.T, onConn func(n int, c *websocket.Conn)) *fakeHub {
	h := &fakeHub{}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/relay/uplink" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			h.authBad.Add(1)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		c.SetReadLimit(64 << 20)
		n := int(h.conns.Add(1))
		onConn(n, c)
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func startClient(t *testing.T, hub string, token string, logf func(string, ...any)) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{
		Config:     Config{Hub: hub, DeviceID: "dev1", Token: token},
		Handler:    runnerHandler(make(chan struct{})),
		Key:        testKey,
		MinBackoff: 10 * time.Millisecond,
		MaxBackoff: 50 * time.Millisecond,
		Logf:       logf,
	}
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return cancel
}

func sendReq(t *testing.T, c *websocket.Conn, f Frame) {
	t.Helper()
	f.Type = "req"
	b, _ := json.Marshal(f)
	if err := c.Write(context.Background(), websocket.MessageText, b); err != nil {
		t.Errorf("hub write: %v", err)
	}
}

func readRes(t *testing.T, c *websocket.Conn) Frame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, b, err := c.Read(ctx)
	if err != nil {
		t.Errorf("hub read: %v", err)
		return Frame{}
	}
	var f Frame
	if err := json.Unmarshal(b, &f); err != nil {
		t.Errorf("bad res frame %s: %v", b, err)
	}
	if f.Type != "res" {
		t.Errorf("frame type = %q, want res", f.Type)
	}
	return f
}

func TestRequestsRoundTrip(t *testing.T) {
	got := make(chan map[string]Frame, 1)
	hub := newFakeHub(t, func(n int, c *websocket.Conn) {
		sendReq(t, c, Frame{ID: "echo", Method: "POST", Path: "/v1/echo?x=1", Body: `{"text":"hi"}`})
		sendReq(t, c, Frame{ID: "notv1", Method: "GET", Path: "/relay/secret"})
		sendReq(t, c, Frame{ID: "dotdot", Method: "GET", Path: "/v1/../admin"})
		sendReq(t, c, Frame{ID: "big", Method: "POST", Path: "/v1/echo", Body: strings.Repeat("a", MaxBody+1)})
		sendReq(t, c, Frame{ID: "huge", Method: "GET", Path: "/v1/huge"})
		m := map[string]Frame{}
		for i := 0; i < 5; i++ {
			f := readRes(t, c)
			m[f.ID] = f
		}
		got <- m
		c.Close(websocket.StatusNormalClosure, "")
	})
	startClient(t, hub.srv.URL, testToken, t.Logf)

	var m map[string]Frame
	select {
	case m = <-got:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for responses")
	}
	e := m["echo"]
	if e.Status != 202 || e.ContentType != "application/json" || e.Body != `{"method":"POST","q":"1","body":{"text":"hi"}}` {
		t.Errorf("echo res = %+v", e)
	}
	for _, id := range []string{"notv1", "dotdot"} {
		if m[id].Status != 404 {
			t.Errorf("%s status = %d, want 404", id, m[id].Status)
		}
	}
	for _, id := range []string{"big", "huge"} {
		if m[id].Status != 413 {
			t.Errorf("%s status = %d, want 413", id, m[id].Status)
		}
	}
}

func TestRequestsHandledConcurrently(t *testing.T) {
	got := make(chan []string, 1)
	hub := newFakeHub(t, func(n int, c *websocket.Conn) {
		// slow blocks until fast runs: only passes if dispatch is concurrent.
		sendReq(t, c, Frame{ID: "slow", Path: "/v1/slow"})
		sendReq(t, c, Frame{ID: "fast", Path: "/v1/fast"})
		got <- []string{readRes(t, c).ID, readRes(t, c).ID}
	})
	startClient(t, hub.srv.URL, testToken, t.Logf)
	select {
	case ids := <-got:
		if ids[0] != "fast" || ids[1] != "slow" {
			t.Errorf("order = %v, want [fast slow]", ids)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out: requests not handled concurrently")
	}
}

func TestReconnectAfterHubDrops(t *testing.T) {
	got := make(chan Frame, 1)
	hub := newFakeHub(t, func(n int, c *websocket.Conn) {
		if n == 1 {
			c.CloseNow() // hub restart / replaced socket
			return
		}
		sendReq(t, c, Frame{ID: "after", Method: "POST", Path: "/v1/echo", Body: `1`})
		got <- readRes(t, c)
	})
	startClient(t, hub.srv.URL, testToken, t.Logf)
	select {
	case f := <-got:
		if f.ID != "after" || f.Status != 202 {
			t.Errorf("res after reconnect = %+v", f)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runner did not reconnect")
	}
	if n := hub.conns.Load(); n < 2 {
		t.Errorf("connections = %d, want >= 2", n)
	}
}

func TestUnauthorizedLogsAndBacksOffAtCap(t *testing.T) {
	hub := newFakeHub(t, func(int, *websocket.Conn) { t.Error("must not upgrade with a bad token") })
	var mu sync.Mutex
	var logs []string
	cancel := startClient(t, hub.srv.URL, "wrong", func(f string, a ...any) {
		mu.Lock()
		logs = append(logs, fmt.Sprintf(f, a...))
		mu.Unlock()
	})
	time.Sleep(180 * time.Millisecond)
	cancel()
	mu.Lock()
	defer mu.Unlock()
	if len(logs) == 0 || !strings.Contains(logs[0], "device revoked or token invalid") || !strings.Contains(logs[0], "re-pair") {
		t.Errorf("logs = %q", logs)
	}
	// At the 50ms cap, 180ms allows ~4 attempts; 10ms doubling would allow more.
	if n := hub.authBad.Load(); n < 2 || n > 5 {
		t.Errorf("401 attempts = %d, want 2..5 (backoff at cap)", n)
	}
}

func TestUplinkURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://hub.example.com":   "wss://hub.example.com/relay/uplink",
		"https://hub.example.com/":  "wss://hub.example.com/relay/uplink",
		"http://localhost:8080":     "ws://localhost:8080/relay/uplink",
		"https://h.example/prefix/": "wss://h.example/prefix/relay/uplink",
	} {
		if got := uplinkURL(in); got != want {
			t.Errorf("uplinkURL(%q) = %q, want %q", in, got, want)
		}
	}
}
