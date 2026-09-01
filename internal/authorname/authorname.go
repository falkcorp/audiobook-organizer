// file: internal/authorname/authorname.go
// version: 1.2.0
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
// NOTE: this unifies the LITERAL only. The filename/directory author parser
// itself STILL exists twice, in internal/scanner and internal/metadata, as
// divergent copies: parseFilenameForAuthor (scanner.go:1865, metadata.go:852)
// and extractAuthorFromDirectory (scanner.go:1809, metadata.go:806). Verified
// still duplicated 2026-09-01. Until they are collapsed, a fix to one is not a
// fix to the other, which is exactly how the first version of this change came
// to be inert on the path that produces the bug.
//
// looksLikePersonName WAS on that list and is no longer: all copies of it (and
// of isValidAuthor, and of dedup's looksLikeAuthorName) now live in
// internal/personname. The two parsers above are what remain.
//
// Depends on nothing but the standard library, so any package may import it.
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
