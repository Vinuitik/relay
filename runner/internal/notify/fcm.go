package notify

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"
)

// fcmScope is the OAuth2 scope required to call FCM's HTTP v1 send API.
const fcmScope = "https://www.googleapis.com/auth/firebase.messaging"

// credentials is the subset of a Google service-account JSON key file this
// package needs. Google's file has more fields (client_id, auth_uri, ...)
// that are irrelevant to the JWT-bearer flow used here, so they're ignored.
type credentials struct {
	Type        string `json:"type"`
	ProjectID   string `json:"project_id"`
	PrivateKey  string `json:"private_key"`
	ClientEmail string `json:"client_email"`
	TokenURI    string `json:"token_uri"`
}

// loadCredentials reads and parses a service-account JSON key file at path.
func loadCredentials(path string) (*credentials, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cred credentials
	if err := json.Unmarshal(data, &cred); err != nil {
		return nil, fmt.Errorf("parse credentials JSON: %w", err)
	}
	if cred.ProjectID == "" || cred.PrivateKey == "" || cred.ClientEmail == "" {
		return nil, errors.New("credentials JSON missing project_id/private_key/client_email")
	}
	if cred.TokenURI == "" {
		cred.TokenURI = "https://oauth2.googleapis.com/token"
	}
	return &cred, nil
}

// fcmNotifier is the real Notifier. It authenticates via a hand-rolled
// service-account JWT-bearer OAuth2 exchange (RFC 7523) rather than pulling
// in golang.org/x/oauth2 - this keeps the runner's dependency graph at
// stdlib-only (matching the "single static binary, no runtime deps"
// decision in ARCHITECTURE.md) and avoids relying on module-fetch access at
// build time for a rarely-exercised code path.
type fcmNotifier struct {
	cred       *credentials
	privateKey *rsa.PrivateKey
	httpClient *http.Client

	mu          sync.Mutex
	accessToken string
	expiresAt   time.Time
}

func newFCMNotifier(cred *credentials) (*fcmNotifier, error) {
	key, err := parsePrivateKey(cred.PrivateKey)
	if err != nil {
		return nil, err
	}
	return &fcmNotifier{
		cred:       cred,
		privateKey: key,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}, nil
}

func parsePrivateKey(pemStr string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("no PEM block found in private_key")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	keyAny, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	key, ok := keyAny.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("private_key is not an RSA key")
	}
	return key, nil
}

// NotifySessionFinished sends a data-only FCM message to device telling it
// session has finished. Errors are returned to the caller, which - per the
// wiring in internal/session - logs and continues rather than failing the
// session transition.
func (n *fcmNotifier) NotifySessionFinished(device Device, session Session) error {
	token, err := n.accessTokenFor()
	if err != nil {
		return fmt.Errorf("obtain FCM access token: %w", err)
	}

	msg := map[string]any{
		"message": map[string]any{
			"token": device.FCMToken,
			"data": map[string]string{
				"type":      "session_finished",
				"sessionId": session.ID,
				"projectId": session.ProjectID,
			},
		},
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal FCM message: %w", err)
	}

	sendURL := fmt.Sprintf("https://fcm.googleapis.com/v1/projects/%s/messages:send", n.cred.ProjectID)
	req, err := http.NewRequest(http.MethodPost, sendURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := n.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send FCM message: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("FCM send failed: %s: %s", resp.Status, string(respBody))
	}
	return nil
}

// accessTokenFor returns a cached OAuth2 access token, refreshing it via
// the JWT-bearer grant if it's missing or close to expiry.
func (n *fcmNotifier) accessTokenFor() (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.accessToken != "" && time.Now().Before(n.expiresAt.Add(-1*time.Minute)) {
		return n.accessToken, nil
	}

	token, expiresIn, err := n.exchangeJWT()
	if err != nil {
		return "", err
	}
	n.accessToken = token
	n.expiresAt = time.Now().Add(time.Duration(expiresIn) * time.Second)
	return token, nil
}

// exchangeJWT signs a service-account JWT assertion and exchanges it for an
// OAuth2 access token per RFC 7523 / Google's server-to-server auth flow.
func (n *fcmNotifier) exchangeJWT() (accessToken string, expiresIn int, err error) {
	now := time.Now()
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	claims := map[string]any{
		"iss":   n.cred.ClientEmail,
		"scope": fcmScope,
		"aud":   n.cred.TokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(1 * time.Hour).Unix(),
	}

	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", 0, err
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", 0, err
	}

	signingInput := base64URLEncode(headerJSON) + "." + base64URLEncode(claimsJSON)

	hashed := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, n.privateKey, crypto.SHA256, hashed[:])
	if err != nil {
		return "", 0, fmt.Errorf("sign JWT: %w", err)
	}

	jwt := signingInput + "." + base64URLEncode(sig)

	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
	form.Set("assertion", jwt)

	resp, err := n.httpClient.PostForm(n.cred.TokenURI, form)
	if err != nil {
		return "", 0, fmt.Errorf("token exchange request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, err
	}
	if resp.StatusCode >= 300 {
		return "", 0, fmt.Errorf("token exchange failed: %s: %s", resp.Status, string(respBody))
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(respBody, &tokenResp); err != nil {
		return "", 0, fmt.Errorf("parse token response: %w", err)
	}
	if tokenResp.AccessToken == "" {
		return "", 0, errors.New("token response missing access_token")
	}
	return tokenResp.AccessToken, tokenResp.ExpiresIn, nil
}

func base64URLEncode(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}
