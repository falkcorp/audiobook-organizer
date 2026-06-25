// file: internal/itunes/service/preview_test.go
// version: 1.0.0
// guid: 7a8b9c0d-1e2f-3a4b-5c6d-7e8f9a0b1c2d
// last-edited: 2026-06-20

package itunesservice

import "testing"

func TestPreviewGroups(t *testing.T) {
	// Anthology (constant album, per-story artist, distinct track numbers) →
	// one multi-file book titled after the album.
	l := lib(
		ab("Strings", "Stephen Leigh", "Wild Cards I", 17),
		ab("The Long Sleep", "Michael Cassutt", "Wild Cards I", 3),
		// A separate empty-album part-book.
		ab("Aces Abroad - Part 1", "GRRM", "", 1),
		ab("Aces Abroad - Part 2", "GRRM", "", 2),
	)
	p := PreviewGroups(l)
	if p.TotalGroups != 2 {
		t.Fatalf("TotalGroups = %d, want 2", p.TotalGroups)
	}
	if p.MultiFile != 2 || p.SingleFile != 0 {
		t.Fatalf("multi=%d single=%d, want multi=2 single=0", p.MultiFile, p.SingleFile)
	}
	titles := map[string]int{}
	for _, b := range p.Books {
		titles[b.Title] = b.NumTracks
	}
	if titles["Wild Cards I"] != 2 {
		t.Errorf("anthology title/tracks = %v, want Wild Cards I:2", titles)
	}
	if titles["Aces Abroad"] != 2 {
		t.Errorf("parts title/tracks = %v, want Aces Abroad:2 (stripped)", titles)
	}
}
