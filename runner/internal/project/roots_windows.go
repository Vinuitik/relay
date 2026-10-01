//go:build windows

package project

import (
	"os"

	"golang.org/x/sys/windows"
)

// rootsPlaceholder is what BrowseDir reports as the "current path" when
// listing filesystem roots (no real path is being browsed yet).
const rootsPlaceholder = ""

// listRoots lists drive letters A:\ through Z:\ that actually exist, by
// probing each rather than calling into a Windows-specific syscall - stdlib
// only, matches the rest of this codebase's cross-compile constraints.
func listRoots() []DirEntry {
	var out []DirEntry
	for c := 'A'; c <= 'Z'; c++ {
		drive := string(c) + `:\`
		if info, err := os.Stat(drive); err == nil && info.IsDir() {
			out = append(out, DirEntry{Name: drive})
		}
	}
	return out
}

// knownDocumentsDir asks Windows where Documents is - it may be redirected
// (e.g. into OneDrive), so %USERPROFILE%\Documents isn't reliable.
func knownDocumentsDir() string {
	d, err := windows.KnownFolderPath(windows.FOLDERID_Documents, 0)
	if err != nil {
		return ""
	}
	return d
}
