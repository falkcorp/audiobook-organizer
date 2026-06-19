// file: internal/pathutil/abbreviate.go
// version: 1.0.0
// guid: 4a7d2e91-3c58-4b06-9f2a-1d8e6b07c534
// last-edited: 2026-06-19

// Package pathutil renders filesystem paths in a short, readable form for the
// UI by replacing known library roots with literal $(var) tokens. The same
// rules are mirrored by the frontend formatter (web/src/utils/formatPath.ts);
// PathVars is the single source of truth for the root values both sides use.
package pathutil

import (
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// PathVar is a named library root used for abbreviation. Value is the absolute
// path prefix; Name is the short token shown in its place (e.g. "libroot").
type PathVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Abbreviate replaces the most-specific matching root in p with a literal
// $(name) token. vars are checked in the order given, so callers must list the
// most-specific root first (e.g. libroot before books, since libroot is nested
// under books). Empty-valued vars are skipped so they never match everything.
// Paths matching no var are returned unchanged.
func Abbreviate(p string, vars []PathVar) string {
	for _, v := range vars {
		if v.Value == "" {
			continue
		}
		if p == v.Value {
			return "$(" + v.Name + ")"
		}
		if strings.HasPrefix(p, v.Value+"/") {
			return "$(" + v.Name + ")" + p[len(v.Value):]
		}
	}
	return p
}

// PathVars returns the configured library roots in match order (most-specific
// first): libroot = config RootDir, books = its parent directory.
func PathVars() []PathVar {
	root := strings.TrimRight(config.AppConfig.RootDir, "/")
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

// AbbreviatePath abbreviates p using the configured library roots.
func AbbreviatePath(p string) string {
	return Abbreviate(p, PathVars())
}
