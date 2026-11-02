package jobs

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Tool sets. map/plan: read-only file tools + subagents (which inherit
// the same tools and the same --restricted confinement). kpi: none.
const readOnlyTools = "Read,Grep,Glob,Agent"

// Disallowed explicitly as a second layer; --restricted + --tools already
// leave them out.
const deniedTools = "Bash,Edit,Write,NotebookEdit,WebFetch,WebSearch"

// secretDeny keeps likely secrets inside the project out of map/plan jobs
// (their output is stored by the hub). Passed as --settings
// permissions.deny; Read rules also hide the files from Grep and Glob, in
// subagents too (verified live). Claude's rule globs have no negation or
// character classes and deny beats allow, so ".env.*" also blocks
// .env.example / .env.sample - fail closed. .git/HEAD and refs stay
// readable (the hub reads the commit sha); .git/config can hold tokens in
// remote URLs.
var secretDeny = []string{
	"Read(**/.env)", "Read(**/.env.*)",
	"Read(**/*.pem)", "Read(**/*.key)", "Read(**/*.p12)", "Read(**/*.pfx)",
	"Read(**/id_rsa*)", "Read(**/id_ed25519*)", "Read(**/id_ecdsa*)",
	"Read(**/secrets/**)", "Read(**/.git/config)", "Read(**/credentials*.json)",
}

// secretSettings is the --settings JSON for map/plan jobs.
func secretSettings() string {
	b, _ := json.Marshal(map[string]any{"permissions": map[string]any{"deny": secretDeny}})
	return string(b)
}

// Args builds the claude command line for a job. The prompt goes on stdin
// (no argv length limit, no "-"-prefixed prompt read as a flag).
//
// Confinement (verified live, claude 2.1.273): --restricted confines
// Read/Grep/Glob to the working directory (absolute paths, ../ and
// symlinks pointing outside are refused with "is outside <cwd>"), drops
// Bash/code-running tools and WebFetch, and ignores user/project/local
// settings files (so no user hooks, allow rules or MCP from there);
// --strict-mcp-config drops the remaining MCP servers;
// --permission-mode dontAsk + --permission-prompts none deny anything that
// would otherwise ask.
func Args(kind string, req Request) []string {
	tools := readOnlyTools
	if kind == KindKPI {
		tools = ""
	}
	a := []string{
		"-p", "--output-format", "stream-json", "--verbose",
		"--restricted", "--strict-mcp-config",
		"--tools", tools,
		"--disallowedTools", deniedTools,
		"--permission-mode", "dontAsk", "--permission-prompts", "none",
		"--no-session-persistence",
	}
	if kind != KindKPI {
		a = append(a, "--settings", secretSettings())
	}
	if req.Model != "" {
		a = append(a, "--model", req.Model)
	}
	if req.MaxTurns > 0 {
		a = append(a, "--max-turns", strconv.Itoa(req.MaxTurns))
	}
	return a
}

// run executes one job to completion and reports via m.done.
func (m *Manager) run(ctx context.Context, j *job, bin string) {
	dir := j.dir
	if j.kind == KindKPI {
		tmp, err := os.MkdirTemp("", "relay-job-")
		if err != nil {
			m.done(j, StateFailed, "temp dir: "+err.Error(), "", nil)
			return
		}
		defer os.RemoveAll(tmp)
		dir = tmp
	}

	cmd := exec.CommandContext(ctx, bin, append(append([]string{}, m.ExtraArgs...), Args(j.kind, j.req)...)...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(j.req.Prompt)
	cmd.WaitDelay = 5 * time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &limitedWriter{buf: &stderr, max: 8 << 10}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		m.done(j, StateFailed, err.Error(), "", nil)
		return
	}
	if err := cmd.Start(); err != nil {
		m.done(j, StateFailed, "start claude: "+err.Error(), "", nil)
		return
	}
	m.addEvent(j, EventStep, "Started")

	t := &translator{cwd: dir}
	r := bufio.NewReaderSize(stdout, 64<<10)
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			for _, ev := range t.line(line) {
				m.addEvent(j, ev.Kind, ev.Text)
			}
		}
		if err != nil {
			break
		}
	}
	waitErr := cmd.Wait()

	switch {
	case t.gotResult && !t.isError:
		m.done(j, StateDone, "", t.result, t.usage)
	case t.gotResult:
		msg := t.result
		if t.subtype != "success" && t.subtype != "" {
			msg = strings.TrimSuffix(t.subtype+": "+t.result, ": ")
		}
		m.done(j, StateFailed, msg, "", t.usage)
	default:
		msg := "claude exited without a result"
		if waitErr != nil {
			msg += " (" + waitErr.Error() + ")"
		}
		if s := strings.TrimSpace(stderr.String()); s != "" {
			msg += ": " + s
		}
		m.done(j, StateFailed, msg, "", nil)
	}
}

// translator turns stream-json lines into short human events.
type translator struct {
	cwd       string
	gotResult bool
	isError   bool
	subtype   string
	result    string
	usage     *Usage
}

type streamLine struct {
	Type            string          `json:"type"`
	Subtype         string          `json:"subtype"`
	ParentToolUseID *string         `json:"parent_tool_use_id"`
	Message         json.RawMessage `json:"message"`
	// result
	IsError bool   `json:"is_error"`
	Result  string `json:"result"`
	Usage   *struct {
		InputTokens         int `json:"input_tokens"`
		OutputTokens        int `json:"output_tokens"`
		CacheCreationTokens int `json:"cache_creation_input_tokens"`
		CacheReadTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage"`
	// system permission_denied
	ToolName       string `json:"tool_name"`
	DecisionReason string `json:"decision_reason"`
}

type contentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

func (t *translator) line(b []byte) []Event {
	var l streamLine
	if json.Unmarshal(b, &l) != nil {
		return nil
	}
	switch l.Type {
	case "assistant":
		var msg struct {
			Content []contentBlock `json:"content"`
		}
		if json.Unmarshal(l.Message, &msg) != nil {
			return nil
		}
		sub := l.ParentToolUseID != nil
		var out []Event
		for _, c := range msg.Content {
			switch c.Type {
			case "tool_use":
				s := t.step(c.Name, c.Input)
				if sub {
					s = "(subagent) " + s
				}
				out = append(out, Event{Kind: EventStep, Text: s})
			case "text":
				if !sub && strings.TrimSpace(c.Text) != "" {
					out = append(out, Event{Kind: EventText, Text: strings.TrimSpace(c.Text)})
				}
			}
		}
		return out
	case "system":
		if l.Subtype == "permission_denied" {
			return []Event{{Kind: EventError, Text: "Blocked " + l.ToolName + ": " + l.DecisionReason}}
		}
	case "result":
		t.gotResult = true
		t.isError = l.IsError || (l.Subtype != "" && l.Subtype != "success")
		t.subtype = l.Subtype
		t.result = l.Result
		if l.Usage != nil {
			t.usage = &Usage{
				InputTokens:  l.Usage.InputTokens + l.Usage.CacheCreationTokens + l.Usage.CacheReadTokens,
				OutputTokens: l.Usage.OutputTokens,
			}
		}
	}
	return nil
}

// step: "Reading src/Foo.java", "Searching for X", "Started subagent: Y".
func (t *translator) step(name string, raw json.RawMessage) string {
	var in map[string]any
	_ = json.Unmarshal(raw, &in)
	str := func(k string) string { s, _ := in[k].(string); return s }
	switch name {
	case "Read":
		return "Reading " + t.rel(str("file_path"))
	case "Grep":
		s := "Searching for " + str("pattern")
		if p := str("path"); p != "" {
			s += " in " + t.rel(p)
		}
		return s
	case "Glob":
		s := "Finding files " + str("pattern")
		if p := str("path"); p != "" {
			s += " in " + t.rel(p)
		}
		return s
	case "Agent", "Task":
		return "Started subagent: " + str("description")
	}
	return "Using " + name
}

// rel shows paths inside the job dir relative to it.
func (t *translator) rel(p string) string {
	if p == "" || t.cwd == "" || !filepath.IsAbs(p) {
		return filepath.ToSlash(p)
	}
	if r, err := filepath.Rel(t.cwd, p); err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(r)
	}
	return filepath.ToSlash(p)
}

type limitedWriter struct {
	buf *bytes.Buffer
	max int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if room := w.max - w.buf.Len(); room > 0 {
		if len(p) > room {
			w.buf.Write(p[:room])
		} else {
			w.buf.Write(p)
		}
	}
	return len(p), nil
}
