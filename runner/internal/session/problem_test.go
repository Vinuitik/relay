package session

import (
	"errors"
	"strings"
	"testing"

	"relay/runner/internal/acp"
)

func TestProblemKind(t *testing.T) {
	cases := []struct {
		name string
		err  error
		text string
		want string
	}{
		{"acp auth code", &acp.RPCError{Code: -32000, Message: "Authentication required"}, "", KindAuth},
		{"expired token error", errors.New("API Error: 401 OAuth token has expired"), "", KindAuth},
		{"login hint reply", nil, "Invalid API key · Please run /login", KindAuth},
		{"limit reply", nil, "You've hit your limit · resets 5pm (Europe/London)", KindQuota},
		{"limit error", errors.New("Claude AI usage limit reached|1759507200"), "", KindQuota},
		{"ordinary reply", nil, "Done - tests pass.", ""},
		{"ordinary error", errors.New("broken pipe"), "", ""},
		{"long reply mentioning limits", nil, "The usage limit middleware " + strings.Repeat("x", 400), ""},
	}
	for _, c := range cases {
		if got := problemKind(c.err, c.text); got != c.want {
			t.Errorf("%s: problemKind = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestMarkProblemReplyOnlyLooksAtThisTurn(t *testing.T) {
	msgs := []Message{
		{Role: "user", Text: "hi"},
		{Role: "agent", Text: "You've hit your limit · resets 5pm"},
		{Role: "user", Text: "again"},
		{Role: "agent", Text: "Sure, done."},
	}
	markProblemReply(msgs)
	if msgs[1].Kind != "" || msgs[3].Kind != "" {
		t.Errorf("kinds = %q, %q; want both empty", msgs[1].Kind, msgs[3].Kind)
	}
	markProblemReply(msgs[:2])
	if msgs[1].Kind != KindQuota {
		t.Errorf("kind = %q, want %q", msgs[1].Kind, KindQuota)
	}
}
