package uplink

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Pair redeems a one-time pairing code at POST <hub>/relay/pair and returns
// the resulting hub.json content. It does not write the file; see Save.
func Pair(ctx context.Context, client *http.Client, hub, code, name string) (*Config, error) {
	if err := CheckHubURL(hub); err != nil {
		return nil, err
	}
	if code == "" || name == "" {
		return nil, fmt.Errorf("pair: code and name are required")
	}
	if client == nil {
		client = http.DefaultClient
	}
	hub = strings.TrimRight(hub, "/")
	body, _ := json.Marshal(map[string]string{"code": code, "name": name})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hub+"/relay/pair", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pair: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	switch {
	case resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("pair: hub rejected the code (%d) - it is wrong, expired or already used; get a new one in the browser", resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("pair: hub returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		DeviceID string `json:"deviceId"`
		Token    string `json:"token"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("pair: parse hub response: %w", err)
	}
	if out.DeviceID == "" || out.Token == "" {
		return nil, fmt.Errorf("pair: hub response missing deviceId or token")
	}
	return &Config{Hub: hub, DeviceID: out.DeviceID, Token: out.Token}, nil
}
