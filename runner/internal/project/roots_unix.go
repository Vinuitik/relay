//go:build !windows

package project

import (
	"os"
	"path/filepath"
	"strings"
)

// rootsPlaceholder is what BrowseDir reports as the "current path" when
// listing filesystem roots (no real path is being browsed yet).
const rootsPlaceholder = "/"

func listRoots() []DirEntry {
	return []DirEntry{{Name: "/"}}
}

// knownDocumentsDir reads XDG_DOCUMENTS_DIR from ~/.config/user-dirs.dirs
// (set on desktop Linux, possibly localized); "" if absent.
func knownDocumentsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(home, ".config")
	}
	data, err := os.ReadFile(filepath.Join(cfg, "user-dirs.dirs"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		v, ok := strings.CutPrefix(strings.TrimSpace(line), "XDG_DOCUMENTS_DIR=")
		if !ok {
			continue
		}
		v = strings.ReplaceAll(strings.Trim(v, `"`), "$HOME", home)
		if filepath.IsAbs(v) && v != home {
			return v
		}
	}
	return ""
}
