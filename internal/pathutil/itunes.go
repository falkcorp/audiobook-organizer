// file: internal/pathutil/itunes.go
// version: 1.1.0
// guid: e78a7a41-4cb6-4a20-a070-9bfce89450a6
// last-edited: 2026-09-26

package pathutil

import "strings"

// FrozenITunesSegment names the hands-off Original iTunes tree
// (books/itunes/**) in messages and refusal labels. Matching is done by
// UnderFrozenITunesTree, not by searching for this string.
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
// THE RULE. The path, split on '/' or '\', has a segment equal to "itunes"
// (case-insensitive, surrounding spaces ignored) whose preceding segment ends
// in "books" (case-insensitive). So it matches:
//
//   - /mnt/bigdata/books/itunes/... (the real library);
//   - Books/iTunes/..., BOOKS/ITUNES (any case: a case-sensitive match missed
//     these);
//   - /x/audiobooks/itunes/... (a "books" suffix: a whole-segment-only match
//     missed this);
//   - books/itunes itself, and Windows separators.
//
// and does NOT match a folder merely named "iTunes" elsewhere (an audiobook
// titled "iTunes"), "itunes" as a substring of a longer segment
// ("books/itunes-backup/"), or "iTunes Media" on its own. Two predicates used
// to disagree here: this one (case-sensitive substring "books/itunes/") and
// author-path-link's (case-insensitive, whole segment "books" then
// "itunes"). This is their union, and it is the ONE spelling of the rule.
//
// It lives in this leaf package rather than in internal/config so that
// internal/database (which config imports) can apply it too: database's
// copy-keeper choice (keeperLess) and the duration backfill's iTunes skip
// must agree on which rows are frozen.
//
// The "iTunes Media" ghost-path checks (merge.IsITunesGhostPath and
// fs-regroup-xml's) are a DIFFERENT rule, about the iTunes media layout, and
// do not use this.
func UnderFrozenITunesTree(p string) bool {
	if p == "" {
		return false
	}
	segs := strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' })
	for i := 1; i < len(segs); i++ {
		if strings.EqualFold(strings.TrimSpace(segs[i]), "itunes") &&
			strings.HasSuffix(strings.ToLower(strings.TrimSpace(segs[i-1])), "books") {
			return true
		}
	}
	return false
}
