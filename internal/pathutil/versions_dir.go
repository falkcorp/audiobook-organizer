// file: internal/pathutil/versions_dir.go
// version: 1.0.0
// guid: b5145e37-5347-4308-b396-dcb2bbd4c873
// last-edited: 2026-10-30

package pathutil

import (
	"path/filepath"
	"strings"
)

// VersionsDirName is the hidden directory inside a book's folder where the
// files of its non-active versions live: <book folder>/.versions/<version id>/.
// internal/versions re-exports it (versions.VersionsDirName); it is declared
// here, below both internal/database and internal/versions, so the two can
// share one definition of what a version slot is.
const VersionsDirName = ".versions"

// UnderVersionsDir reports whether path lies inside a .versions/ directory that
// sits below folder: path is within folder (on a component boundary) and at
// least one component of the part below folder is VersionsDirName. A
// .versions component in folder itself, or above it, does not count: the
// question is whether the file is one of the folder's version slots, and a book
// folder that happens to live under a .versions tree is not a slot of itself.
//
// Neither argument is cleaned or resolved. A caller that must not be fooled by
// a symlinked slot directory resolves both first (versions.inVersionSlot does).
func UnderVersionsDir(path, folder string) bool {
	rest, ok := CutPathPrefix(path, folder)
	if !ok {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(rest), "/") {
		if part == VersionsDirName {
			return true
		}
	}
	return false
}

// HasSlotPair reports whether p has the consecutive path components
// ".versions" and verID, the slot of version verID. It is a lexical test: it
// does not resolve symlinks (see UnderVersionsDir). An empty verID never
// matches.
func HasSlotPair(p, verID string) bool {
	if verID == "" {
		return false
	}
	parts := strings.Split(filepath.ToSlash(filepath.Clean(p)), "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == VersionsDirName && parts[i+1] == verID {
			return true
		}
	}
	return false
}
