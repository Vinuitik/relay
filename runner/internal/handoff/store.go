package handoff

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// writeJSONFile persists v atomically (tmp + rename); readJSONFile treats a
// missing file as empty.
func writeJSONFile(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	_ = os.Remove(path) // Windows: rename won't replace
	return os.Rename(tmp, path)
}

func readJSONFile(path string, v any) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// Peer is another runner this one can exchange chats with. The phone hands
// it over at pairing time (name, Tailscale URL, that runner's key).
type Peer struct {
	Name    string `json:"name"`
	BaseURL string `json:"baseUrl"`
	Key     string `json:"key"`
}

// PeerStore is $RELAY_HOME/peers.json.
type PeerStore struct {
	mu    sync.Mutex
	file  string
	peers map[string]Peer
}

func NewPeerStore(file string) (*PeerStore, error) {
	s := &PeerStore{file: file, peers: map[string]Peer{}}
	var list []Peer
	if err := readJSONFile(file, &list); err != nil {
		return nil, err
	}
	for _, p := range list {
		s.peers[p.Name] = p
	}
	return s, nil
}

func (s *PeerStore) List() []Peer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listLocked()
}

func (s *PeerStore) listLocked() []Peer {
	out := make([]Peer, 0, len(s.peers))
	for _, p := range s.peers {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *PeerStore) Get(name string) (Peer, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.peers[name]
	return p, ok
}

func (s *PeerStore) Set(p Peer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.peers[p.Name] = p
	return writeJSONFile(s.file, s.listLocked())
}

func (s *PeerStore) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.peers, name)
	return writeJSONFile(s.file, s.listLocked())
}

// Lent records a chat this (home) runner released to a peer, so the
// auto-return loop knows what to ask for. ProjectID is the local project it
// came from.
type Lent struct {
	AgentChatID string    `json:"agentChatId"`
	ProjectID   string    `json:"projectId,omitempty"`
	Remote      string    `json:"remote,omitempty"`
	At          time.Time `json:"at"`
}

// LentStore is $RELAY_HOME/handoffs.json (chat origin tracking).
type LentStore struct {
	mu   sync.Mutex
	file string
	m    map[string]Lent
}

func NewLentStore(file string) (*LentStore, error) {
	s := &LentStore{file: file, m: map[string]Lent{}}
	var list []Lent
	if err := readJSONFile(file, &list); err != nil {
		return nil, err
	}
	for _, l := range list {
		s.m[l.AgentChatID] = l
	}
	return s, nil
}

func (s *LentStore) List() []Lent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listLocked()
}

func (s *LentStore) listLocked() []Lent {
	out := make([]Lent, 0, len(s.m))
	for _, l := range s.m {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AgentChatID < out[j].AgentChatID })
	return out
}

func (s *LentStore) Get(id string) (Lent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.m[id]
	return l, ok
}

func (s *LentStore) Put(l Lent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[l.AgentChatID] = l
	return writeJSONFile(s.file, s.listLocked())
}

func (s *LentStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[id]; !ok {
		return nil
	}
	delete(s.m, id)
	return writeJSONFile(s.file, s.listLocked())
}
