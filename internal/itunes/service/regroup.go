// file: internal/itunes/service/regroup.go
// version: 1.0.0
// guid: 1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d
// last-edited: 2026-06-20

package itunesservice

import (
	"sort"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/itunes"
)

// HealGroup is one target book the FIXED importer grouping (groupTracksByAlbum,
// CONS-FRAG / PR #1528) would produce from a library, identified by its canonical
// title and the iTunes PIDs of every track that belongs to it. It is the unit of
// truth for the in-place re-group heal: each group's PIDs' BookFiles are gathered
// onto a single book.
type HealGroup struct {
	Title string
	PIDs  []string
}

// GroupLibraryForHeal groups a parsed iTunes library exactly as a fresh import
// would and returns, per resulting book, its canonical title and the sorted PIDs
// of its tracks. It is pure and DB-free.
//
// Output is fully deterministic — groups sorted by (title, joined PIDs) and PIDs
// sorted lexically — so repeated runs over the same library produce an identical
// plan even though itunes.Library.Tracks iterates in non-deterministic map order.
// Tracks without a PID cannot be healed (nothing to reassign) and are dropped;
// a group left with no PIDs is omitted.
func GroupLibraryForHeal(library *itunes.Library) []HealGroup {
	imp := &Importer{}
	groups := imp.groupTracksByAlbum(library)

	out := make([]HealGroup, 0, len(groups))
	for _, g := range groups {
		pids := make([]string, 0, len(g.tracks))
		for _, t := range g.tracks {
			if pid := strings.TrimSpace(t.PersistentID); pid != "" {
				pids = append(pids, pid)
			}
		}
		if len(pids) == 0 {
			continue
		}
		sort.Strings(pids)
		out = append(out, HealGroup{Title: healTitle(g), PIDs: pids})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Title != out[j].Title {
			return out[i].Title < out[j].Title
		}
		return strings.Join(out[i].PIDs, ",") < strings.Join(out[j].PIDs, ",")
	})
	return out
}

// healTitle mirrors buildBookFromAlbumGroup's title chain without filesystem
// access: album tag → agreed chapter-stripped title (multi-file) → stripped
// first-track name.
func healTitle(g albumGroup) string {
	first := g.tracks[0]
	if title := strings.TrimSpace(first.Album); title != "" {
		return title
	}
	if len(g.tracks) > 1 {
		if agreed := agreedStrippedTitle(g.tracks); agreed != "" {
			return agreed
		}
	}
	return stripChapterSuffix(stripChapterPrefix(strings.TrimSpace(first.Name)))
}
