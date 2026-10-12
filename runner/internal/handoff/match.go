package handoff

import (
	"net/url"
	"sort"
	"strings"
	"time"
)

// EncodeDir is Claude Code's folder name for a project path under
// <config dir>/projects/: every character outside [A-Za-z0-9] becomes '-'
// (C:\Users\me\app → C--Users-me-app, /home/me/app → -home-me-app).
// Verified live against claude-agent-acp. Claude truncates + hashes very
// long paths (~200+ chars); that case is not reproduced here.
func EncodeDir(path string) string {
	b := []byte(path)
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			b[i] = '-'
		}
	}
	return string(b)
}

// NormalizeRemote reduces a git remote URL to host/owner/repo so the same
// repo matches across https, ssh and scp-like forms, credentials, ports,
// ".git" suffixes and case. "" if it can't be parsed.
func NormalizeRemote(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var host, path string
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return ""
		}
		host, path = u.Hostname(), u.Path
	} else {
		// scp-like: [user@]host:owner/repo
		rest := raw
		if i := strings.Index(rest, "@"); i >= 0 {
			rest = rest[i+1:]
		}
		i := strings.Index(rest, ":")
		if i < 0 {
			return ""
		}
		host, path = rest[:i], rest[i+1:]
	}
	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	path = strings.Trim(path, "/")
	if host == "" || path == "" {
		return ""
	}
	return strings.ToLower(host + "/" + path)
}

// ChatStatus is one chat's state on a runner, as reported to a peer.
type ChatStatus struct {
	Present     bool  `json:"present"`
	Busy        bool  `json:"busy"`
	IdleSeconds int64 `json:"idleSeconds"`
}

// SelectIdle returns (sorted) the chats that are present, not mid-turn and
// untouched for at least min - the ones the home runner should pull back.
func SelectIdle(status map[string]ChatStatus, min time.Duration) []string {
	var ids []string
	for id, st := range status {
		if st.Present && !st.Busy && time.Duration(st.IdleSeconds)*time.Second >= min {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}
