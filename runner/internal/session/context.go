package session

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"
)

// Context size: how many tokens a chat's next message re-sends to the model
// (input + cache write + cache read of its last assistant reply, read from
// Claude Code's transcript). The web shows it so a long chat's cost is
// visible before resuming it - see shared/API.md Session.contextTokens.

// contextTailBytes bounds the read: the last assistant line is near the end;
// a single huge tool result above it can hide it (then 0 = unknown).
const contextTailBytes = 512 << 10

type ctxEntry struct {
	path   string
	size   int64
	mtime  time.Time
	tokens int
}

var ctxCache sync.Map // agent session id → ctxEntry

// contextTokensFor returns the chat's current context size, 0 if unknown.
// Cached per transcript size+mtime, so repeated polls cost one stat.
func contextTokensFor(agentSessionID string) int {
	if agentSessionID == "" {
		return 0
	}
	var e ctxEntry
	if v, ok := ctxCache.Load(agentSessionID); ok {
		e = v.(ctxEntry)
	}
	if e.path == "" {
		if e.path = findTranscript(agentSessionID); e.path == "" {
			return 0
		}
	}
	fi, err := os.Stat(e.path)
	if err != nil {
		ctxCache.Delete(agentSessionID) // moved (handoff) or deleted: re-find next time
		return 0
	}
	if fi.Size() == e.size && fi.ModTime().Equal(e.mtime) {
		return e.tokens
	}
	e.size, e.mtime, e.tokens = fi.Size(), fi.ModTime(), lastContextTokens(e.path, fi.Size())
	ctxCache.Store(agentSessionID, e)
	return e.tokens
}

func lastContextTokens(path string, size int64) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	off := size - contextTailBytes
	if off < 0 {
		off = 0
	}
	buf := make([]byte, size-off)
	if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
		return 0
	}
	lines := bytes.Split(buf, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		l := lines[i]
		if !bytes.Contains(l, []byte(`"assistant"`)) || !bytes.Contains(l, []byte(`"usage"`)) {
			continue
		}
		var d struct {
			Type    string `json:"type"`
			Message struct {
				Usage struct {
					In int `json:"input_tokens"`
					CW int `json:"cache_creation_input_tokens"`
					CR int `json:"cache_read_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(l, &d) != nil || d.Type != "assistant" {
			continue
		}
		u := d.Message.Usage
		if n := u.In + u.CW + u.CR; n > 0 {
			return n
		}
	}
	return 0
}
