package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestVerifyChecksum(t *testing.T) {
	content := []byte("fake binary contents")
	sum := sha256.Sum256(content)
	hexSum := hex.EncodeToString(sum[:])

	t.Run("matches", func(t *testing.T) {
		sumFile := []byte(hexSum + "  relay-runner-linux-amd64\nabc123  some-other-file\n")
		called := false
		orig := download
		download = func(url string) ([]byte, error) { called = true; return sumFile, nil }
		defer func() { download = orig }()

		if err := verifyChecksum(content, "http://example/checksums.txt", "relay-runner-linux-amd64"); err != nil {
			t.Fatalf("verifyChecksum: %v", err)
		}
		if !called {
			t.Fatal("expected download to be called")
		}
	})

	t.Run("mismatch", func(t *testing.T) {
		sumFile := []byte("deadbeef  relay-runner-linux-amd64\n")
		orig := download
		download = func(url string) ([]byte, error) { return sumFile, nil }
		defer func() { download = orig }()

		if err := verifyChecksum(content, "http://example/checksums.txt", "relay-runner-linux-amd64"); err == nil {
			t.Fatal("expected checksum mismatch error, got nil")
		}
	})

	t.Run("no matching entry", func(t *testing.T) {
		sumFile := []byte(hexSum + "  some-other-file\n")
		orig := download
		download = func(url string) ([]byte, error) { return sumFile, nil }
		defer func() { download = orig }()

		if err := verifyChecksum(content, "http://example/checksums.txt", "relay-runner-linux-amd64"); err == nil {
			t.Fatal("expected 'no checksum entry' error, got nil")
		}
	})
}
