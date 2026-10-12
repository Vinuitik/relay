package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Subagent viewing: read-only access to the background agents (the Agent
// tool) a chat spawned, straight from the Claude transcripts:
//
//	<claude dir>/projects/<enc cwd>/<parent id>.jsonl                      parent chat
//	<claude dir>/projects/<enc cwd>/<parent id>/subagents/agent-<id>.jsonl  one per subagent
//	.../agent-<id>.meta.json  {agentType, description, toolUseId, ...}
//
// Like the shared-chat code, nothing is cached: every call re-reads the file.

// ErrSubagentNotFound: no such subagent transcript for the session.
var ErrSubagentNotFound = errors.New("subagent not found")

// subagentRunningWindow: a transcript untouched for longer than this is
// treated as finished even without a final report (killed or abandoned).
const subagentRunningWindow = 5 * time.Minute

// MaxSubagentMessages caps one SubagentMessages response.
const MaxSubagentMessages = 200

// maxSubagentText caps a single message text.
const maxSubagentText = 20000

var subagentIDPattern = regexp.MustCompile(`^[A-Za-z0-9]+$`)

// SubagentInfo is one entry of GET /v1/sessions/{id}/agents.
type SubagentInfo struct {
	AgentID        string `json:"agentId"`
	Description    string `json:"description"`
	Status         string `json:"status"` // "running" | "done"
	StartedAt      string `json:"startedAt"`
	LastActivityAt string `json:"lastActivityAt"`
	MessageCount   int    `json:"messageCount"`
}

// SubagentPage is the GET /v1/sessions/{id}/agents/{agentId}/messages body.
// Pass Next back as ?after= to get only newer messages.
type SubagentPage struct {
	Status   string    `json:"status"`
	Total    int       `json:"total"`
	Next     int       `json:"next"`
	Messages []Message `json:"messages"`
}

type subagentTranscript struct {
	msgs         []Message
	started      string
	last         string
	finalReport  bool // last entry is an assistant turn that ended
	lastModified time.Time
}

// subagentsDir returns the subagents folder of the session chat,
// "" if the chat has no transcript.
func (m *Manager) subagentsDir(sessionID string) (string, error) {
	rec, err := m.lookup(sessionID)
	if err != nil {
		return "", err
	}
	rec.mu.Lock()
	id := rec.acpSession
	rec.mu.Unlock()
	tp := findTranscript(id)
	if tp == "" {
		return "", nil
	}
	return filepath.Join(strings.TrimSuffix(tp, ".jsonl"), "subagents"), nil
}

// Subagents lists the session subagents, oldest first. Empty (not an
// error) when the chat has no transcript or never spawned any.
func (m *Manager) Subagents(sessionID string) ([]SubagentInfo, error) {
	dir, err := m.subagentsDir(sessionID)
	if err != nil {
		return nil, err
	}
	return listSubagents(dir), nil
}

// SubagentMessages returns up to MaxSubagentMessages messages of one
// subagent after the first `after` ones.
func (m *Manager) SubagentMessages(sessionID, agentID string, after int) (SubagentPage, error) {
	dir, err := m.subagentsDir(sessionID)
	if err != nil {
		return SubagentPage{}, err
	}
	if dir == "" || !subagentIDPattern.MatchString(agentID) {
		return SubagentPage{}, ErrSubagentNotFound
	}
	tr, err := parseSubagent(filepath.Join(dir, "agent-"+agentID+".jsonl"))
	if err != nil {
		return SubagentPage{}, ErrSubagentNotFound
	}
	return tr.page(after), nil
}

func (t *subagentTranscript) status() string {
	if t.finalReport || time.Since(t.lastModified) > subagentRunningWindow {
		return "done"
	}
	return "running"
}

func (t *subagentTranscript) page(after int) SubagentPage {
	if after < 0 {
		after = 0
	}
	if after > len(t.msgs) {
		after = len(t.msgs)
	}
	end := after + MaxSubagentMessages
	if end > len(t.msgs) {
		end = len(t.msgs)
	}
	out := make([]Message, end-after)
	copy(out, t.msgs[after:end])
	return SubagentPage{Status: t.status(), Total: len(t.msgs), Next: end, Messages: out}
}

func listSubagents(dir string) []SubagentInfo {
	out := []SubagentInfo{}
	if dir == "" {
		return out
	}
	files, _ := filepath.Glob(filepath.Join(dir, "agent-*.jsonl"))
	for _, f := range files {
		id := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "agent-"), ".jsonl")
		tr, err := parseSubagent(f)
		if err != nil {
			continue
		}
		var meta struct {
			Description string `json:"description"`
		}
		if b, err := os.ReadFile(strings.TrimSuffix(f, ".jsonl") + ".meta.json"); err == nil {
			_ = json.Unmarshal(b, &meta)
		}
		last := tr.last
		if last == "" {
			last = tr.lastModified.UTC().Format(time.RFC3339)
		}
		out = append(out, SubagentInfo{
			AgentID: id, Description: meta.Description, Status: tr.status(),
			StartedAt: tr.started, LastActivityAt: last, MessageCount: len(tr.msgs),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt < out[j].StartedAt })
	return out
}

type tline struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Message   *struct {
		Content    json.RawMessage `json:"content"`
		StopReason string          `json:"stop_reason"`
	} `json:"message"`
}

type tblock struct {
	Type      string         `json:"type"`
	Text      string         `json:"text"`
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Input     map[string]any `json:"input"`
	ToolUseID string         `json:"tool_use_id"`
	IsError   bool           `json:"is_error"`
}

// parseSubagent turns one agent-*.jsonl into phone-shaped messages: the
// prompt (role user), assistant text (agent), tool_use rows (tool, status
// from the matching tool_result). Thinking and tool_result bodies are skipped.
func parseSubagent(path string) (*subagentTranscript, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	t := &subagentTranscript{lastModified: st.ModTime()}
	toolIdx := map[string]int{} // tool_use id -> index in t.msgs
	r := bufio.NewReaderSize(f, 1<<20)
	for {
		line, rerr := r.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			t.consume(line, toolIdx)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, rerr
		}
	}
	return t, nil
}

func (t *subagentTranscript) consume(line []byte, toolIdx map[string]int) {
	var l tline
	if json.Unmarshal(line, &l) != nil || l.Message == nil || (l.Type != "user" && l.Type != "assistant") {
		return
	}
	if t.started == "" {
		t.started = l.Timestamp
	}
	if l.Timestamp != "" {
		t.last = l.Timestamp
	}
	at := l.Timestamp
	role := "user"
	if l.Type == "assistant" {
		role = "agent"
	}
	t.finalReport = l.Type == "assistant" && l.Message.StopReason == "end_turn"
	var s string
	if json.Unmarshal(l.Message.Content, &s) == nil { // plain string content
		if strings.TrimSpace(s) != "" {
			t.msgs = append(t.msgs, Message{Role: role, Text: clip(s), At: at})
		}
		return
	}
	var blocks []tblock
	if json.Unmarshal(l.Message.Content, &blocks) != nil {
		return
	}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) != "" {
				t.msgs = append(t.msgs, Message{Role: role, Text: clip(b.Text), At: at})
			}
		case "tool_use":
			toolIdx[b.ID] = len(t.msgs)
			t.msgs = append(t.msgs, Message{
				Role: "tool", Text: toolTitle(b.Name, b.Input), At: at,
				ToolKind: toolKind(b.Name), Status: "in_progress",
			})
		case "tool_result":
			if i, ok := toolIdx[b.ToolUseID]; ok {
				t.msgs[i].Status = "completed"
				if b.IsError {
					t.msgs[i].Status = "failed"
				}
			}
		}
	}
}

func clip(s string) string {
	if len(s) > maxSubagentText {
		return s[:maxSubagentText] + "..."
	}
	return s
}

func toolKind(name string) string {
	switch name {
	case "Read":
		return "read"
	case "Edit", "Write", "NotebookEdit", "MultiEdit":
		return "edit"
	case "Bash", "PowerShell":
		return "execute"
	case "Grep", "Glob":
		return "search"
	case "WebFetch", "WebSearch":
		return "fetch"
	}
	return "other"
}

// toolTitle is a one-line summary like the live chat: "Read foo.go",
// "Bash: npm test", "Grep: pattern".
func toolTitle(name string, in map[string]any) string {
	arg := ""
	for _, k := range []string{"file_path", "notebook_path", "command", "pattern", "url", "query", "description", "prompt"} {
		if v, _ := in[k].(string); v != "" {
			arg = v
			break
		}
	}
	arg = strings.Join(strings.Fields(arg), " ")
	if len(arg) > 120 {
		arg = arg[:120] + "..."
	}
	if arg == "" {
		return name
	}
	if name == "Read" || name == "Edit" || name == "Write" {
		return fmt.Sprintf("%s %s", name, filepath.Base(strings.ReplaceAll(arg, "\\", "/")))
	}
	return name + ": " + arg
}