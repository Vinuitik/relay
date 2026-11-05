package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestContextTokensFor(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	dir := filepath.Join(cfg, "projects", "-x")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(dir, "abc-1.jsonl")
	write := func(s string) {
		if err := os.WriteFile(f, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"type":"user","message":{"content":"hi"}}
{"type":"assistant","message":{"usage":{"input_tokens":2,"cache_creation_input_tokens":100,"cache_read_input_tokens":1000}}}
{"type":"assistant","message":{"usage":{"input_tokens":3,"cache_creation_input_tokens":200,"cache_read_input_tokens":5000}}}
{"type":"user","message":{"content":"next"}}
`)
	if got := contextTokensFor("abc-1"); got != 5203 {
		t.Fatalf("tokens = %d, want 5203", got)
	}
	write(`{"type":"assistant","message":{"usage":{"input_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":9}}}` + "\n\n")
	if got := contextTokensFor("abc-1"); got != 10 {
		t.Fatalf("after growth tokens = %d, want 10", got)
	}
	if got := contextTokensFor("missing"); got != 0 {
		t.Fatalf("missing = %d, want 0", got)
	}
}
