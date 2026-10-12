// file: internal/pathutil/abbreviate.go
// version: 1.0.2
// guid: 4a7d2e91-3c58-4b06-9f2a-1d8e6b07c534
// last-edited: 2026-10-12

// Package pathutil renders filesystem paths in a short, readable form for the
// UI by replacing known library roots with literal $(var) tokens. The same
// rules are mirrored by the frontend formatter (web/src/utils/formatPath.ts);
// PathVars is the single source of truth for the root values both sides use.
package pathutil

import "strings"

// PathVar is a named library root used for abbreviation. Value is the absolute
// path prefix; Name is the short token shown in its place (e.g. "libroot").
type PathVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// PathVars returns the library roots in match order (most-specific first):
// libroot = rootDir (the configured RootDir), books = its parent directory.
//
// rootDir is a parameter rather than a read of config.AppConfig so pathutil
// stays a leaf package: config imports database, and database uses pathutil's
// containment helpers, so pathutil importing config would be a cycle.
func PathVars(rootDir string) []PathVar {
	root := strings.TrimRight(rootDir, "/")
	if root == "" {
		return nil
	}
	books := root
	if i := strings.LastIndex(root, "/"); i > 0 {
		books = root[:i]
	}
	return []PathVar{
		{Name: "libroot", Value: root},
		{Name: "books", Value: books},
	}
}
