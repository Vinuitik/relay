package uplink

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWaitForApprovalDeclined(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	_, err := WaitForApproval(context.Background(), nil, srv.URL, &Claim{ID: "x", PollSecret: "y"}, time.Millisecond)
	if !errors.Is(err, ErrClaimGone) {
		t.Errorf("err = %v", err)
	}
}

func TestWaitForApprovalRetriesHubErrorsUntilDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway) // e.g. Cloudflare while the hub restarts
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := WaitForApproval(ctx, nil, srv.URL, &Claim{ID: "x", PollSecret: "y"}, 5*time.Millisecond)
	if err == nil || errors.Is(err, ErrClaimGone) {
		t.Errorf("err = %v", err)
	}
}

func TestClaimURL(t *testing.T) {
	if got := (&Claim{ID: "AB2"}).URL("https://hub.example.com/"); got != "https://hub.example.com/relay/claim/AB2" {
		t.Errorf("URL = %q", got)
	}
}

func TestWaitForConfigPicksUpLaterPairing(t *testing.T) {
	dir := t.TempDir()
	go func() {
		time.Sleep(20 * time.Millisecond)
		Save(dir, Config{Hub: "https://hub.example.com", DeviceID: "d", Token: "t"})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c, err := WaitForConfig(ctx, dir, 5*time.Millisecond)
	if err != nil || c.Token != "t" {
		t.Fatalf("c = %+v, err = %v", c, err)
	}
}
