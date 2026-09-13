// file: internal/plugins/maintenance/path_prefix.go
// version: 1.0.0
// guid: 7c3e91a4-58d2-4b6f-a0e7-2d94f1b86c35
// last-edited: 2026-09-13

package maintenance

import "github.com/falkcorp/audiobook-organizer/internal/pathutil"

// pathPrefixMatches reports whether path is in scope for a maintenance op's
// PathPrefix param. It is the single filter shared by mark-missing-files,
// missing-file-repoint, missing-file-repair, missing-file-audit and
// merge-same-path-dupes, so all five scope a sweep identically.
//
// The match is on a path-component boundary (pathutil.IsWithin): "/lib" matches
// "/lib" and "/lib/a.m4b" but NOT "/lib2/a.m4b", which a bare strings.HasPrefix
// wrongly swept in. A trailing separator on the prefix ("/lib/") is accepted.
//
// An empty prefix means "no filter" and matches everything. That is the
// opposite of pathutil.IsWithin, which rejects an empty root, so the guard lives
// here rather than being repeated at each call site.
//
// The prefix is deliberately NOT cleaned or trimmed: stored paths are not
// cleaned either, and pathutil.IsWithin compares bytes as given. Trimming would
// also turn a whitespace-only prefix, which used to match nothing, into "match
// everything" — a scope widening on ops that write.
func pathPrefixMatches(path, prefix string) bool {
	return prefix == "" || pathutil.IsWithin(path, prefix)
}
