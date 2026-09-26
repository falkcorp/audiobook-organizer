// file: internal/pathutil/itunes.go
// version: 1.0.0
// guid: e78a7a41-4cb6-4a20-a070-9bfce89450a6
// last-edited: 2026-09-26

package pathutil

import "strings"

// FrozenITunesSegment is the path segment that marks the hands-off Original
// iTunes tree (books/itunes/**). It is matched as a segment, not as an
// absolute root, so it catches the real library regardless of the mount
// prefix.
const FrozenITunesSegment = "books/itunes/"

// UnderFrozenITunesTree reports whether a path lives in the hands-off Original
// iTunes tree (books/itunes/**).
//
// That tree is externally managed by iTunes itself and is marked Frozen:
// read-only, never reorganised by us. Callers that PROPOSE or PERFORM
// structural changes (regroup holds, merges, moves, row writes) must consult
// this and skip such paths, because a change we are not permitted to carry
// out is noise in a human's queue at best and a data-loss invitation at worst.
//
// This is the ONE spelling of the rule. It lives in this leaf package rather
// than in internal/config so that internal/database (which config imports)
// can apply it too: database's copy-keeper choice (keeperLess) and the
// duration backfill's iTunes skip must agree on which rows are frozen, and a
// second copy of the predicate would let them drift.
func UnderFrozenITunesTree(p string) bool {
	if p == "" {
		return false
	}
	clean := strings.ReplaceAll(p, "\\", "/")
	return strings.Contains(clean+"/", FrozenITunesSegment)
}
