//go:build !windows

package project

// rootsPlaceholder is what BrowseDir reports as the "current path" when
// listing filesystem roots (no real path is being browsed yet).
const rootsPlaceholder = "/"

func listRoots() []DirEntry {
	return []DirEntry{{Name: "/"}}
}
