// file: internal/util/normalize.go
// version: 1.1.0
// guid: a3f7e2d1-9b4c-4e8a-b6f0-2c5d7a1e3f9b
// last-edited: 2026-09-12

// Package util provides shared string and path normalization helpers.
package util

import (
	"path/filepath"
	"strings"
	"unicode"
)

// NormalizePath cleans a filepath and lowercases it for consistent comparison.
func NormalizePath(p string) string {
	return strings.ToLower(filepath.Clean(p))
}

// NormalizeTitle trims whitespace and lowercases a title for comparison.
func NormalizeTitle(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// NormalizeAuthor trims whitespace, collapses internal whitespace runs, and
// lowercases an author name for comparison.
//
// It is the key of the Pebble name indexes author:name:, author_alias:name:,
// narrator_name: and series:name:, so "Raymond  L.  Weil" and "Raymond L. Weil"
// resolve to one row. Every whitespace rune unicode.IsSpace recognizes (tab,
// newline, NBSP, U+2000..U+200A, U+3000, ...) collapses to one ASCII space.
//
// Index entries written before 2026-09-12 are keyed by NormalizeAuthorLegacy,
// which did not collapse; the store's lookups fall back to that key, and its
// index deletes check ownership, because the two forms now overlap.
func NormalizeAuthor(s string) string {
	return CollapseSpaces(strings.ToLower(s))
}

// NormalizeAuthorLegacy is the author-name normalization used before
// 2026-09-12: trim and lowercase, with internal whitespace left as it was.
//
// It exists ONLY so index entries written under the old key stay reachable
// (read-compat in internal/database/pebble_store_name_index.go). Do not use it
// for anything new. It goes away with the re-key of the legacy index entries,
// which is deliberately deferred until maintenance.author-whitespace-collision-report
// has been reviewed.
func NormalizeAuthorLegacy(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// NormalizeString trims whitespace and lowercases any generic string.
func NormalizeString(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// CollapseSpaces replaces runs of whitespace with a single space and trims.
func CollapseSpaces(s string) string {
	var b strings.Builder
	prevSpace := false
	for _, r := range strings.TrimSpace(s) {
		if unicode.IsSpace(r) {
			if !prevSpace {
				b.WriteRune(' ')
			}
			prevSpace = true
		} else {
			b.WriteRune(r)
			prevSpace = false
		}
	}
	return b.String()
}
