package collectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ErrSource means an input couldn't be produced (ActivityWatch not running,
// file unreadable). Not the collector's fault: scheduled runs retry later
// and the collector's state is left alone.
var ErrSource = errors.New("source unavailable")

// MaxFileBytes caps what file sources copy per run (all globs together).
const MaxFileBytes = 200 << 20

// DefaultAWBase is ActivityWatch's local, unauthenticated API.
const DefaultAWBase = "http://localhost:5600"

// Secret deny-list, same idea as internal/jobs secretDeny: checked against
// a declared glob (literally) and against every matched path. Fail closed.
var (
	deniedDirs  = []string{".ssh", ".gnupg", ".aws", ".kube", ".password-store", "keyrings", "secrets", ".docker"}
	deniedFiles = []string{
		".env", ".env.*", "*.pem", "*.key", "*.p12", "*.pfx", "id_rsa*", "id_ed25519*", "id_ecdsa*", "id_dsa*",
		"credentials*.json", ".netrc", ".git-credentials", ".pgpass",
		// browser cookie / password / key stores (Chrome-family + Firefox)
		"Cookies", "Cookies-journal", "Login Data", "Login Data-journal", "Login Data For Account*",
		"Web Data", "Web Data-journal", "Local State", "key3.db", "key4.db", "logins.json", "cookies.sqlite*",
		"signons.sqlite",
	}
)

// denied returns why path p (or glob) is a secret path, "" if allowed.
func denied(p string) string {
	parts := strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' })
	for i, part := range parts {
		low := strings.ToLower(part)
		for _, d := range deniedDirs {
			if low == d && i < len(parts)-1 {
				return d
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	base := parts[len(parts)-1]
	for _, pat := range deniedFiles {
		if ok, _ := filepath.Match(strings.ToLower(pat), strings.ToLower(base)); ok {
			return pat
		}
	}
	for _, d := range deniedDirs {
		if strings.EqualFold(base, d) {
			return d
		}
	}
	return ""
}

// snapshot builds the inputs dir for one run: /in/aw/<type>.json per
// activitywatch bucket type, /in/files/<sourceIndex>/<path below the glob's
// fixed prefix> per file source, /in/http/<sourceIndex>.json|.txt per http
// source (httpsource.go).
func (e *Engine) snapshot(ctx context.Context, m Manifest, from, to time.Time, in string) error {
	var fileBytes int64
	for i, s := range m.Sources {
		switch s.Type {
		case "activitywatch":
			if err := e.awSnapshot(ctx, s.Buckets, from, to, filepath.Join(in, "aw")); err != nil {
				return err
			}
		case "file":
			n, err := fileSnapshot(s.Glob, filepath.Join(in, "files", fmt.Sprint(i)), MaxFileBytes-fileBytes)
			if err != nil {
				return err
			}
			fileBytes += n
		case "http":
			if err := e.httpSnapshot(ctx, i, s, from, to, filepath.Join(in, "http")); err != nil {
				return err
			}
		}
	}
	return nil
}

func (e *Engine) awGet(ctx context.Context, path string, q url.Values, v any) error {
	u := strings.TrimRight(e.AWBase, "/") + path
	if q != nil {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := e.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("%w: activitywatch not reachable at %s: %v", ErrSource, e.AWBase, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%w: activitywatch %s: %s %s", ErrSource, path, resp.Status, b)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 256<<20)).Decode(v); err != nil {
		return fmt.Errorf("%w: activitywatch %s: %v", ErrSource, path, err)
	}
	return nil
}

// awBuckets lists bucket ids, sorted.
func (e *Engine) awBuckets(ctx context.Context) ([]string, error) {
	var buckets map[string]json.RawMessage
	if err := e.awGet(ctx, "/api/0/buckets/", nil, &buckets); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(buckets))
	for id := range buckets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func (e *Engine) awSnapshot(ctx context.Context, types []string, from, to time.Time, dir string) error {
	ids, err := e.awBuckets(ctx)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, t := range types {
		id := ""
		for _, b := range ids {
			if strings.HasPrefix(b, "aw-watcher-"+t) {
				id = b
				break
			}
		}
		if id == "" {
			hint := ""
			if t == "web" {
				hint = " (is the ActivityWatch browser extension installed?)"
			}
			return fmt.Errorf("%w: activitywatch has no aw-watcher-%s bucket%s", ErrSource, t, hint)
		}
		q := url.Values{"start": {from.Format(time.RFC3339)}, "end": {to.Format(time.RFC3339)}, "limit": {"-1"}}
		var events json.RawMessage
		if err := e.awGet(ctx, "/api/0/buckets/"+url.PathEscape(id)+"/events", q, &events); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, t+".json"), events, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// globRoot is the glob's fixed prefix: the dirs before the first wildcard.
func globRoot(g string) string {
	dir := g
	for strings.ContainsAny(dir, `*?[`) {
		dir = filepath.Dir(dir)
	}
	if dir == g {
		dir = filepath.Dir(g)
	}
	return dir
}

func expandHome(g string) (string, error) {
	if !strings.HasPrefix(g, "~/") {
		return g, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, g[2:]), nil
}

// fileSnapshot copies the glob's regular-file matches (symlinks resolved,
// every path re-checked against the deny-list) into dir. Copying, not
// binding, sidesteps browser SQLite locks. Returns bytes copied.
func fileSnapshot(glob, dir string, budget int64) (int64, error) {
	g, err := expandHome(glob)
	if err != nil {
		return 0, err
	}
	matches, err := filepath.Glob(g)
	if err != nil {
		return 0, fmt.Errorf("%w: glob %q: %v", ErrBadRequest, glob, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	root := globRoot(g)
	var total int64
	for _, p := range matches {
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			continue
		}
		if why := denied(p); why != "" {
			return total, fmt.Errorf("%w: %s matches the secret deny-list (%s)", ErrBadRequest, p, why)
		}
		if why := denied(real); why != "" {
			return total, fmt.Errorf("%w: %s resolves to a secret path (%s)", ErrBadRequest, p, why)
		}
		fi, err := os.Stat(real)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if total+fi.Size() > budget {
			return total, fmt.Errorf("%w: file sources exceed %d MB per run", ErrBadRequest, MaxFileBytes>>20)
		}
		rel, err := filepath.Rel(root, p)
		if err != nil || strings.HasPrefix(rel, "..") {
			rel = filepath.Base(p)
		}
		dst := filepath.Join(dir, rel)
		n, err := copyFile(real, dst, budget-total)
		total += n
		if err != nil {
			return total, fmt.Errorf("%w: copy %s: %v", ErrSource, p, err)
		}
	}
	return total, nil
}

func copyFile(src, dst string, limit int64) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, err
	}
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, io.LimitReader(in, limit+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > limit {
		err = fmt.Errorf("file grew past the %d MB cap while copying", MaxFileBytes>>20)
	}
	return n, err
}
