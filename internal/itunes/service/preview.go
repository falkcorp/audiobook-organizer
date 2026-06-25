// file: internal/itunes/service/preview.go
// version: 1.0.0
// guid: 5e6f7a8b-9c0d-1e2f-3a4b-5c6d7e8f9a0b
// last-edited: 2026-06-20

package itunesservice

import (
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/itunes"
)

// PreviewBook is one book a fresh import would create from a library group.
type PreviewBook struct {
	Title     string `json:"title"`
	Artist    string `json:"artist"`
	NumTracks int    `json:"num_tracks"`
	Key       string `json:"key"`
}

// GroupPreview summarizes what a fresh import would create from a parsed iTunes
// library, using the REAL groupTracksByAlbum logic, WITHOUT touching the
// database or filesystem. Used by the itunes-group-preview diagnostic and a
// future import-preview UI to answer "how many books would this become?".
type GroupPreview struct {
	TotalGroups int           `json:"total_groups"`
	MultiFile   int           `json:"multi_file"`
	SingleFile  int           `json:"single_file"`
	Books       []PreviewBook `json:"books"`
}

// PreviewGroups groups a parsed iTunes library exactly as an import would and
// returns a DB-free summary (book count + per-book track counts + the title the
// importer would assign). Title derivation mirrors buildBookFromAlbumGroup
// minus the common-parent-folder fallback (which needs on-disk path decoding),
// so the COUNT is exact and titles match for the album / agreed-stripped cases.
func PreviewGroups(library *itunes.Library) GroupPreview {
	imp := &Importer{}
	groups := imp.groupTracksByAlbum(library)
	p := GroupPreview{TotalGroups: len(groups)}
	for _, g := range groups {
		if len(g.tracks) > 1 {
			p.MultiFile++
		} else {
			p.SingleFile++
		}
		p.Books = append(p.Books, PreviewBook{
			Title:     previewTitle(g),
			Artist:    strings.TrimSpace(g.tracks[0].Artist),
			NumTracks: len(g.tracks),
			Key:       g.key,
		})
	}
	return p
}

// previewTitle mirrors buildBookFromAlbumGroup's title chain without filesystem
// access: album tag → agreed chapter-stripped title (multi-file) → stripped
// first-track name.
func previewTitle(g albumGroup) string {
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
