package uplink

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	// MaxBody caps a request or response body (shared/HUB.md: 4 MiB each way).
	MaxBody = 4 << 20
	// readLimit caps one incoming frame: a 4 MiB body JSON-escaped can grow
	// up to 6x (\u00XX), plus the envelope. Bigger frames close the socket.
	readLimit = 6*MaxBody + 64<<10
	// maxFrameOut is the hub's frame limit (8 MiB + 64 KiB, Spring side). A response
	// frame above it would make the hub close the whole uplink, so it is replaced
	// by a 502 instead.
	maxFrameOut = 8<<20 + 64<<10
	// requestTimeout bounds one dispatched request; the hub gives up at 30s.
	requestTimeout = 60 * time.Second
)

// Frame is one JSON text message on the uplink, both directions.
type Frame struct {
	Type        string `json:"type"`
	ID          string `json:"id"`
	Method      string `json:"method,omitempty"`
	Path        string `json:"path,omitempty"`
	Status      int    `json:"status,omitempty"`
	ContentType string `json:"contentType,omitempty"`
	Body        string `json:"body"`
}

// Client keeps one uplink alive: dial, serve, reconnect, forever.
type Client struct {
	Config  Config
	Handler http.Handler // the runner's api.Server.Routes()
	Key     string       // the runner's own key, added as X-Relay-Key

	MinBackoff time.Duration // default 1s
	MaxBackoff time.Duration // default 60s
	HTTPClient *http.Client  // optional, for the handshake
	Logf       func(format string, args ...any)
}

func (c *Client) logf(format string, args ...any) {
	if c.Logf != nil {
		c.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// Run dials the hub and serves requests until ctx is done, reconnecting
// with exponential backoff (MinBackoff doubling to MaxBackoff). A 401 on
// the handshake means the device was revoked or the token is wrong - it is
// logged loudly and retried at MaxBackoff.
func (c *Client) Run(ctx context.Context) {
	minB, maxB := c.MinBackoff, c.MaxBackoff
	if minB <= 0 {
		minB = time.Second
	}
	if maxB <= 0 {
		maxB = 60 * time.Second
	}
	backoff := minB
	for ctx.Err() == nil {
		connected, err := c.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		var wait time.Duration
		if errors.Is(err, errUnauthorized) {
			c.logf("uplink: hub said 401 - device revoked or token invalid - re-pair with `runnerd pair` (retrying every %s)", maxB)
			wait, backoff = maxB, maxB
		} else {
			if connected {
				backoff = minB // a session was established; start over
			}
			c.logf("uplink: %v (reconnecting in %s)", err, backoff)
			wait = backoff
			backoff *= 2
			if backoff > maxB {
				backoff = maxB
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

var errUnauthorized = errors.New("unauthorized")

// runOnce performs one connection lifetime. connected reports whether the
// WebSocket handshake succeeded.
func (c *Client) runOnce(ctx context.Context) (connected bool, err error) {
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+c.Config.Token)
	conn, resp, err := websocket.Dial(ctx, uplinkURL(c.Config.Hub), &websocket.DialOptions{
		HTTPClient: c.HTTPClient,
		HTTPHeader: hdr,
	})
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return false, errUnauthorized
		}
		return false, err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(readLimit)
	c.logf("uplink: connected to %s as device %s", c.Config.Hub, c.Config.DeviceID)

	connCtx, cancel := context.WithCancel(ctx)
	var writeMu sync.Mutex
	var wg sync.WaitGroup
	defer wg.Wait() // runs after cancel below: in-flight handlers see ctx done
	defer cancel()

	for {
		// Read also answers the hub's pings (coder/websocket handles control
		// frames inside Read).
		typ, data, err := conn.Read(connCtx)
		if err != nil {
			return true, err
		}
		if typ != websocket.MessageText {
			continue
		}
		var f Frame
		if err := json.Unmarshal(data, &f); err != nil || f.Type != "req" || f.ID == "" {
			c.logf("uplink: ignoring malformed frame")
			continue
		}
		wg.Add(1)
		go func(f Frame) {
			defer wg.Done()
			out, _ := json.Marshal(c.dispatch(connCtx, f))
			if len(out) > maxFrameOut {
				out, _ = json.Marshal(Frame{Type: "res", ID: f.ID, Status: http.StatusBadGateway,
					ContentType: "application/json", Body: `{"error":"response too large for the hub"}`})
			}
			writeMu.Lock()
			defer writeMu.Unlock()
			if err := conn.Write(connCtx, websocket.MessageText, out); err != nil && connCtx.Err() == nil {
				c.logf("uplink: write response %s: %v", f.ID, err)
			}
		}(f)
	}
}

type uplinkKey struct{}

// WithUplink marks ctx as belonging to a request that arrived through the
// hub uplink. Local-only routes (api /local/confirm) refuse such requests.
func WithUplink(ctx context.Context) context.Context {
	return context.WithValue(ctx, uplinkKey{}, true)
}

// FromUplink reports whether ctx was marked by WithUplink.
func FromUplink(ctx context.Context) bool { v, _ := ctx.Value(uplinkKey{}).(bool); return v }

// dispatch runs one req frame through the runner's handler.
func (c *Client) dispatch(ctx context.Context, f Frame) Frame {
	res := Frame{Type: "res", ID: f.ID}
	fail := func(status int, msg string) Frame {
		b, _ := json.Marshal(map[string]string{"error": msg})
		res.Status, res.ContentType, res.Body = status, "application/json", string(b)
		return res
	}
	u, err := url.ParseRequestURI(f.Path)
	if err != nil || u.Host != "" || !strings.HasPrefix(u.Path, "/v1/") || strings.Contains(u.Path, "/..") {
		return fail(http.StatusNotFound, "not found")
	}
	if len(f.Body) > MaxBody {
		return fail(http.StatusRequestEntityTooLarge, "request body too large")
	}
	method := f.Method
	if method == "" {
		method = http.MethodGet
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(WithUplink(ctx), method, "http://relay-uplink"+u.RequestURI(), strings.NewReader(f.Body))
	if err != nil {
		return fail(http.StatusBadRequest, "bad request")
	}
	req.RemoteAddr = "relay-hub:0" // never looks like loopback
	if f.Body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Relay-Key", c.Key)

	rw := newRecorder()
	func() {
		defer func() {
			if p := recover(); p != nil {
				c.logf("uplink: handler panic on %s %s: %v", method, u.Path, p)
				rw.code = http.StatusInternalServerError
			}
		}()
		c.Handler.ServeHTTP(rw, req)
	}()
	if rw.overflow {
		return fail(http.StatusRequestEntityTooLarge, "response body too large")
	}
	res.Status = rw.status()
	res.ContentType = rw.header.Get("Content-Type")
	res.Body = rw.body.String()
	return res
}

// recorder is an in-memory http.ResponseWriter capped at MaxBody.
type recorder struct {
	header   http.Header
	code     int
	body     bytes.Buffer
	overflow bool
}

func newRecorder() *recorder { return &recorder{header: http.Header{}} }

func (r *recorder) Header() http.Header { return r.header }

func (r *recorder) WriteHeader(code int) {
	if r.code == 0 {
		r.code = code
	}
}

func (r *recorder) Write(p []byte) (int, error) {
	if r.code == 0 {
		r.code = http.StatusOK
	}
	if r.overflow || r.body.Len()+len(p) > MaxBody {
		r.overflow = true
		return 0, errors.New("uplink: response body exceeds 4 MiB")
	}
	return r.body.Write(p)
}

// Flush is a no-op so handlers that flush still work (the whole body is
// sent as one frame when the handler returns).
func (r *recorder) Flush() {}

func (r *recorder) status() int {
	if r.code == 0 {
		return http.StatusOK
	}
	return r.code
}
