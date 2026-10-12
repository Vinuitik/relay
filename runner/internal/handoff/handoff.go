// Package handoff moves a chat's transcript between two runners (laptop ↔
// server) - see runner/FLOWS.md "Shared chats" and HANDOFF-chat-sync.md.
//
// A chat lives on the machine whose ~/.claude/projects/<dir>/<id>.jsonl
// holds it; moving the file moves ownership. Transfer is pull-based:
//
//	target: Pull    → POST peer /v1/handoff/release → Install (write file)
//	source: Release → refuse if mid-turn → move file to $RELAY_HOME/handed-off/
//	                  (outside Claude's folder, so VS Code can't see it)
//	home:   RunReturn → POST peer /v1/handoff/status → idle ≥ N min → Pull back
//	either: Takeover → restore from a handed-off copy when the owner is asleep
package handoff

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"relay/runner/internal/project"
)

var (
	ErrBusy      = errors.New("chat is mid-turn")
	ErrNotFound  = errors.New("chat not found")
	ErrMismatch  = errors.New("project git remotes don't match")
	ErrNoRemote  = errors.New("project has no git remote to match on")
	ErrExists    = errors.New("chat already exists here")
	ErrBadID     = errors.New("invalid chat id")
	ErrNoPeer    = errors.New("unknown peer")
	ErrNoProject = errors.New("project not found")
	// ErrUnreachable wraps transport failures talking to a peer.
	ErrUnreachable = errors.New("peer unreachable")
)

// Chats is the part of session.Manager the handoff needs.
type Chats interface {
	ChatBusy(agentSessionID string) bool
	DropChat(agentSessionID string)
}

// Projects is the part of project.Registry the handoff needs.
type Projects interface {
	List() []project.Project
	Get(id string) (project.Project, error)
}

// Service holds the handoff logic.
type Service struct {
	Home     string // RELAY_HOME
	Peers    *PeerStore
	Lent     *LentStore
	Sessions Chats
	Projects Projects
	// Remote returns a project folder's git origin URL ("" if none).
	Remote func(ctx context.Context, dir string) string
	// ClaudeDir returns Claude's config dir (<dir>/projects/...).
	ClaudeDir func() string
	// HomeMode: this runner is the "home" of the chats it releases - it
	// records them in Lent and RunReturn pulls them back.
	HomeMode bool
	IdleMin  time.Duration // idle time before auto-return
	Client   *http.Client
	Now      func() time.Time
	// OnReleased, if set, runs (own goroutine) after Release succeeded -
	// used for the "Chat moved to <peer>" push.
	OnReleased func(chatID string)
}

// New builds a Service persisting peers.json / handoffs.json under home.
func New(home string, sessions Chats, projects Projects, remote func(context.Context, string) string, claudeDir func() string) (*Service, error) {
	peers, err := NewPeerStore(filepath.Join(home, "peers.json"))
	if err != nil {
		return nil, fmt.Errorf("load peers: %w", err)
	}
	lent, err := NewLentStore(filepath.Join(home, "handoffs.json"))
	if err != nil {
		return nil, fmt.Errorf("load handoffs: %w", err)
	}
	return &Service{
		Home: home, Peers: peers, Lent: lent, Sessions: sessions, Projects: projects,
		Remote: remote, ClaudeDir: claudeDir, IdleMin: 30 * time.Minute,
		Client: &http.Client{Timeout: 5 * time.Minute}, Now: time.Now,
	}, nil
}

var chatIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

func (s *Service) handedOffDir() string { return filepath.Join(s.Home, "handed-off") }

// findChat locates <id>.jsonl in any project folder → (path, folder name).
func (s *Service) findChat(id string) (string, string) {
	matches, _ := filepath.Glob(filepath.Join(s.ClaudeDir(), "projects", "*", id+".jsonl"))
	if len(matches) == 0 {
		return "", ""
	}
	return matches[0], filepath.Base(filepath.Dir(matches[0]))
}

// projectForDir finds the registered project whose encoded path is dirName.
func (s *Service) projectForDir(dirName string) (project.Project, bool) {
	for _, p := range s.Projects.List() {
		if EncodeDir(p.Path) == dirName {
			return p, true
		}
	}
	return project.Project{}, false
}

// ---- source side ----

// Release hands a chat to a peer: refuses while a Relay session is mid-turn
// or (if remote != "") when the chat's project has a different git remote;
// otherwise returns the transcript bytes and moves its own file to
// $RELAY_HOME/handed-off/ (kept as the "last copy" for Takeover).
func (s *Service) Release(ctx context.Context, id, remote string) ([]byte, error) {
	if !chatIDPattern.MatchString(id) {
		return nil, ErrBadID
	}
	path, dirName := s.findChat(id)
	if path == "" {
		return nil, ErrNotFound
	}
	if s.Sessions.ChatBusy(id) {
		return nil, ErrBusy
	}
	proj, known := s.projectForDir(dirName)
	if remote != "" {
		if !known || NormalizeRemote(s.Remote(ctx, proj.Path)) != NormalizeRemote(remote) {
			return nil, ErrMismatch
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.handedOffDir(), 0o700); err != nil {
		return nil, err
	}
	copyPath := filepath.Join(s.handedOffDir(), id+".jsonl")
	_ = os.Remove(copyPath)
	if err := os.Rename(path, copyPath); err != nil {
		// Different volume: copy then delete.
		if err := os.WriteFile(copyPath, data, 0o600); err != nil {
			return nil, err
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	s.Sessions.DropChat(id)
	if s.HomeMode {
		l := Lent{AgentChatID: id, At: s.Now()}
		if known {
			l.ProjectID = proj.ID
			l.Remote = s.Remote(ctx, proj.Path)
		}
		if err := s.Lent.Put(l); err != nil {
			log.Printf("handoff: record lent chat %s: %v", id, err)
		}
	}
	if s.OnReleased != nil {
		go s.OnReleased(id)
	}
	return data, nil
}

// Status reports each id's state here: present, mid-turn, seconds since the
// transcript last changed.
func (s *Service) Status(ids []string) map[string]ChatStatus {
	out := make(map[string]ChatStatus, len(ids))
	for _, id := range ids {
		if !chatIDPattern.MatchString(id) {
			continue
		}
		path, _ := s.findChat(id)
		if path == "" {
			out[id] = ChatStatus{}
			continue
		}
		st := ChatStatus{Present: true, Busy: s.Sessions.ChatBusy(id)}
		if fi, err := os.Stat(path); err == nil {
			st.IdleSeconds = int64(s.Now().Sub(fi.ModTime()).Seconds())
		}
		out[id] = st
	}
	return out
}

// Copy returns this runner's handed-off copy of a chat and when it was saved.
func (s *Service) Copy(id string) ([]byte, time.Time, error) {
	if !chatIDPattern.MatchString(id) {
		return nil, time.Time{}, ErrBadID
	}
	p := filepath.Join(s.handedOffDir(), id+".jsonl")
	fi, err := os.Stat(p)
	if err != nil {
		return nil, time.Time{}, ErrNotFound
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, time.Time{}, err
	}
	return data, fi.ModTime(), nil
}

// ---- target side ----

// Installed describes a chat written into a local project.
type Installed struct {
	AgentChatID string `json:"agentChatId"`
	ProjectID   string `json:"projectId"`
	Bytes       int    `json:"bytes"`
}

// Install writes a transcript into Claude's folder for the project's path.
// The cwd fields inside the jsonl are left as-is (session/load was verified
// to work with a foreign cwd there). Refuses if the chat already exists.
func (s *Service) Install(id, projectID string, data []byte) (Installed, error) {
	if !chatIDPattern.MatchString(id) {
		return Installed{}, ErrBadID
	}
	p, err := s.Projects.Get(projectID)
	if err != nil {
		return Installed{}, ErrNoProject
	}
	if existing, _ := s.findChat(id); existing != "" {
		return Installed{}, ErrExists
	}
	dir := filepath.Join(s.ClaudeDir(), "projects", EncodeDir(p.Path))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Installed{}, err
	}
	dst := filepath.Join(dir, id+".jsonl")
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return Installed{}, err
	}
	_ = os.Remove(dst)
	if err := os.Rename(tmp, dst); err != nil {
		return Installed{}, err
	}
	if err := s.Lent.Delete(id); err != nil {
		log.Printf("handoff: clear lent chat %s: %v", id, err)
	}
	return Installed{AgentChatID: id, ProjectID: projectID, Bytes: len(data)}, nil
}

// Pull fetches chat id from peerName and installs it in projectID. Unless
// link is set (projects linked by hand on the phone) the local project's git
// remote must be non-empty and is sent so the peer can verify it matches.
func (s *Service) Pull(ctx context.Context, peerName, id, projectID string, link bool) (Installed, error) {
	if !chatIDPattern.MatchString(id) {
		return Installed{}, ErrBadID
	}
	peer, ok := s.Peers.Get(peerName)
	if !ok {
		return Installed{}, ErrNoPeer
	}
	p, err := s.Projects.Get(projectID)
	if err != nil {
		return Installed{}, ErrNoProject
	}
	if existing, _ := s.findChat(id); existing != "" {
		return Installed{}, ErrExists
	}
	remote := ""
	if !link {
		if remote = s.Remote(ctx, p.Path); remote == "" {
			return Installed{}, ErrNoRemote
		}
	}
	body, _ := json.Marshal(map[string]string{"agentChatId": id, "remote": remote})
	data, _, err := s.peerCall(ctx, peer, http.MethodPost, "/v1/handoff/release", body)
	if err != nil {
		return Installed{}, err
	}
	return s.Install(id, projectID, data)
}

// TakenOver is Takeover's result. MaybeStale is always true: a copy made at
// handoff time lacks any turns the other machine added afterwards.
type TakenOver struct {
	Installed
	MaybeStale bool      `json:"maybeStale"`
	CopySaved  time.Time `json:"copySavedAt"`
	Source     string    `json:"source"` // "own" or the peer's name
}

// Takeover restores chat id into projectID from peerName's handed-off copy
// (or this runner's own when peerName is ""), for when the owner is
// unreachable. Fails with ErrExists if the chat is already here.
func (s *Service) Takeover(ctx context.Context, peerName, id, projectID string) (TakenOver, error) {
	var data []byte
	var saved time.Time
	var err error
	source := "own"
	if peerName == "" {
		data, saved, err = s.Copy(id)
	} else {
		source = peerName
		peer, ok := s.Peers.Get(peerName)
		if !ok {
			return TakenOver{}, ErrNoPeer
		}
		var hdr http.Header
		data, hdr, err = s.peerCall(ctx, peer, http.MethodGet, "/v1/handoff/copy/"+id, nil)
		if err == nil {
			saved, _ = time.Parse(time.RFC3339, hdr.Get("X-Relay-Copy-At"))
		}
	}
	if err != nil {
		return TakenOver{}, err
	}
	in, err := s.Install(id, projectID, data)
	if err != nil {
		return TakenOver{}, err
	}
	return TakenOver{Installed: in, MaybeStale: true, CopySaved: saved, Source: source}, nil
}

// ---- peer HTTP ----

// PeerError is a peer's non-200 answer; Code is its JSON "code" ("busy",
// "mismatch", "not_found") when it sent one.
type PeerError struct {
	Status int
	Code   string
	Msg    string
}

func (e *PeerError) Error() string { return fmt.Sprintf("peer answered %d: %s", e.Status, e.Msg) }

// Is maps peer codes back onto the package errors so callers can switch on them.
func (e *PeerError) Is(target error) bool {
	switch e.Code {
	case "busy":
		return target == ErrBusy
	case "mismatch":
		return target == ErrMismatch
	case "not_found":
		return target == ErrNotFound
	}
	return false
}

func (s *Service) peerCall(ctx context.Context, p Peer, method, path string, body []byte) ([]byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(p.BaseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("X-Relay-Key", p.Key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	if resp.StatusCode != http.StatusOK {
		pe := &PeerError{Status: resp.StatusCode, Msg: strings.TrimSpace(string(data))}
		var e struct{ Error, Code string }
		if json.Unmarshal(data, &e) == nil {
			pe.Msg, pe.Code = e.Error, e.Code
		}
		return nil, nil, pe
	}
	return data, resp.Header, nil
}

// ---- auto-return ----

// RunReturn is the home runner's background loop: every interval, ask each
// peer about the chats it lent out and pull back the ones idle ≥ IdleMin.
// Returns when ctx is cancelled.
func (s *Service) RunReturn(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.ReturnTick(ctx)
		}
	}
}

// ReturnTick is one pass of RunReturn.
func (s *Service) ReturnTick(ctx context.Context) {
	lent := s.Lent.List()
	if len(lent) == 0 {
		return
	}
	ids := make([]string, len(lent))
	byID := map[string]Lent{}
	for i, l := range lent {
		ids[i] = l.AgentChatID
		byID[l.AgentChatID] = l
	}
	body, _ := json.Marshal(map[string]any{"chatIds": ids})
	for _, peer := range s.Peers.List() {
		data, _, err := s.peerCall(ctx, peer, http.MethodPost, "/v1/handoff/status", body)
		if err != nil {
			continue // asleep / unreachable: try next pass
		}
		var resp struct {
			Chats map[string]ChatStatus `json:"chats"`
		}
		if json.Unmarshal(data, &resp) != nil {
			continue
		}
		for _, id := range SelectIdle(resp.Chats, s.IdleMin) {
			l := byID[id]
			if l.ProjectID == "" {
				continue // origin project unknown: needs a manual pull
			}
			if _, err := s.Pull(ctx, peer.Name, id, l.ProjectID, true); err != nil {
				log.Printf("handoff: return %s from %s: %v", id, peer.Name, err)
				continue
			}
			log.Printf("handoff: chat %s returned from %s", id, peer.Name)
		}
	}
}
