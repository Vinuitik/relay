package handoff

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"relay/runner/internal/project"
)

func TestEncodeDir(t *testing.T) {
	cases := map[string]string{
		`C:\Users\me\app`: "C--Users-me-app",
		"/home/me/my.app": "-home-me-my-app",
	}
	for in, want := range cases {
		if got := EncodeDir(in); got != want {
			t.Errorf("EncodeDir(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeRemote(t *testing.T) {
	same := []string{
		"https://github.com/Vinuitik/Relay.git",
		"https://user:tok@GitHub.com/vinuitik/relay/",
		"git@github.com:Vinuitik/Relay.git",
		"ssh://git@github.com:22/vinuitik/relay.git",
	}
	for _, u := range same {
		if got := NormalizeRemote(u); got != "github.com/vinuitik/relay" {
			t.Errorf("NormalizeRemote(%q) = %q", u, got)
		}
	}
	for _, u := range []string{"", "nonsense", "https://github.com"} {
		if got := NormalizeRemote(u); got != "" {
			t.Errorf("NormalizeRemote(%q) = %q, want empty", u, got)
		}
	}
}

func TestSelectIdle(t *testing.T) {
	st := map[string]ChatStatus{
		"old":    {Present: true, IdleSeconds: 3600},
		"edge":   {Present: true, IdleSeconds: 1800},
		"fresh":  {Present: true, IdleSeconds: 60},
		"busy":   {Present: true, Busy: true, IdleSeconds: 9999},
		"absent": {IdleSeconds: 9999},
	}
	got := SelectIdle(st, 30*time.Minute)
	if want := []string{"edge", "old"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SelectIdle = %v, want %v", got, want)
	}
}

type fakeChats struct {
	busy    map[string]bool
	dropped []string
}

func (f *fakeChats) ChatBusy(id string) bool { return f.busy[id] }
func (f *fakeChats) DropChat(id string)      { f.dropped = append(f.dropped, id) }

type fakeProjects []project.Project

func (f fakeProjects) List() []project.Project { return f }
func (f fakeProjects) Get(id string) (project.Project, error) {
	for _, p := range f {
		if p.ID == id {
			return p, nil
		}
	}
	return project.Project{}, project.ErrNotFound
}

const chatID = "b99d84fb-25a2-4775-a90c-b64333ae8e63"

// newTestService builds a runner with one project at projPath whose git
// remote is `remote`, and (if seed) a chat transcript in Claude's folder.
func newTestService(t *testing.T, projPath, remote string, seed bool, chats Chats) (*Service, string) {
	t.Helper()
	home, claude := t.TempDir(), t.TempDir()
	s, err := New(home, chats, fakeProjects{{ID: "p1", Path: projPath}},
		func(context.Context, string) string { return remote },
		func() string { return claude })
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(claude, "projects", EncodeDir(projPath), chatID+".jsonl")
	if seed {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(`{"type":"user"}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return s, path
}

func TestReleaseRefusesWhileBusy(t *testing.T) {
	chats := &fakeChats{busy: map[string]bool{chatID: true}}
	s, path := newTestService(t, `C:\work\app`, "git@github.com:a/b.git", true, chats)
	if _, err := s.Release(context.Background(), chatID, ""); err != ErrBusy {
		t.Fatalf("err = %v, want ErrBusy", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("busy release must leave the transcript in place: %v", err)
	}
	if len(chats.dropped) != 0 {
		t.Fatal("busy release must not drop the session")
	}
}

func TestReleaseMovesFileAndRecordsLent(t *testing.T) {
	chats := &fakeChats{}
	s, path := newTestService(t, `C:\work\app`, "git@github.com:a/b.git", true, chats)
	s.HomeMode = true
	data, err := s.Release(context.Background(), chatID, "https://github.com/a/b")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"type":"user"`)) {
		t.Fatalf("data = %q", data)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("transcript should be gone from Claude's folder: %v", err)
	}
	if _, _, err := s.Copy(chatID); err != nil {
		t.Fatalf("handed-off copy missing: %v", err)
	}
	if l, ok := s.Lent.Get(chatID); !ok || l.ProjectID != "p1" {
		t.Fatalf("lent = %+v %v", l, ok)
	}
	if len(chats.dropped) != 1 {
		t.Fatalf("dropped = %v", chats.dropped)
	}
}

func TestReleaseRemoteMismatch(t *testing.T) {
	s, path := newTestService(t, `C:\work\app`, "git@github.com:a/b.git", true, &fakeChats{})
	if _, err := s.Release(context.Background(), chatID, "git@github.com:a/other.git"); err != ErrMismatch {
		t.Fatalf("err = %v, want ErrMismatch", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("mismatch must not move the file")
	}
}

func TestStatusIdle(t *testing.T) {
	s, path := newTestService(t, "/srv/app", "", true, &fakeChats{})
	old := time.Now().Add(-45 * time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	st := s.Status([]string{chatID, "missing-id"})
	if !st[chatID].Present || st[chatID].IdleSeconds < 45*60 {
		t.Fatalf("status = %+v", st[chatID])
	}
	if st["missing-id"].Present {
		t.Fatal("missing chat reported present")
	}
}

func TestInstallEncodesProjectPathAndRefusesDuplicate(t *testing.T) {
	s, _ := newTestService(t, "/srv/my app", "", false, &fakeChats{})
	if _, err := s.Install(chatID, "p1", []byte("x\n")); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(s.ClaudeDir(), "projects", "-srv-my-app", chatID+".jsonl")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected transcript at %s: %v", want, err)
	}
	if _, err := s.Install(chatID, "p1", []byte("y\n")); err != ErrExists {
		t.Fatalf("second install err = %v, want ErrExists", err)
	}
}

// End to end between two runners over real HTTP: B pulls a chat from A, A's
// copy lands in handed-off/, A refuses while busy, and B can take over from
// A's copy after losing the file.
func TestPullAndTakeoverBetweenRunners(t *testing.T) {
	chatsA := &fakeChats{busy: map[string]bool{chatID: true}}
	a, aPath := newTestService(t, `C:\a\app`, "git@github.com:a/b.git", true, chatsA)
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/handoff/release":
			data, err := a.Release(r.Context(), chatID, r.URL.Query().Get("x"))
			if err != nil {
				code := "internal"
				if err == ErrBusy {
					code = "busy"
				}
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"error":"` + err.Error() + `","code":"` + code + `"}`))
				return
			}
			_, _ = w.Write(data)
		case "/v1/handoff/copy/" + chatID:
			data, saved, err := a.Copy(chatID)
			if err != nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("X-Relay-Copy-At", saved.UTC().Format(time.RFC3339))
			_, _ = w.Write(data)
		}
	}))
	defer srvA.Close()

	b, bPath := newTestService(t, "/srv/app", "https://github.com/a/b", false, &fakeChats{})
	if err := b.Peers.Set(Peer{Name: "laptop", BaseURL: srvA.URL, Key: "k"}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if _, err := b.Pull(ctx, "laptop", chatID, "p1", false); !isBusy(err) {
		t.Fatalf("pull while busy err = %v, want busy", err)
	}
	chatsA.busy[chatID] = false
	res, err := b.Pull(ctx, "laptop", chatID, "p1", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Bytes == 0 {
		t.Fatal("empty transfer")
	}
	if _, err := os.Stat(bPath); err != nil {
		t.Fatalf("B should hold the chat at %s: %v", bPath, err)
	}
	if _, err := os.Stat(aPath); !os.IsNotExist(err) {
		t.Fatal("A should no longer hold the chat")
	}

	// B loses the file; A is the only one with a copy → take over from it.
	if err := os.Remove(bPath); err != nil {
		t.Fatal(err)
	}
	to, err := b.Takeover(ctx, "laptop", chatID, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if !to.MaybeStale || to.Source != "laptop" || to.CopySaved.IsZero() {
		t.Fatalf("takeover = %+v", to)
	}
}

func isBusy(err error) bool {
	pe, ok := err.(*PeerError)
	return ok && pe.Is(ErrBusy)
}

func TestTakeoverFromOwnCopy(t *testing.T) {
	s, _ := newTestService(t, "/srv/app", "", true, &fakeChats{})
	if _, err := s.Release(context.Background(), chatID, ""); err != nil {
		t.Fatal(err)
	}
	to, err := s.Takeover(context.Background(), "", chatID, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if !to.MaybeStale || to.Source != "own" {
		t.Fatalf("takeover = %+v", to)
	}
}
