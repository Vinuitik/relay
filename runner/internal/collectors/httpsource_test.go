package collectors

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"relay/runner/internal/credentials"
)

const testSecret = "tok-SECRET-123"

func httpManifest(src string) string {
	return `{"schedule":"daily 00:10","window":"1d","sources":[` + src + `],"unit":"n","aggregation":"last"}`
}

func TestHTTPManifestValidation(t *testing.T) {
	ok := []string{
		`{"type":"http","credential":"github","url":"https://api.github.com/search/issues?q=created:{from}..{to}","why":"PRs"}`,
		`{"type":"http","credential":"gh_1","url":"https://api.github.com/user","method":"GET","why":"x"}`,
	}
	for _, s := range ok {
		if _, _, err := ParseManifest(json.RawMessage(httpManifest(s))); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	bad := []string{
		`{"type":"http","credential":"github","url":"http://api.github.com/user","why":"x"}`,
		`{"type":"http","credential":"github","url":"https://api.github.com/user","method":"POST","why":"x"}`,
		`{"type":"http","credential":"github","url":"https://api.github.com/user"}`,
		`{"type":"http","credential":"Bad Name","url":"https://api.github.com/user","why":"x"}`,
		`{"type":"http","url":"https://api.github.com/user","why":"x"}`,
		`{"type":"http","credential":"github","url":"https://127.0.0.1/user","why":"x"}`,
		`{"type":"http","credential":"github","url":"https://[::1]/user","why":"x"}`,
		`{"type":"http","credential":"github","url":"https://localhost/user","why":"x"}`,
		`{"type":"http","credential":"github","url":"https://api.github.com:8443/user","why":"x"}`,
		`{"type":"http","credential":"github","url":"https://u:p@api.github.com/user","why":"x"}`,
		`{"type":"http","credential":"github","url":"https://{from}.github.com/user","why":"x"}`,
		`{"type":"http","credential":"github","url":"https://api.github.com/{nope}","why":"x"}`,
		`{"type":"http","credential":"github","url":"https://api.github.com/x#f","why":"x"}`,
	}
	for _, s := range bad {
		if _, _, err := ParseManifest(json.RawMessage(httpManifest(s))); !errors.Is(err, ErrBadRequest) {
			t.Errorf("%s: %v, want bad request", s, err)
		}
	}
	// credential + url are part of the contract
	_, h1, _ := ParseManifest(json.RawMessage(httpManifest(ok[1])))
	_, h2, _ := ParseManifest(json.RawMessage(httpManifest(strings.Replace(ok[1], "/user", "/user/repos", 1))))
	_, h3, _ := ParseManifest(json.RawMessage(httpManifest(strings.Replace(ok[1], "gh_1", "gh_2", 1))))
	if h1 == h2 || h1 == h3 {
		t.Fatal("contract hash ignores url or credential")
	}
}

func TestHTTPInstallHostMismatch(t *testing.T) {
	e := newTestEngine(t, &fakeSandbox{}, "")
	e.Credentials = credentials.Open(e.DataDir)
	if _, err := e.Credentials.Put("github", credentials.PutRequest{Host: "api.github.com", Auth: "bearer", Secret: json.RawMessage(`"x"`)}); err != nil {
		t.Fatal(err)
	}
	m := httpManifest(`{"type":"http","credential":"github","url":"https://evil.example.com/","why":"x"}`)
	if _, err := e.Install(InstallRequest{ID: "a", KPIName: "k", Manifest: json.RawMessage(m), Files: map[string]string{"collector.py": "print(1)"}}); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("host mismatch install = %v", err)
	}
	// missing credential: install allowed (added later)
	install(t, e, "b", httpManifest(`{"type":"http","credential":"later","url":"https://api.github.com/","why":"x"}`), nil)
}

// tlsEngine: credential host example.com (in httptest's cert), every dial
// goes to srv.
func tlsEngine(t *testing.T, h http.Handler, auth string) (*Engine, *httptest.Server) {
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	e := newTestEngine(t, &fakeSandbox{}, "")
	e.Credentials = credentials.Open(e.DataDir)
	var req credentials.PutRequest
	json.Unmarshal([]byte(auth), &req)
	if _, err := e.Credentials.Put("svc", req); err != nil {
		t.Fatal(err)
	}
	tr := srv.Client().Transport.(*http.Transport).Clone()
	addr := srv.Listener.Addr().String()
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	tr.TLSClientConfig = &tls.Config{RootCAs: tr.TLSClientConfig.RootCAs, ServerName: "example.com"}
	e.HTTPTransport = tr
	return e, srv
}

var (
	wFrom = time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	wTo   = time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
)

func TestHTTPFetchAuthAndPlaceholders(t *testing.T) {
	var gotAuth, gotQuery, gotHeader string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotQuery, gotHeader = r.Header.Get("Authorization"), r.URL.RawQuery, r.Header.Get("X-Api-Key")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		fmt.Fprint(w, `{"n": 4}`)
	})
	e, _ := tlsEngine(t, h, `{"host":"example.com","auth":"bearer","secret":"`+testSecret+`"}`)
	dir := t.TempDir()
	src := Source{Type: "http", Credential: "svc", URL: "https://example.com/q?since={from}&until={to}", Why: "x"}
	if err := e.httpSnapshot(context.Background(), 2, src, wFrom, wTo, dir); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer "+testSecret || gotQuery != "since=2026-11-07T00:00:00Z&until=2026-11-08T00:00:00Z" {
		t.Fatalf("auth %q query %q", gotAuth, gotQuery)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "2.json")); string(b) != `{"n": 4}` {
		t.Fatalf("2.json = %q", b)
	}

	e2, _ := tlsEngine(t, h, `{"host":"example.com","auth":"query","secret":{"key":"k","token":"`+testSecret+`"}}`)
	if err := e2.httpSnapshot(context.Background(), 0, Source{Credential: "svc", URL: "https://example.com/c?f=1"}, wFrom, wTo, dir); err != nil {
		t.Fatal(err)
	}
	if gotQuery != "f=1&key=k&token="+testSecret || gotAuth != "" {
		t.Fatalf("query auth: %q %q", gotQuery, gotAuth)
	}
	e3, _ := tlsEngine(t, h, `{"host":"example.com","auth":"header","headerName":"X-Api-Key","secret":"`+testSecret+`"}`)
	if err := e3.httpSnapshot(context.Background(), 0, Source{Credential: "svc", URL: "https://example.com/c"}, wFrom, wTo, dir); err != nil || gotHeader != testSecret {
		t.Fatalf("header auth: %v %q", err, gotHeader)
	}
}

func TestHTTPFetchFailuresNeverLeakSecret(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)
	var offHostHit bool
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect-off":
			http.Redirect(w, r, "https://other.example.org/steal", http.StatusFound)
		case "/redirect-http":
			http.Redirect(w, r, "http://example.com/x", http.StatusFound)
		case "/redirect-same":
			http.Redirect(w, r, "/ok", http.StatusFound)
		case "/ok":
			w.Header().Set("Content-Type", "text/csv")
			fmt.Fprint(w, "a,b")
		case "/big":
			w.Write(bytes.Repeat([]byte("x"), MaxHTTPBytes+10))
		case "/denied":
			http.Error(w, "echo "+r.URL.RawQuery, http.StatusUnauthorized)
		case "/steal":
			offHostHit = true
		default:
			http.Error(w, "nope", http.StatusInternalServerError)
		}
	})
	e, srv := tlsEngine(t, h, `{"host":"example.com","auth":"query","queryParam":"token","secret":"`+testSecret+`"}`)
	dir := t.TempDir()
	get := func(p string) error {
		return e.httpSnapshot(context.Background(), 1, Source{Credential: "svc", URL: "https://example.com" + p}, wFrom, wTo, dir)
	}
	if err := get("/redirect-same"); err != nil {
		t.Fatalf("same-host redirect: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "1.txt")); string(b) != "a,b" {
		t.Fatalf("1.txt = %q", b)
	}
	var errs []error
	for _, p := range []string{"/redirect-off", "/redirect-http", "/big", "/denied", "/500"} {
		err := get(p)
		if !errors.Is(err, ErrSource) {
			t.Errorf("%s: %v, want ErrSource", p, err)
		}
		errs = append(errs, err)
	}
	if offHostHit {
		t.Fatal("followed an off-host redirect")
	}
	if !strings.Contains(errs[0].Error(), "redirect") || !strings.Contains(errs[2].Error(), "larger than") || !strings.Contains(errs[3].Error(), "rejected credential") {
		t.Errorf("messages: %v", errs)
	}
	srv.Close() // connection error path (url.Error carries the full URL)
	errs = append(errs, get("/gone"))
	if err := e.httpSnapshot(context.Background(), 0, Source{Credential: "missing", URL: "https://example.com/"}, wFrom, wTo, dir); !errors.Is(err, ErrSource) || !strings.Contains(err.Error(), `credential named "missing"`) {
		t.Errorf("missing credential: %v", err)
	}
	if err := e.httpSnapshot(context.Background(), 0, Source{Credential: "svc", URL: "https://api.github.com/"}, wFrom, wTo, dir); !errors.Is(err, ErrSource) {
		t.Errorf("host changed under the collector: %v", err)
	}
	for _, err := range errs {
		if err != nil && strings.Contains(err.Error(), testSecret) {
			t.Errorf("error leaks the secret: %v", err)
		}
	}
	if strings.Contains(logs.String(), testSecret) {
		t.Errorf("log leaks the secret: %s", logs.String())
	}
}

func TestPublicAddrGuard(t *testing.T) {
	for _, a := range []string{"127.0.0.1", "10.1.2.3", "192.168.1.1", "172.16.0.1", "100.100.1.1", "169.254.169.254", "::1", "fd00::1", "fe80::1", "0.0.0.0", "::ffff:127.0.0.1"} {
		if publicAddr(netip.MustParseAddr(a)) {
			t.Errorf("%s counted as public", a)
		}
	}
	for _, a := range []string{"140.82.112.6", "2606:4700::1111"} {
		if !publicAddr(netip.MustParseAddr(a)) {
			t.Errorf("%s counted as private", a)
		}
	}
	// the real transport refuses loopback even for an allowed-looking dial
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	_, err := (&http.Client{Transport: safeTransport()}).Get(srv.URL)
	if err == nil || !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("safeTransport dialed loopback: %v", err)
	}
}
