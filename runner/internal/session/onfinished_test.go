package session

import (
	"sync"
	"testing"
	"time"
)

func testResolver(dir string) ResolveProjectDir {
	return func(projectID string) (string, bool) {
		if projectID == "proj" {
			return dir, true
		}
		return "", false
	}
}

func TestOnFinished_CalledOnStop(t *testing.T) {
	m := NewManager(testResolver(t.TempDir()))

	var mu sync.Mutex
	var got []Session
	m.OnFinished = func(s Session) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, s)
	}

	sess, err := m.Start("proj", "echo-agent")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := m.Stop(sess.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("OnFinished called %d times, want 1", len(got))
	}
	if got[0].ID != sess.ID || got[0].State != StateFinished {
		t.Fatalf("unexpected finished session: %+v", got[0])
	}
}

func TestOnFinished_NotCalledTwiceWhenProcessAlsoExitsAfterStop(t *testing.T) {
	m := NewManager(testResolver(t.TempDir()))

	var mu sync.Mutex
	var count int
	m.OnFinished = func(s Session) {
		mu.Lock()
		defer mu.Unlock()
		count++
	}

	sess, err := m.Start("proj", "echo-agent")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := m.Stop(sess.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Give the background awaitExit goroutine time to observe the killed
	// process exiting - it must see rec.stopped and skip re-finalizing/
	// re-notifying.
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if count != 1 {
		t.Fatalf("OnFinished called %d times, want exactly 1", count)
	}
}
