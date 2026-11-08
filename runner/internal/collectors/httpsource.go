package collectors

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"relay/runner/internal/credentials"
)

// "http" sources: the runner (trusted code) GETs an API with one of the
// user's stored credentials and hands the body to the network-less script
// as <inputs>/http/<sourceIndex>.json (or .txt). The script never sees the
// credential.
const (
	HTTPTimeout      = 15 * time.Second
	MaxHTTPBytes     = 1 << 20
	maxHTTPRedirects = 5
	maxURLLen        = 2048
)

// URL placeholders, replaced by the run window as RFC3339 UTC with
// seconds ("2026-11-07T00:00:00Z"), inserted verbatim (all URL-safe).
const (
	phFrom = "{from}"
	phTo   = "{to}"
)

func expandURL(raw string, from, to time.Time) string {
	r := strings.NewReplacer(phFrom, from.UTC().Format(time.RFC3339), phTo, to.UTC().Format(time.RFC3339))
	return r.Replace(raw)
}

// checkHTTPSource validates an http source's static shape. The
// credential's host is compared at install (if it exists) and at every
// fetch.
func checkHTTPSource(s Source) error {
	if err := credentials.ValidName(s.Credential); err != nil {
		return errors.New("http source: credential name must match [a-z0-9_-]{1,32}")
	}
	if s.Method != "" && s.Method != http.MethodGet {
		return errors.New(`http source: only method "GET" is allowed`)
	}
	if strings.TrimSpace(s.Why) == "" {
		return errors.New("http source needs why")
	}
	_, err := parseHTTPURL(s.URL)
	return err
}

// parseHTTPURL checks the declared URL (placeholders allowed after the
// host) and returns it parsed with a sample window.
func parseHTTPURL(raw string) (*url.URL, error) {
	if raw == "" || len(raw) > maxURLLen {
		return nil, fmt.Errorf("http source: url is required (max %d chars)", maxURLLen)
	}
	rest, ok := strings.CutPrefix(raw, "https://")
	if !ok {
		return nil, errors.New("http source: url must start with https://")
	}
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	if strings.ContainsAny(rest, "{}") {
		return nil, errors.New("http source: placeholders are not allowed in the host")
	}
	sample := expandURL(raw, time.Unix(0, 0), time.Unix(0, 0))
	if strings.ContainsAny(sample, "{}") {
		return nil, errors.New("http source: only {from} and {to} placeholders are supported")
	}
	u, err := url.Parse(sample)
	if err != nil {
		return nil, errors.New("http source: url does not parse")
	}
	switch {
	case u.Scheme != "https" || u.Opaque != "":
		return nil, errors.New("http source: url must be https://host/...")
	case u.User != nil:
		return nil, errors.New("http source: url must not contain user:password@")
	case u.Port() != "" || u.Host != u.Hostname():
		return nil, errors.New("http source: url must not set a port")
	case u.Fragment != "" || strings.Contains(raw, "#"):
		return nil, errors.New("http source: url must not have a #fragment")
	}
	if err := credentials.ValidHost(u.Hostname()); err != nil {
		return nil, fmt.Errorf("http source: %s", strings.TrimPrefix(err.Error(), credentials.ErrBadRequest.Error()+": "))
	}
	return u, nil
}

// checkHTTPCredentials: at install, an http source whose credential already
// exists must call exactly that credential's host. A missing credential is
// allowed (added later); runs then fail with an actionable ErrSource.
func (e *Engine) checkHTTPCredentials(m Manifest) error {
	for i, s := range m.Sources {
		if s.Type != "http" || e.Credentials == nil {
			continue
		}
		c, err := e.Credentials.Get(s.Credential)
		if errors.Is(err, credentials.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		u, _ := parseHTTPURL(s.URL)
		if u.Hostname() != c.Host {
			return badf("source %d: url host %s does not match credential %q's host %s", i, u.Hostname(), s.Credential, c.Host)
		}
	}
	return nil
}

var errOffHost = errors.New("redirect to another host or scheme blocked")

// httpSnapshot fetches source i into dir/<i>.json|.txt. Errors never
// contain the credential's value: url.Error (which embeds the full URL,
// query auth included) is unwrapped, and only host+path are named.
func (e *Engine) httpSnapshot(ctx context.Context, i int, s Source, from, to time.Time, dir string) error {
	if e.Credentials == nil {
		return fmt.Errorf("%w: http source %d: credentials are not enabled on this runner", ErrSource, i)
	}
	cred, err := e.Credentials.Get(s.Credential)
	if errors.Is(err, credentials.ErrNotFound) {
		return fmt.Errorf("%w: http source %d needs a credential named %q: add it on this runner (Relay → runner → KPI credentials)", ErrSource, i, s.Credential)
	}
	if err != nil {
		return fmt.Errorf("%w: http source %d: %v", ErrSource, i, err)
	}
	if _, err := parseHTTPURL(s.URL); err != nil {
		return fmt.Errorf("%w: %v", ErrBadRequest, err)
	}
	u, err := url.Parse(expandURL(s.URL, from, to))
	if err != nil {
		return fmt.Errorf("%w: http source %d: url does not parse", ErrBadRequest, i)
	}
	where := u.Hostname() + u.EscapedPath()
	if u.Hostname() != cred.Host {
		return fmt.Errorf("%w: http source %d calls %s but credential %q is for %s: fix the credential's host or the collector", ErrSource, i, u.Hostname(), s.Credential, cred.Host)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return fmt.Errorf("%w: http source %d: bad request", ErrBadRequest, i)
	}
	req.Header.Set("User-Agent", "relay-runner-collector")
	req.Header.Set("Accept", "application/json, text/plain;q=0.9, */*;q=0.5")
	cred.Apply(req)
	client := &http.Client{
		Transport: e.httpTransport(),
		Timeout:   HTTPTimeout,
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) >= maxHTTPRedirects {
				return errors.New("too many redirects")
			}
			if r.URL.Scheme != "https" || r.URL.Hostname() != cred.Host || r.URL.Port() != "" {
				return errOffHost
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
			err = fmt.Errorf("timed out after %s", HTTPTimeout)
		}
		return fmt.Errorf("%w: http source %d (%s): %v", ErrSource, i, where, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w: http source %d (%s): HTTP %d - %s rejected credential %q; check or replace it on this runner", ErrSource, i, where, resp.StatusCode, cred.Host, s.Credential)
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return fmt.Errorf("%w: http source %d (%s): HTTP %d", ErrSource, i, where, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxHTTPBytes+1))
	if err != nil {
		return fmt.Errorf("%w: http source %d (%s): reading body failed", ErrSource, i, where)
	}
	if len(body) > MaxHTTPBytes {
		return fmt.Errorf("%w: http source %d (%s): response larger than %d KiB", ErrSource, i, where, MaxHTTPBytes>>10)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d%s", i, bodyExt(resp.Header.Get("Content-Type")))), body, 0o644)
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func bodyExt(ct string) string {
	mt, _, _ := mime.ParseMediaType(ct)
	if mt == "application/json" || strings.HasSuffix(mt, "+json") {
		return ".json"
	}
	return ".txt"
}

func (e *Engine) httpTransport() http.RoundTripper {
	if e.HTTPTransport != nil {
		return e.HTTPTransport
	}
	return safeTransport()
}

// safeTransport: no env proxy (it would bypass the guard), and a dial guard
// that refuses non-public addresses at connect time, so a public hostname
// that resolves to 127.0.0.1 / 10.x / 100.64.x (Tailscale) / link-local
// metadata cannot be reached.
func safeTransport() *http.Transport {
	d := &net.Dialer{Timeout: 10 * time.Second, Control: func(network, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return fmt.Errorf("refusing to dial %s", address)
		}
		if !publicAddr(ap.Addr()) {
			return fmt.Errorf("refusing to connect to non-public address %s", ap.Addr())
		}
		return nil
	}}
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           d.DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: HTTPTimeout,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
	}
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

func publicAddr(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsGlobalUnicast() && !a.IsPrivate() && !a.IsLoopback() && !a.IsLinkLocalUnicast() && !cgnat.Contains(a)
}
