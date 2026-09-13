// Package history implements weekly history cleanup: finished sessions
// older than a cutoff are dropped, per ARCHITECTURE.md's "History is
// cleaned weekly" note.
package history

import "time"

// DefaultMaxAge is the retention window from ARCHITECTURE.md (finished
// sessions older than a week are purged). Configurable, not hardcoded
// end-to-end: callers may pass any duration to compute their own cutoff.
const DefaultMaxAge = 7 * 24 * time.Hour

// SessionInfo is the minimal view PurgeOlderThan needs. It's a plain struct
// (rather than importing the session package) so this package stays
// dependency-free and trivially unit-testable.
type SessionInfo struct {
	ID         string
	State      string // only "finished" and "error" sessions are ever purged
	FinishedAt *time.Time
}

// PurgeOlderThan returns the subset of sessions to keep: a session that
// isn't finished/error is always kept (it's still live); a finished/error
// session is kept only if it finished at or after cutoff.
func PurgeOlderThan(sessions []SessionInfo, cutoff time.Time) []SessionInfo {
	kept := make([]SessionInfo, 0, len(sessions))
	for _, s := range sessions {
		if !isTerminal(s.State) {
			kept = append(kept, s)
			continue
		}
		// No FinishedAt on a terminal session shouldn't happen, but keep
		// it rather than silently dropping data we can't safely age out.
		if s.FinishedAt == nil || !s.FinishedAt.Before(cutoff) {
			kept = append(kept, s)
		}
	}
	return kept
}

func isTerminal(state string) bool {
	return state == "finished" || state == "error"
}
