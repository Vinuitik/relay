package session

import (
	"errors"
	"strings"

	"relay/runner/internal/acp"
)

// Message.Kind values for agent messages the phone renders as a card
// instead of a plain bubble.
const (
	// KindQuota: the provider's subscription quota is used up (Claude's
	// 5-hour / weekly limit). Nothing to do but wait or upgrade.
	KindQuota = "quota"
	// KindAuth: the provider's login is missing or expired - fixable from
	// the phone via the re-login flow (see runner/FLOWS.md "Provider login").
	KindAuth = "auth"
)

// ACP's "authentication required" error code (agentclientprotocol.com,
// RequestError.authRequired).
const acpAuthRequired = -32000

var (
	quotaMarkers = []string{
		"usage limit",
		"hit your limit",
		"limit reached",
		"out of extra usage",
	}
	authMarkers = []string{
		"authentication required",
		"authentication_error",
		"oauth token has expired",
		"token has expired",
		"please run /login",
		"not logged in",
		"invalid api key",
		"invalid bearer token",
	}
)

// maxProblemText caps how long an agent's own (non-error) reply may be and
// still count as a quota/auth notice. Providers report these as one short
// line; a long reply that merely mentions "usage limit" is a real answer.
const maxProblemText = 300

// problemKind classifies an agent error (or the agent's own final text) as
// a quota or auth problem; "" when it's neither. Plain substring matching
// on the provider's wording - see runner/FLOWS.md Technology Notes for why
// that's fragile.
func problemKind(err error, text string) string {
	var rpc *acp.RPCError
	if errors.As(err, &rpc) && rpc.Code == acpAuthRequired {
		return KindAuth
	}
	if err != nil {
		text = err.Error()
	} else if len(text) > maxProblemText {
		return ""
	}
	t := strings.ToLower(text)
	switch {
	case containsAny(t, authMarkers):
		return KindAuth
	case containsAny(t, quotaMarkers):
		return KindQuota
	}
	return ""
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// LastProblem is the Kind of the latest turn's last agent message - what the
// turn that just finished ended on. Stops at the user's message so an old
// turn's problem is never reported again.
func (s Session) LastProblem() string {
	for i := len(s.Messages) - 1; i >= 0 && s.Messages[i].Role != "user"; i-- {
		if s.Messages[i].Role == "agent" {
			return s.Messages[i].Kind
		}
	}
	return ""
}
