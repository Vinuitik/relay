package uplink

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultHub is the hub `runnerd pair` uses when neither --hub nor RELAY_HUB is set.
const DefaultHub = "https://habittrackerdima.me"

// Claim is a pending QR pairing: the hub hands out ID (goes into the QR link)
// and PollSecret (stays on this machine, proves the poller started the claim).
type Claim struct {
	ID         string
	PollSecret string
	ExpiresIn  time.Duration
}

// URL is the link the phone opens to confirm the claim.
func (c *Claim) URL(hub string) string {
	return strings.TrimRight(hub, "/") + "/relay/claim/" + url.PathEscape(c.ID)
}

// ErrClaimGone means the claim was declined on the phone, expired or is unknown to the hub.
var ErrClaimGone = fmt.Errorf("pairing was declined, expired or the hub restarted - run pair again for a new QR")

// StartClaim opens a QR pairing at POST <hub>/relay/pair/start.
func StartClaim(ctx context.Context, client *http.Client, hub, name string) (*Claim, error) {
	if err := CheckHubURL(hub); err != nil {
		return nil, err
	}
	status, raw, err := postJSON(ctx, client, strings.TrimRight(hub, "/")+"/relay/pair/start", map[string]string{"name": name})
	if err != nil {
		return nil, fmt.Errorf("pair: %w", err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("pair: hub returned %d: %s", status, strings.TrimSpace(string(raw)))
	}
	var out struct {
		ClaimID          string `json:"claimId"`
		PollSecret       string `json:"pollSecret"`
		ExpiresInSeconds int    `json:"expiresInSeconds"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.ClaimID == "" || out.PollSecret == "" {
		return nil, fmt.Errorf("pair: unexpected hub response: %s", strings.TrimSpace(string(raw)))
	}
	return &Claim{ID: out.ClaimID, PollSecret: out.PollSecret, ExpiresIn: time.Duration(out.ExpiresInSeconds) * time.Second}, nil
}

// WaitForApproval polls POST <hub>/relay/pair/poll every `every` until the
// phone confirms (returns the hub.json content), declines (ErrClaimGone) or
// ctx ends. Network errors and unexpected statuses are retried: the phone
// step can take a while and a blip shouldn't lose the pairing.
func WaitForApproval(ctx context.Context, client *http.Client, hub string, c *Claim, every time.Duration) (*Config, error) {
	hub = strings.TrimRight(hub, "/")
	body := map[string]string{"claimId": c.ID, "pollSecret": c.PollSecret}
	for {
		status, raw, err := postJSON(ctx, client, hub+"/relay/pair/poll", body)
		switch {
		case err == nil && status == http.StatusOK:
			var out struct {
				DeviceID string `json:"deviceId"`
				Token    string `json:"token"`
			}
			if err := json.Unmarshal(raw, &out); err != nil || out.DeviceID == "" || out.Token == "" {
				return nil, fmt.Errorf("pair: unexpected hub response: %s", strings.TrimSpace(string(raw)))
			}
			return &Config{Hub: hub, DeviceID: out.DeviceID, Token: out.Token}, nil
		case err == nil && status == http.StatusNotFound:
			return nil, ErrClaimGone
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("pair: no confirmation from the phone in time - run pair again for a new QR")
		case <-time.After(every):
		}
	}
}

// WaitForConfig polls dir/hub.json until a valid pairing appears or ctx
// ends, so a running runnerd starts its uplink right after `runnerd pair`
// (a separate process) writes the file - no restart. Unreadable or
// half-written files are retried on the next tick.
func WaitForConfig(ctx context.Context, dir string, every time.Duration) (*Config, error) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-t.C:
		}
		if c, err := Load(dir); err == nil && c != nil {
			return c, nil
		}
	}
}

func postJSON(ctx context.Context, client *http.Client, target string, in any) (int, []byte, error) {
	if client == nil {
		client = http.DefaultClient
	}
	body, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, raw, nil
}
