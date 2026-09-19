//go:build windows

package project

import "os"

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
