package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
)

// Provider is one sign-in recipe: which CLI to run, what to pick out of its
// output, and how it finishes. Two shapes exist:
//
//	PasteCode = true:  CLI prints a URL, the phone signs in, the callback page
//	                   shows a code, the user pastes it → written to stdin
//	                   (claude, gcloud)
//	PasteCode = false: device flow - CLI prints a URL + one-time code, the
//	                   user enters the code on that page, the CLI notices by
//	                   itself and exits 0 (gh)
//
// To add a provider: write a recipe here and append it to Providers().
type Provider struct {
	ID          string   // URL segment: /v1/auth/{ID}
	Name        string   // shown on the phone
	CLINames    []string // PATH lookup order; env CLIEnv overrides
	CLIEnv      string   // e.g. RELAY_CLAUDE_CLI
	InstallHint string   // "install <InstallHint>, or set <CLIEnv> ..."
	LoginArgs   []string
	// URLRe matches the sign-in URL in the CLI output (ANSI stripped).
	URLRe *regexp.Regexp
	// CodeRe (device flow only) captures the one-time code in group 1. The
	// login counts as started once both URL and code are printed.
	CodeRe    *regexp.Regexp
	PasteCode bool
	// SuccessText in the output means success even if the exit status is
	// non-zero (claude has exited 1 after a good login).
	SuccessText string
	// AfterLogin commands run (same CLI) after a successful login; any
	// failure fails the login. gh: wire git to use the new token.
	AfterLogin [][]string
	StatusArgs []string
	// ParseStatus reads the StatusArgs output (combined stdout+stderr) and
	// exit error into loggedIn + account (email/username, may be "").
	ParseStatus func(out []byte, err error) (loggedIn bool, account string)
}

// Providers is every recipe the runner knows, in the order the phone lists
// them.
func Providers() []Provider {
	return []Provider{Claude(), GitHub(), GoogleCloud()}
}

// Claude: `claude auth login` → oauth/authorize URL → paste code from the
// platform.claude.com callback page.
func Claude() Provider {
	names := []string{"claude"}
	if runtime.GOOS == "windows" {
		names = append(names, "claude.cmd", "claude.exe")
	}
	return Provider{
		ID: "claude", Name: "Claude", CLINames: names, CLIEnv: "RELAY_CLAUDE_CLI", InstallHint: "Claude Code",
		LoginArgs:   []string{"auth", "login"},
		URLRe:       loginRe,
		PasteCode:   true,
		SuccessText: "Login successful",
		StatusArgs:  []string{"auth", "status", "--json"},
		ParseStatus: func(out []byte, _ error) (bool, string) { return ParseAuthStatus(out) },
	}
}

// GitHub: `gh auth login --web` device flow, then `gh auth setup-git` so
// git (push/pull over https) uses gh's token for github.com instead of
// Git Credential Manager's desktop popup. Verified 2026-10 (gh 2.95,
// Windows): with no TTY it prints the code + URL and polls, no stdin needed.
func GitHub() Provider {
	return Provider{
		ID: "github", Name: "GitHub", CLINames: []string{"gh"}, CLIEnv: "RELAY_GH_CLI", InstallHint: "GitHub CLI (gh)",
		LoginArgs: []string{"auth", "login", "--hostname", "github.com", "--git-protocol", "https", "--web", "--scopes", "workflow"},
		URLRe:     regexp.MustCompile(`https://github\.com/login/device\S*`),
		CodeRe:    regexp.MustCompile(`one-time code: ([A-Z0-9]{4}-[A-Z0-9]{4})`),
		AfterLogin: [][]string{
			{"auth", "setup-git", "--hostname", "github.com"},
		},
		StatusArgs:  []string{"auth", "status", "--hostname", "github.com"},
		ParseStatus: ParseGHStatus,
	}
}

// GoogleCloud: `gcloud auth login --no-launch-browser` → accounts.google.com
// URL → paste the verification code. Verified 2026-10 (SDK 584, Windows).
func GoogleCloud() Provider {
	return Provider{
		ID: "gcloud", Name: "Google Cloud", CLINames: []string{"gcloud"}, CLIEnv: "RELAY_GCLOUD_CLI", InstallHint: "Google Cloud SDK",
		LoginArgs:   []string{"auth", "login", "--no-launch-browser"},
		URLRe:       regexp.MustCompile(`https://accounts\.google\.com/o/oauth2/auth\S*`),
		PasteCode:   true,
		SuccessText: "You are now logged in",
		StatusArgs:  []string{"auth", "list", "--format=json"},
		ParseStatus: ParseGcloudStatus,
	}
}

var ghAccountRe = regexp.MustCompile(`Logged in to github\.com (?:account|as) (\S+)`)

// ParseGHStatus: `gh auth status` exits 0 and names the account when logged
// in (older gh: "as <user>", newer: "account <user>").
func ParseGHStatus(out []byte, err error) (bool, string) {
	m := ghAccountRe.FindSubmatch(out)
	if err != nil || m == nil {
		return false, ""
	}
	return true, string(m[1])
}

// ParseGcloudStatus: `gcloud auth list --format=json` → the ACTIVE account.
func ParseGcloudStatus(out []byte, _ error) (bool, string) {
	s := string(out)
	if i := strings.IndexByte(s, '['); i >= 0 {
		s = s[i:]
	}
	var accts []struct{ Account, Status string }
	if json.NewDecoder(strings.NewReader(s)).Decode(&accts) != nil {
		return false, ""
	}
	for _, a := range accts {
		if a.Status == "ACTIVE" {
			return true, a.Account
		}
	}
	return false, ""
}

// findCLI resolves the provider's binary: $CLIEnv, else PATH.
func (p Provider) findCLI() (string, error) {
	if v := strings.TrimSpace(os.Getenv(p.CLIEnv)); v != "" {
		if _, err := os.Stat(v); err != nil {
			return "", fmt.Errorf("%w: %s=%q: %v", p.notFound(), p.CLIEnv, v, err)
		}
		return v, nil
	}
	for _, n := range p.CLINames {
		if path, err := exec.LookPath(n); err == nil {
			return path, nil
		}
	}
	return "", p.notFound()
}

func (p Provider) notFound() error {
	return fmt.Errorf("%s %w (install %s, or set %s to its full path)", p.CLINames[0], ErrCLINotFound, p.InstallHint, p.CLIEnv)
}
