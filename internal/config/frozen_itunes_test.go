// file: internal/config/frozen_itunes_test.go
// version: 1.0.0
// guid: 5b1f8c47-2a93-4e06-bd75-8f2c4a1e903d
// last-edited: 2026-08-05

package config

import "testing"

// 🔴 books/itunes/** is the externally-managed Original library, marked Frozen and
// read-only. Producers that PROPOSE structural changes must skip it: 561 of 777
// ambiguous regroup holds were iTunes AUTHOR folders, because that layout puts an
// author's whole catalogue in one directory and a folder-grouping classifier reads
// a shared folder as a shared book. Every one of those proposals was both wrong
// (distinct novels, 8-29h each) and unactionable (a tree we may not reorganise).
func TestUnderFrozenITunesTree(t *testing.T) {
	frozen := []string{
		"/mnt/bigdata/books/itunes/iTunes Media/Audiobooks/Shirtaloon, Travis Deverell/x.m4b",
		"/mnt/bigdata/books/itunes/iTunes Media/Audiobooks/Tamryn Tamer",
		"books/itunes/anything",
		`W:\books\itunes\iTunes Media\Audiobooks\A\b.m4b`, // windows separators
	}
	for _, p := range frozen {
		if !UnderFrozenITunesTree(p) {
			t.Errorf("UnderFrozenITunesTree(%q) = false, want true", p)
		}
	}

	// The organized trees are ours and must stay eligible — excluding them would
	// silently disable regrouping for the books we CAN fix.
	ours := []string{
		"/mnt/bigdata/books/audiobook-organizer/Author/Book/01.m4b",
		"/mnt/bigdata/books/abooks/imported/Rysa Walker/x.mp3",
		"/mnt/bigdata/books/newbooks/audiobooks/four hex/01.mp3",
		"",
	}
	for _, p := range ours {
		if UnderFrozenITunesTree(p) {
			t.Errorf("UnderFrozenITunesTree(%q) = true, want false", p)
		}
	}
}
