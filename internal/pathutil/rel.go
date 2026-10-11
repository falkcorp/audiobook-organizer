// file: internal/pathutil/rel.go
// version: 1.0.0
// guid: 48d1cfe0-ff1d-4603-9d78-71c39d09e6e2
// last-edited: 2026-10-11

package pathutil

import (
	"path/filepath"
	"strings"
)

// RelEscapesBase reports whether rel, a filepath.Rel result, names a path
// outside its base: ".." itself or anything under ".." plus a separator.
//
// A name that merely begins with two dots ("..Extras", "...") is a child of the
// base, not a climb out of it. Until 2026-10-11 the Deluge importers
// (internal/deluge/import.go, maintenance bulk-deluge-import) tested
// rel[:2] == ".." and so treated a library directory named "..Extras" as
// outside the root, while deluge.centralize and deluge.link-backfill tested
// the component. A file there was "in the library" to one and "outside" to the
// other: the importers would copy it to the top of the root. Every one of them
// now calls this.
func RelEscapesBase(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// IsUnderRoot reports whether path lies strictly inside root (root itself is
// not under root), comparing cleaned paths component by component. An empty
// root or path is never under anything.
func IsUnderRoot(root, path string) bool {
	if root == "" || path == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != "." && !filepath.IsAbs(rel) && !RelEscapesBase(rel)
}
