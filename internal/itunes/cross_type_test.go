// file: internal/itunes/cross_type_test.go
// version: 1.0.0
// guid: 6d1f8b40-2a95-4c73-8e60-3b7a1c9e0d58
// last-edited: 2026-07-24

package itunes

import (
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// pidHexToBytes turns an 8-byte upper-hex PID string into the [8]byte the ITL
// track carries, so a synthetic ITLTrack maps to a book_file's PID via pidToHex.
func pidHexToBytes(hex string) [8]byte {
	var b [8]byte
	for i := 0; i < 8 && i*2+1 < len(hex); i++ {
		var v int
		for _, c := range hex[i*2 : i*2+2] {
			v <<= 4
			switch {
			case c >= '0' && c <= '9':
				v |= int(c - '0')
			case c >= 'A' && c <= 'F':
				v |= int(c-'A') + 10
			case c >= 'a' && c <= 'f':
				v |= int(c-'a') + 10
			}
		}
		b[i] = byte(v)
	}
	return b
}

// TestComputeCrossTypeCollisions exercises the 2×2 cross-tab and, critically, the
// non-audiobook-track-owned-by-AO collision cell (the relocate disjointness hazard).
func TestComputeCrossTypeCollisions(t *testing.T) {
	primary := true

	// Sanity: pidToHex(pidHexToBytes(x)) round-trips to x, so the fake store's PIDs
	// line up with the synthetic tracks.
	if got := strings.ToUpper(pidToHex(pidHexToBytes("AABBCCDD11223344"))); got != "AABBCCDD11223344" {
		t.Fatalf("pid round-trip broken: %q", got)
	}

	store := &pidCensusMock{
		files: []database.BookFileCore{
			bf("f_ok", "b_ok", "/mnt/x/audiobooks/ok.m4b", "AAAAAAAA00000001"),   // owns an audiobook track
			bf("f_bad", "b_bad", "/mnt/x/music/song.mp3", "BBBBBBBB00000002"),    // owns a MUSIC track → collision
		},
		books: map[string]*database.Book{
			"b_ok":  {ID: "b_ok", IsPrimaryVersion: &primary},
			"b_bad": {ID: "b_bad", Title: "Mislabeled", IsPrimaryVersion: &primary},
		},
	}

	tracks := []ITLTrack{
		// audiobook track, AO-owned → ab_owned
		{PersistentID: pidHexToBytes("AAAAAAAA00000001"), Genre: "Audiobooks", Name: "Chapter 1"},
		// audiobook track, NOT AO-owned → ab_unowned
		{PersistentID: pidHexToBytes("CCCCCCCC00000003"), Genre: "Audiobook", Name: "Orphan chapter"},
		// MUSIC track, AO-owned → COLLISION (non_ab_owned)
		{PersistentID: pidHexToBytes("BBBBBBBB00000002"), Genre: "Rock", Kind: "MPEG audio file", Name: "Some Song"},
		// MUSIC track, NOT AO-owned → non_ab_unowned (hands-off, correct)
		{PersistentID: pidHexToBytes("DDDDDDDD00000004"), Genre: "Podcast", Name: "Episode 5"},
	}

	r, err := computeCrossTypeCollisions(store, tracks)
	if err != nil {
		t.Fatalf("computeCrossTypeCollisions: %v", err)
	}

	if r.TracksInITL != 4 {
		t.Errorf("TracksInITL = %d, want 4", r.TracksInITL)
	}
	if r.AudiobookTracks != 2 || r.NonAudiobookTracks != 2 {
		t.Errorf("audiobook/non split = %d/%d, want 2/2", r.AudiobookTracks, r.NonAudiobookTracks)
	}
	if r.ABOwned != 1 || r.ABUnowned != 1 || r.NonABOwned != 1 || r.NonABUnowned != 1 {
		t.Errorf("cells = ab_owned=%d ab_unowned=%d non_ab_owned=%d non_ab_unowned=%d, want 1/1/1/1",
			r.ABOwned, r.ABUnowned, r.NonABOwned, r.NonABUnowned)
	}
	if r.CrossTypeCollisions != 1 {
		t.Errorf("CrossTypeCollisions = %d, want 1", r.CrossTypeCollisions)
	}
	if r.CollisionsLivePrimaryOwner != 1 {
		t.Errorf("CollisionsLivePrimaryOwner = %d, want 1", r.CollisionsLivePrimaryOwner)
	}
	if len(r.Samples) != 1 || r.Samples[0].OwnerBookTitle != "Mislabeled" {
		t.Errorf("expected 1 sample for 'Mislabeled', got %+v", r.Samples)
	}
}
