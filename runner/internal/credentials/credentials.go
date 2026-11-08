// Package credentials stores the API credentials that KPI collectors use
// through "http" sources (see internal/collectors). Values live only on
// this machine, in one 0600 JSON file in the runner data dir; the API lists
// names and hosts, never values. The runner itself adds a credential to the
// requests it makes; collector scripts never see it.
// See runner/FLOWS.md "Credentials" and shared/API.md.
package credentials

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	// ErrBadRequest marks invalid input → HTTP 400.
	ErrBadRequest = errors.New("bad request")
	// ErrNotFound: no credential with that name → HTTP 404.
	ErrNotFound = errors.New("credential not found")
)

func badf(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrBadRequest, fmt.Sprintf(format, a...))
}

// FileName is the store file in the runner data dir ($RELAY_HOME).
const FileName = "credentials.json"

const (
	AuthBearer = "bearer" // Authorization: Bearer <secret>
	AuthHeader = "header" // <HeaderName>: <secret>
	AuthQuery  = "query"  // ?<QueryParam>=<secret>, or every param of Params

	maxSecret = 8 << 10
	maxParams = 8
	maxBody   = 64 << 10
)

var (
	nameRe   = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
	labelRe  = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	paramRe  = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	headerRe = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]{1,64}$")

	// Hostnames that never reach the public internet.
	deniedHostSuffixes = []string{"localhost", "local", "internal", "lan", "home", "corp", "intranet", "arpa", "test", "invalid", "onion"}
	// Headers a credential may not set (transport/framing/host).
	deniedHeaders = []string{"host", "content-length", "transfer-encoding", "connection", "upgrade", "te", "trailer",
		"keep-alive", "proxy-authorization", "proxy-connection", "expect"}
)

// ValidName checks a credential name: [a-z0-9_-]{1,32}.
func ValidName(name string) error {
	if !nameRe.MatchString(name) {
		return badf("credential name must match [a-z0-9_-]{1,32}")
	}
	return nil
}

// ValidHost checks a bare public DNS hostname: lowercase, no scheme, port,
// path, userinfo or trailing dot; not an IP literal, not localhost or a
// private-use suffix (.local, .internal, .lan, ...). Public names that
// resolve to private addresses are refused at connect time by the fetcher
// (collectors' dial guard), not here.
func ValidHost(host string) error {
	if host == "" || len(host) > 253 {
		return badf("host is required (a bare hostname like api.github.com)")
	}
	if net.ParseIP(strings.Trim(host, "[]")) != nil {
		return badf("host %q: IP addresses are not allowed, use a hostname", host)
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return badf("host %q: need a full hostname like api.github.com", host)
	}
	for _, l := range labels {
		if !labelRe.MatchString(l) {
			return badf("host %q: must be a bare lowercase hostname (no scheme, port, path or trailing dot)", host)
		}
	}
	tld := labels[len(labels)-1]
	if strings.Trim(tld, "0123456789") == "" {
		return badf("host %q: numeric top-level label (IP-like) not allowed", host)
	}
	for _, d := range deniedHostSuffixes {
		if tld == d {
			return badf("host %q: .%s names are private, not allowed", host, d)
		}
	}
	return nil
}

// Credential is one stored credential, value included. Never encode or
// log it; String() hides the value.
type Credential struct {
	Name       string            `json:"name"`
	Host       string            `json:"host"`
	Auth       string            `json:"auth"`
	HeaderName string            `json:"headerName,omitempty"`
	QueryParam string            `json:"queryParam,omitempty"`
	Secret     string            `json:"secret,omitempty"`
	Params     map[string]string `json:"params,omitempty"` // query auth with several params (Trello key+token)
	UpdatedAt  time.Time         `json:"updatedAt"`
}

// String and GoString keep the value out of %v / %+v / %#v.
func (c Credential) String() string {
	return fmt.Sprintf("credential %s (%s, %s)", c.Name, c.Auth, c.Host)
}
func (c Credential) GoString() string { return c.String() }

// Info is what the API shows: never the value.
type Info struct {
	Name      string    `json:"name"`
	Host      string    `json:"host"`
	Auth      string    `json:"auth"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (c Credential) Info() Info {
	return Info{Name: c.Name, Host: c.Host, Auth: c.Auth, UpdatedAt: c.UpdatedAt}
}

// Apply adds the credential to req: a header, or query params appended to
// the existing raw query (the approved URL text is kept as is).
func (c Credential) Apply(req *http.Request) {
	switch c.Auth {
	case AuthBearer:
		req.Header.Set("Authorization", "Bearer "+c.Secret)
	case AuthHeader:
		req.Header.Set(c.HeaderName, c.Secret)
	case AuthQuery:
		var add []string
		if len(c.Params) > 0 {
			keys := make([]string, 0, len(c.Params))
			for k := range c.Params {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				add = append(add, url.QueryEscape(k)+"="+url.QueryEscape(c.Params[k]))
			}
		} else {
			add = append(add, url.QueryEscape(c.QueryParam)+"="+url.QueryEscape(c.Secret))
		}
		q := req.URL.RawQuery
		if q != "" {
			q += "&"
		}
		req.URL.RawQuery = q + strings.Join(add, "&")
	}
}

// PutRequest is the body of PUT /v1/credentials/{name}. Secret is a
// string, or (auth "query" only) an object {param: value}.
type PutRequest struct {
	Host       string          `json:"host"`
	Auth       string          `json:"auth"`
	Secret     json.RawMessage `json:"secret"`
	HeaderName string          `json:"headerName,omitempty"`
	QueryParam string          `json:"queryParam,omitempty"`
}

// build validates req into a Credential. Error messages never include the
// secret.
func build(name string, req PutRequest, now time.Time) (Credential, error) {
	if err := ValidName(name); err != nil {
		return Credential{}, err
	}
	host := strings.ToLower(strings.TrimSpace(req.Host))
	if err := ValidHost(host); err != nil {
		return Credential{}, err
	}
	c := Credential{Name: name, Host: host, Auth: req.Auth, UpdatedAt: now.UTC()}
	var secret string
	var params map[string]string
	raw := []byte(strings.TrimSpace(string(req.Secret)))
	switch {
	case len(raw) == 0 || string(raw) == "null":
		return Credential{}, badf("secret is required")
	case raw[0] == '"':
		if json.Unmarshal(raw, &secret) != nil {
			return Credential{}, badf("secret must be a string")
		}
	case raw[0] == '{':
		if json.Unmarshal(raw, &params) != nil {
			return Credential{}, badf("secret object must map param names to string values")
		}
	default:
		return Credential{}, badf("secret must be a string (or, for auth \"query\", an object of params)")
	}
	switch req.Auth {
	case AuthBearer, AuthHeader:
		if params != nil {
			return Credential{}, badf("auth %q needs a string secret", req.Auth)
		}
		if req.Auth == AuthHeader {
			h := http.CanonicalHeaderKey(strings.TrimSpace(req.HeaderName))
			if !headerRe.MatchString(h) {
				return Credential{}, badf("auth \"header\" needs a valid headerName")
			}
			for _, d := range deniedHeaders {
				if strings.EqualFold(h, d) {
					return Credential{}, badf("headerName %q is not allowed", h)
				}
			}
			c.HeaderName = h
		}
		if err := checkValue(secret); err != nil {
			return Credential{}, err
		}
		c.Secret = secret
	case AuthQuery:
		if params != nil {
			if len(params) == 0 || len(params) > maxParams {
				return Credential{}, badf("secret object needs 1-%d params", maxParams)
			}
			for k, v := range params {
				if !paramRe.MatchString(k) {
					return Credential{}, badf("query param name %q must match [A-Za-z0-9_.-]{1,64}", k)
				}
				if err := checkValue(v); err != nil {
					return Credential{}, err
				}
			}
			c.Params = params
		} else {
			if !paramRe.MatchString(req.QueryParam) {
				return Credential{}, badf("auth \"query\" needs queryParam matching [A-Za-z0-9_.-]{1,64} (or a secret object of params)")
			}
			if err := checkValue(secret); err != nil {
				return Credential{}, err
			}
			c.QueryParam, c.Secret = req.QueryParam, secret
		}
	default:
		return Credential{}, badf(`auth must be "bearer", "header" or "query"`)
	}
	return c, nil
}

func checkValue(v string) error {
	if v == "" || len(v) > maxSecret {
		return badf("secret must be 1-%d bytes", maxSecret)
	}
	if strings.ContainsAny(v, "\r\n\x00") {
		return badf("secret must not contain line breaks or NUL")
	}
	return nil
}

// MaxBody caps PUT bodies (the API applies it).
const MaxBody = maxBody

// Store is the credential file. Every call re-reads the file (tiny), so
// several Store values on the same path stay consistent; writes are
// serialized per Store and atomic (temp file + rename).
type Store struct {
	path string
	mu   sync.Mutex
	Now  func() time.Time
}

// Open returns the store at dataDir/credentials.json (created on first Put).
func Open(dataDir string) *Store {
	return &Store{path: filepath.Join(dataDir, FileName), Now: time.Now}
}

// Path is the store file.
func (s *Store) Path() string { return s.path }

type fileFormat struct {
	Version     int                   `json:"version"`
	Credentials map[string]Credential `json:"credentials"`
}

func (s *Store) read() (map[string]Credential, error) {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]Credential{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read credentials: %w", err)
	}
	var f fileFormat
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("credentials file %s is corrupt (not printing it)", s.path)
	}
	if f.Credentials == nil {
		f.Credentials = map[string]Credential{}
	}
	return f.Credentials, nil
}

// write replaces the file atomically: 0600 temp file in the same dir,
// fsync, rename. On Windows the 0600 bits are not ACLs; the file is
// protected by living in the user's profile dir.
func (s *Store) write(all map[string]Credential) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(fileFormat{Version: 1, Credentials: all}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".credentials-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(name)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, s.path); err != nil {
		return err
	}
	ok = true
	return nil
}

// List returns every credential's name, host and auth kind, sorted by name.
func (s *Store) List() ([]Info, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.read()
	if err != nil {
		return nil, err
	}
	out := make([]Info, 0, len(all))
	for _, c := range all {
		out = append(out, c.Info())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get returns one credential with its value (for the fetcher only).
func (s *Store) Get(name string) (Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.read()
	if err != nil {
		return Credential{}, err
	}
	c, ok := all[name]
	if !ok {
		return Credential{}, ErrNotFound
	}
	return c, nil
}

// Put creates or replaces a credential.
func (s *Store) Put(name string, req PutRequest) (Info, error) {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	c, err := build(name, req, now())
	if err != nil {
		return Info{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.read()
	if err != nil {
		return Info{}, err
	}
	all[name] = c
	if err := s.write(all); err != nil {
		return Info{}, fmt.Errorf("save credentials: %w", err)
	}
	return c.Info(), nil
}

// Delete removes a credential (ErrNotFound if absent).
func (s *Store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.read()
	if err != nil {
		return err
	}
	if _, ok := all[name]; !ok {
		return ErrNotFound
	}
	delete(all, name)
	return s.write(all)
}
