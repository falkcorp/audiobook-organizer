// file: internal/itunes/library_shape.go
// version: 1.0.0
// guid: 2b7e9c14-6a05-4d38-9f27-8c1b3a0e5d62
// last-edited: 2026-07-23
//
// Target-shape guard for the destructive rebuild writebacks. The DB-authoritative
// /rebuild (ComputeITLDiff) and /rebuild-full (RebuildITLFromDB) were designed for a
// disposable, audiobook-only prototype library. Run against the now-reseeded REAL
// library (97,999 tracks / 357 playlists / ~86k music+podcasts) they would mark every
// non-audiobook / unmatched track for removal and shatter the playlists. The existing
// bounded-delta / K15-shrink guards catch a mass shrink, but this adds an explicit,
// fail-closed refusal keyed on what the target library actually IS — so the endpoints
// cannot be run by habit against a non-throwaway library without a deliberate override.
// See docs/specs/2026-07-23-itunes-2way-sync-continuation-findings.md §3.1.

package itunes

import (
	"fmt"
	"strings"
)

// Thresholds distinguishing the real library from a disposable audiobook-only
// prototype. The prototype had ~0 non-audiobook tracks and 14 playlists; the real
// library has ~86k non-audiobook tracks and 357 playlists. A legitimate
// audiobook-only library has ~0 non-audiobook tracks and few playlists, so these
// never false-positive on the intended use.
const (
	realLibraryNonAudiobookTrackThreshold = 1000
	realLibraryPlaylistThreshold          = 50
)

// isAudiobookITL classifies a binary ITLTrack as an audiobook, mirroring the core
// of IsAudiobook (which takes the XML *Track): Kind, Genre, and Location signals.
func isAudiobookITL(t *ITLTrack) bool {
	if t == nil {
		return false
	}
	kind := strings.ToLower(t.Kind)
	if strings.Contains(kind, "audiobook") || strings.Contains(kind, "spoken word") {
		return true
	}
	genre := strings.ToLower(t.Genre)
	if strings.Contains(genre, "audiobook") || strings.Contains(genre, "spoken") {
		return true
	}
	loc := strings.ToLower(t.Location)
	return strings.Contains(loc, "audiobooks")
}

// LibraryShape summarizes the target library for the rebuild guard.
type LibraryShape struct {
	Tracks             int  `json:"tracks"`
	NonAudiobookTracks int  `json:"non_audiobook_tracks"`
	Playlists          int  `json:"playlists"`
	LooksReal          bool `json:"looks_real"`
}

// InspectLibraryShape parses the ITL and classifies whether it "looks real" (a
// populated general-purpose library) vs a disposable audiobook-only prototype.
func InspectLibraryShape(itlPath string) (*LibraryShape, error) {
	lib, err := ParseITL(itlPath)
	if err != nil {
		return nil, fmt.Errorf("parse ITL for shape guard: %w", err)
	}
	return shapeFromLibrary(lib), nil
}

// shapeFromLibrary is the pure classification core (no I/O), split out for testing.
func shapeFromLibrary(lib *ITLLibrary) *LibraryShape {
	shape := &LibraryShape{Tracks: len(lib.Tracks), Playlists: len(lib.Playlists)}
	for i := range lib.Tracks {
		if !isAudiobookITL(&lib.Tracks[i]) {
			shape.NonAudiobookTracks++
		}
	}
	shape.LooksReal = shape.NonAudiobookTracks > realLibraryNonAudiobookTrackThreshold ||
		shape.Playlists > realLibraryPlaylistThreshold
	return shape
}

// GuardRebuildTarget refuses a destructive DB-authoritative rebuild when the target
// library looks real, unless the caller passes an explicit override. Returns a
// non-nil error describing the refusal when blocked; nil when the rebuild may
// proceed. Fail-closed: a parse error blocks (returns an error), never allows.
func GuardRebuildTarget(itlPath string, allowFullLibrary bool) (*LibraryShape, error) {
	shape, err := InspectLibraryShape(itlPath)
	if err != nil {
		return nil, err
	}
	if shape.LooksReal && !allowFullLibrary {
		return shape, fmt.Errorf(
			"refusing DB-authoritative rebuild: target library looks REAL "+
				"(%d tracks, %d non-audiobook, %d playlists) — this would remove music/podcasts "+
				"and shatter playlists. Use the edit-in-place /relocate + /cleanup-merged path instead. "+
				"To override deliberately, pass allow_full_library=true",
			shape.Tracks, shape.NonAudiobookTracks, shape.Playlists)
	}
	return shape, nil
}
