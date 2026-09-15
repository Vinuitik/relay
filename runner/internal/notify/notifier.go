package notify

import "log"

// Notifier sends a push notification to a registered device when a session
// finishes, or when this runner is about to suspend to S5.
type Notifier interface {
	NotifySessionFinished(device Device, session Session) error
	// NotifyRunnerSuspending tells device this runner is about to power
	// off, so the app can show "X is going to sleep" instead of the chat
	// screen just going silent with no explanation. Called from
	// idle.Monitor's BeforeShutdown hook (see cmd/runnerd/main.go) -
	// best-effort and fire-and-forget by design: a failed or dropped push
	// here must never block or retry against a machine that's already
	// mid-shutdown.
	NotifyRunnerSuspending(device Device, hostname string) error
}

// noopNotifier is used whenever RELAY_FCM_CREDENTIALS is unset or unreadable.
// It never errors and never blocks the caller - per ARCHITECTURE.md, an
// unconfigured Firebase project must degrade to "log and skip", not crash
// or fail the session lifecycle that triggered it.
type noopNotifier struct{}

func (noopNotifier) NotifySessionFinished(Device, Session) error {
	log.Printf("FCM not configured, skipping notification")
	return nil
}

func (noopNotifier) NotifyRunnerSuspending(Device, string) error {
	log.Printf("FCM not configured, skipping suspend notification")
	return nil
}

// NewNotifier builds a Notifier from the credential file path in
// RELAY_FCM_CREDENTIALS. If the env var is unset, or the file can't be
// read/parsed, it returns a safe no-op Notifier instead of an error - the
// caller should never have to treat "FCM isn't set up yet" as fatal.
func NewNotifier(credPath string) Notifier {
	if credPath == "" {
		return noopNotifier{}
	}

	cred, err := loadCredentials(credPath)
	if err != nil {
		log.Printf("FCM not configured, skipping notification (credential file %q: %v)", credPath, err)
		return noopNotifier{}
	}

	notifier, err := newFCMNotifier(cred)
	if err != nil {
		log.Printf("FCM not configured, skipping notification (credential file %q: %v)", credPath, err)
		return noopNotifier{}
	}
	return notifier
}
