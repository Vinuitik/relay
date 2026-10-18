// Package tokens turns Claude Code transcripts into token-usage numbers for
// the phone ("where did my plan go?"): per day, model, project and chat, plus
// flags for the patterns that burn the most (see Flags).
//
// Read-only and cache-light, like session/subagents.go: it never writes to
// Claude's files and keeps only a per-file parse cache in memory.
//
//	<claude dir>/projects/<enc cwd>/<chat id>.jsonl                        a chat
//	<claude dir>/projects/<enc cwd>/<chat id>/subagents/agent-<id>.jsonl   its subagents
//
// Every assistant API call carries a usage block. One call is split over
// several transcript lines that share message.id, so calls are de-duplicated
// by id (taking the max of each field: early lines hold partial output counts).
package tokens

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Flag names returned in SessionStat.Flags.
const (
	FlagLongChain     = "long-chain"      // many API calls per prompt you typed
	FlagHugeContext   = "huge-context"    // context grew past HugeContext tokens
	FlagOpusMechanics = "opus-mechanical" // top model spent mostly on shell commands
	FlagManyShell     = "many-shell"      // lots of Bash/PowerShell calls
)

// Thresholds behind the flags. To tune "wasted": change these.
const (
	LongChainCalls       = 40      // calls in the chat AND ...
	LongChainPerPrompt   = 25.0    // ... calls per typed prompt at or above this
	HugeContext          = 200_000 // tokens re-read on the biggest call
	ManyShellCalls       = 60
	OpusMechanicsShare   = 0.6 // shell calls / all tool calls
	OpusMechanicsMinCall = 20
)

// Relative cost weights (API price ratios, input = 1). The plan's real
// accounting is not public, so Units are an ESTIMATE for comparing chats and
// days with each other, never an exact share of the limit.
const (
	weightCacheWrite = 1.25
	weightCacheRead  = 0.1
	weightOutput     = 5.0
)

// modelFactor scales by model family (list-price ratio, sonnet = 1).
func modelFactor(model string) float64 {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "opus"):
		return 5.0 / 3.0
	case strings.Contains(m, "haiku"):
		return 1.0 / 3.0
	default:
		return 1
	}
}

// Counts is a bundle of token counters.
type Counts struct {
	Calls      int     `json:"calls"`
	Input      int64   `json:"input"` // uncached input
	Output     int64   `json:"output"`
	CacheWrite int64   `json:"cacheWrite"`
	CacheRead  int64   `json:"cacheRead"`
	Units      float64 `json:"units"` // weighted estimate, see weight* consts
}

func (c *Counts) add(r record) {
	c.Calls++
	c.Input += r.in
	c.Output += r.out
	c.CacheWrite += r.cw
	c.CacheRead += r.cr
	c.Units += (float64(r.in) + weightCacheWrite*float64(r.cw) +
		weightCacheRead*float64(r.cr) + weightOutput*float64(r.out)) * modelFactor(r.model)
}

// DayStat is one local calendar day.
type DayStat struct {
	Date string `json:"date"` // YYYY-MM-DD, runner's local time
	Counts
}

// NamedStat is a model or project bucket.
type NamedStat struct {
	Name     string `json:"name"`
	Sessions int    `json:"sessions,omitempty"`
	Counts
}

// SessionStat is one chat (its subagents folded in).
type SessionStat struct {
	ID            string   `json:"id"`
	Project       string   `json:"project"`
	Title         string   `json:"title"`
	Model         string   `json:"model"` // most used
	FirstAt       string   `json:"firstAt"`
	LastAt        string   `json:"lastAt"`
	Prompts       int      `json:"prompts"` // messages the user typed
	ToolCalls     int      `json:"toolCalls"`
	ShellCalls    int      `json:"shellCalls"` // Bash + PowerShell
	SubagentCalls int      `json:"subagentCalls"`
	MaxContext    int64    `json:"maxContext"` // tokens on the biggest single call
	Flags         []string `json:"flags"`
	Counts
}

// Report is the GET /v1/tokens body.
type Report struct {
	Days        int           `json:"days"`
	GeneratedAt string        `json:"generatedAt"`
	Totals      Counts        `json:"totals"`
	ByDay       []DayStat     `json:"byDay"` // every day in the window, oldest first
	ByModel     []NamedStat   `json:"byModel"`
	ByProject   []NamedStat   `json:"byProject"`
	Sessions    []SessionStat `json:"sessions"` // heaviest first, capped at MaxSessions
	Flagged     Counts        `json:"flagged"`  // everything in flagged chats ("possibly wasted")
	Estimate    string        `json:"estimate"`
}

// MaxSessions caps Report.Sessions.
const MaxSessions = 40

// MaxDays caps the ?days= window.
const MaxDays = 90

type record struct {
	t               time.Time
	model           string
	in, out, cw, cr int64
	subagent        bool
}

type fileData struct {
	session  string // chat id (the parent's, for subagent files)
	project  string
	title    string
	subagent bool
	prompts  int
	tools    int
	shell    int
	recs     []record
}

type cacheEntry struct {
	size int64
	mod  time.Time
	data *fileData
}

// Scanner reads transcripts under a Claude dir. Safe for concurrent use.
type Scanner struct {
	// ClaudeDir returns Claude's config dir (<dir>/projects/...).
	ClaudeDir func() string
	// Now is the clock (tests).
	Now func() time.Time

	mu    sync.Mutex
	cache map[string]cacheEntry
}

// New builds a Scanner.
func New(claudeDir func() string) *Scanner {
	return &Scanner{ClaudeDir: claudeDir, Now: time.Now, cache: map[string]cacheEntry{}}
}

// Report aggregates the last `days` days (1..MaxDays) of local-time activity.
func (s *Scanner) Report(days int) Report {
	if days < 1 {
		days = 1
	}
	if days > MaxDays {
		days = MaxDays
	}
	now := s.Now()
	loc := now.Location()
	y, m, d := now.Date()
	start := time.Date(y, m, d, 0, 0, 0, 0, loc).AddDate(0, 0, -(days - 1))

	rep := Report{
		Days:        days,
		GeneratedAt: now.UTC().Format(time.RFC3339),
		ByModel:     []NamedStat{}, // never null in JSON (the phone's parser rejects it)
		ByProject:   []NamedStat{},
		Sessions:    []SessionStat{},
		Estimate:    "Units are a weighted estimate (cache read 0.1x, cache write 1.25x, output 5x, model-scaled), not your exact plan share.",
	}
	byDay := map[string]*DayStat{}
	for i := 0; i < days; i++ {
		date := start.AddDate(0, 0, i).Format("2006-01-02")
		byDay[date] = &DayStat{Date: date}
	}
	byModel := map[string]*NamedStat{}
	byProj := map[string]*NamedStat{}
	projSess := map[string]map[string]bool{}
	sess := map[string]*sessAgg{}

	for _, fd := range s.files(start) {
		var a *sessAgg // this file's chat, once a call in the window is seen
		for _, r := range fd.recs {
			if r.t.Before(start) {
				continue
			}
			rep.Totals.add(r)
			if ds := byDay[r.t.In(loc).Format("2006-01-02")]; ds != nil {
				ds.add(r)
			}
			ms := byModel[r.model]
			if ms == nil {
				ms = &NamedStat{Name: r.model}
				byModel[r.model] = ms
			}
			ms.add(r)
			ps := byProj[fd.project]
			if ps == nil {
				ps = &NamedStat{Name: fd.project}
				byProj[fd.project] = ps
				projSess[fd.project] = map[string]bool{}
			}
			ps.add(r)
			projSess[fd.project][fd.session] = true

			if a == nil {
				a = sess[fd.session]
			}
			if a == nil {
				a = &sessAgg{stat: SessionStat{ID: fd.session, Project: fd.project}, models: map[string]int{}}
				sess[fd.session] = a
			}
			a.record(r, fd)
		}
		if a != nil {
			a.addFileMeta(fd)
		}
	}

	for i := 0; i < days; i++ {
		rep.ByDay = append(rep.ByDay, *byDay[start.AddDate(0, 0, i).Format("2006-01-02")])
	}
	for _, v := range byModel {
		rep.ByModel = append(rep.ByModel, *v)
	}
	for k, v := range byProj {
		v.Sessions = len(projSess[k])
		rep.ByProject = append(rep.ByProject, *v)
	}
	sortNamed(rep.ByModel)
	sortNamed(rep.ByProject)

	all := make([]SessionStat, 0, len(sess))
	for _, a := range sess {
		st := a.finish()
		all = append(all, st)
		if len(st.Flags) > 0 {
			rep.Flagged.Calls += st.Calls
			rep.Flagged.Input += st.Input
			rep.Flagged.Output += st.Output
			rep.Flagged.CacheWrite += st.CacheWrite
			rep.Flagged.CacheRead += st.CacheRead
			rep.Flagged.Units += st.Units
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Units > all[j].Units })
	if len(all) > MaxSessions {
		all = all[:MaxSessions]
	}
	rep.Sessions = all
	return rep
}

func sortNamed(l []NamedStat) {
	sort.Slice(l, func(i, j int) bool { return l[i].Units > l[j].Units })
}

// sessAgg folds a chat's main file and subagent files together.
type sessAgg struct {
	stat   SessionStat
	models map[string]int
	first  time.Time
	last   time.Time
}

func (a *sessAgg) record(r record, fd *fileData) {
	a.stat.Counts.add(r)
	a.models[r.model]++
	if ctx := r.in + r.cw + r.cr; ctx > a.stat.MaxContext && !fd.subagent {
		a.stat.MaxContext = ctx
	}
	if fd.subagent {
		a.stat.SubagentCalls++
	}
	if a.first.IsZero() || r.t.Before(a.first) {
		a.first = r.t
	}
	if r.t.After(a.last) {
		a.last = r.t
	}
}

func (a *sessAgg) addFileMeta(fd *fileData) {
	a.stat.ToolCalls += fd.tools
	a.stat.ShellCalls += fd.shell
	if !fd.subagent {
		a.stat.Prompts += fd.prompts
		if a.stat.Title == "" {
			a.stat.Title = fd.title
		}
	}
}

func (a *sessAgg) finish() SessionStat {
	st := a.stat
	best := 0
	for m, n := range a.models {
		if n > best {
			best, st.Model = n, m
		}
	}
	if !a.first.IsZero() {
		st.FirstAt = a.first.UTC().Format(time.RFC3339)
		st.LastAt = a.last.UTC().Format(time.RFC3339)
	}
	st.Flags = flagsFor(st)
	return st
}

func flagsFor(st SessionStat) []string {
	flags := []string{}
	prompts := st.Prompts
	if prompts < 1 {
		prompts = 1
	}
	if st.Calls >= LongChainCalls && float64(st.Calls)/float64(prompts) >= LongChainPerPrompt {
		flags = append(flags, FlagLongChain)
	}
	if st.MaxContext >= HugeContext {
		flags = append(flags, FlagHugeContext)
	}
	if st.ShellCalls >= ManyShellCalls {
		flags = append(flags, FlagManyShell)
	}
	if strings.Contains(strings.ToLower(st.Model), "opus") && st.Calls >= OpusMechanicsMinCall &&
		st.ToolCalls > 0 && float64(st.ShellCalls)/float64(st.ToolCalls) >= OpusMechanicsShare {
		flags = append(flags, FlagOpusMechanics)
	}
	return flags
}

// files returns parsed transcripts modified since `since` (older files cannot
// hold newer messages), re-parsing only when size or mtime changed.
func (s *Scanner) files(since time.Time) []*fileData {
	root := filepath.Join(s.ClaudeDir(), "projects")
	var paths []string
	for _, pat := range []string{"*/*.jsonl", "*/*/subagents/*.jsonl"} {
		m, _ := filepath.Glob(filepath.Join(root, pat))
		paths = append(paths, m...)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	live := map[string]bool{}
	var out []*fileData
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil || fi.ModTime().Before(since) {
			continue
		}
		live[p] = true
		if c, ok := s.cache[p]; ok && c.size == fi.Size() && c.mod.Equal(fi.ModTime()) {
			out = append(out, c.data)
			continue
		}
		fd := parseFile(p, root)
		if fd == nil {
			continue
		}
		s.cache[p] = cacheEntry{fi.Size(), fi.ModTime(), fd}
		out = append(out, fd)
	}
	for p := range s.cache { // forget files that vanished or aged out
		if !live[p] {
			delete(s.cache, p)
		}
	}
	return out
}

type line struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Cwd       string `json:"cwd"`
	IsMeta    bool   `json:"isMeta"`
	Sidechain bool   `json:"isSidechain"`
	Message   struct {
		ID      string          `json:"id"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   *struct {
			In  int64 `json:"input_tokens"`
			Out int64 `json:"output_tokens"`
			CW  int64 `json:"cache_creation_input_tokens"`
			CR  int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

func parseFile(path, root string) *fileData {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return nil
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	fd := &fileData{}
	switch {
	case len(parts) == 2: // <enc cwd>/<id>.jsonl
		fd.session = strings.TrimSuffix(parts[1], ".jsonl")
	case len(parts) == 4 && parts[2] == "subagents": // <enc cwd>/<id>/subagents/agent-x.jsonl
		fd.session = parts[1]
		fd.subagent = true
	default:
		return nil
	}
	fd.project = parts[0]

	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	rd := bufio.NewReaderSize(f, 1<<20)
	byID := map[string]*record{}
	order := []string{}
	for {
		raw, err := rd.ReadBytes('\n')
		if len(bytes.TrimSpace(raw)) > 0 {
			fd.consume(raw, byID, &order)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
	}
	for _, id := range order {
		fd.recs = append(fd.recs, *byID[id])
	}
	return fd
}

func (fd *fileData) consume(raw []byte, byID map[string]*record, order *[]string) {
	var l line
	if json.Unmarshal(raw, &l) != nil {
		return
	}
	if name := lastSegment(l.Cwd); name != "" {
		fd.project = name // readable folder name beats the encoded dir
	}
	switch l.Type {
	case "user":
		if l.IsMeta || l.Sidechain || fd.subagent {
			return
		}
		if text, typed := userText(l.Message.Content); typed {
			fd.prompts++
			if fd.title == "" && !strings.HasPrefix(text, "<") {
				fd.title = clip(text, 80)
			}
		}
	case "assistant":
		fd.countTools(l.Message.Content)
		u := l.Message.Usage
		if u == nil || l.Message.ID == "" || l.Message.Model == "<synthetic>" {
			return
		}
		ts, err := time.Parse(time.RFC3339, l.Timestamp)
		if err != nil {
			return
		}
		r := byID[l.Message.ID]
		if r == nil {
			r = &record{t: ts, model: l.Message.Model, subagent: fd.subagent}
			byID[l.Message.ID] = r
			*order = append(*order, l.Message.ID)
		}
		r.in, r.out = max64(r.in, u.In), max64(r.out, u.Out)
		r.cw, r.cr = max64(r.cw, u.CW), max64(r.cr, u.CR)
	}
}

func (fd *fileData) countTools(content json.RawMessage) {
	var blocks []struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return
	}
	for _, b := range blocks {
		if b.Type != "tool_use" {
			continue
		}
		fd.tools++
		if b.Name == "Bash" || b.Name == "PowerShell" {
			fd.shell++
		}
	}
}

// userText returns the text of a user message and whether the human typed it
// (as opposed to a tool result being fed back).
func userText(content json.RawMessage) (string, bool) {
	var s string
	if json.Unmarshal(content, &s) == nil {
		return strings.TrimSpace(s), strings.TrimSpace(s) != ""
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return "", false
	}
	for _, b := range blocks {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			return strings.TrimSpace(b.Text), true
		}
	}
	return "", false
}

// A git worktree under <project>/.claude/worktrees/<x> counts as <project>.
func lastSegment(p string) string {
	p = strings.TrimRight(strings.ReplaceAll(p, "\\", "/"), "/")
	if i := strings.Index(p, "/.claude/worktrees/"); i >= 0 {
		p = p[:i]
	}
	if i := strings.LastIndex(p, "/"); i >= 0 {
		p = p[i+1:]
	}
	return p
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func max64(a, b int64) int64 {
	if b > a {
		return b
	}
	return a
}
