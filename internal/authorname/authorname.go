// file: internal/authorname/authorname.go
// version: 1.4.0
// guid: 5e2b7c14-9a36-4f81-b0d7-2c93e845af60
// last-edited: 2026-09-01

// Package authorname holds the one author name the system writes to mean "we
// could not resolve an author".
//
// It exists as its own package because four packages had independently
// hardcoded the same literal and could not see each other: internal/organizer
// (the path fallback that names the directory), internal/maintenance/jobs (a
// path prefix to sweep), internal/quarantine (the same fallback for failed
// files), and -- by reading it back out of a filename -- both internal/scanner
// and internal/metadata. A literal duplicated across packages that never import
// one another cannot be kept in step, and the cost of it drifting is that a book
// silently acquires the placeholder as a real author.
//
// This package once unified the LITERAL only, and carried a NOTE that the
// filename/directory author parsers themselves still existed twice, in
// internal/scanner and internal/metadata, as divergent copies. That is CLOSED:
// ExtractAuthorFromDirectory and ParseFilenameForAuthor now live here, in
// parse.go, with one implementation each. See that file for what the two copies
// actually differed on -- measured, and smaller than it looked.
//
// looksLikePersonName was on that list too and left earlier: all copies of it
// (and of isValidAuthor, and of dedup's looksLikeAuthorName) live in
// internal/personname.
//
// THE LIST IS NOT EMPTY. A first draft of this comment said "nothing remains",
// which was false and is exactly the sentence that stops the next person
// looking. internal/metadata/folder_parser.go is a THIRD path->author parser --
// in the same package as one of the two collapsed above -- and it is live in
// production via scanner.go and internal/importer. It has its own container-skip
// map and its own shape predicate, looksLikeAuthorSegment, which diverges from
// personname.LooksLikePersonName on named input classes: 2-5 words rather than
// 2-4, an ASCII-only 'A'..'Z' first-letter test that drops every caseless script
// personname deliberately keeps, and early returns on "," and " & " that skip the
// shape check entirely.
//
// Its skip map now carries the placeholder, so the specific harm this package
// exists to prevent is closed on that path too. The PREDICATE divergence is not,
// and unifying it changes answers on the comma / "&" / five-word classes, so it
// needs its own differential corpus rather than a compile-and-green. Tracked in
// todo.d/20260901-folder-parser-shape-predicate-diverges.md.
//
// Depends only on the standard library and internal/personname, which is itself
// a standard-library-only leaf, so any package may still import this one
// without risking a cycle.
package authorname

import "strings"

// Placeholder is the author name used when a book has no resolvable author.
//
// It is the directory a book gets filed under and, because the organizer's
// naming scheme includes the author, it also ends up inside the filename. That
// round trip is why IsPlaceholder exists: anything reading metadata back out of
// a path must be able to recognise the system's own "unknown" marker rather
// than treating it as an author a human supplied.
const Placeholder = "Unknown Author"

// IsPlaceholder reports whether name is the placeholder rather than a real
// author.
//
// Comparison is case-insensitive and ignores surrounding whitespace: the value
// being tested has usually made a round trip through a file path, where case
// and padding are not preserved reliably.
func IsPlaceholder(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), Placeholder)
}
