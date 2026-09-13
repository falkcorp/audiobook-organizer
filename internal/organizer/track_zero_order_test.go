// file: internal/organizer/track_zero_order_test.go
// version: 1.0.0
// guid: 8d2f4a6c-1e3b-4f5a-9c7d-0b2e4f6a8c13
// last-edited: 2026-09-13

package organizer

import (
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TrackNumber 0 is the "no number" sentinel, not "first". A book with a
// track-0 file beside files numbered 1..N plans the numbered files first, in
// track order, and names the track-0 file by its position (N+1), last. The
// manual track edit on PATCH /audiobooks/:id/files/:file_id accepts 0, so this
// pins what that value actually does to the rename: to put a file first,
// number it 1 and move the others up.
func TestPlanTargetPaths_TrackZeroSortsLastByPosition(t *testing.T) {
	files := []database.BookFile{
		// Caller order deliberately puts the track-0 file first, and its name
		// sorts first too: neither may pull it ahead of the numbered files.
		{ID: "intro", FilePath: "/src/book/00 intro.mp3", TrackNumber: 0},
		{ID: "t2", FilePath: "/src/book/b.mp3", TrackNumber: 2},
		{ID: "t1", FilePath: "/src/book/c.mp3", TrackNumber: 1},
		{ID: "t3", FilePath: "/src/book/a.mp3", TrackNumber: 3},
	}
	entries := mustCompute(t, "/root", "{author}", "{title} - {track:02d}", files, PathVars{Author: "Author", Title: "Book"})

	want := []struct{ id, base string }{
		{"t1", "Book - 01.mp3"},
		{"t2", "Book - 02.mp3"},
		{"t3", "Book - 03.mp3"},
		{"intro", "Book - 04.mp3"},
	}
	if len(entries) != len(want) {
		t.Fatalf("want %d entries, got %d: %+v", len(want), len(entries), entries)
	}
	for i, w := range want {
		if entries[i].SegmentID != w.id || filepath.Base(entries[i].TargetPath) != w.base {
			t.Fatalf("entry %d = %s -> %s, want %s -> %s", i, entries[i].SegmentID, filepath.Base(entries[i].TargetPath), w.id, w.base)
		}
	}
}

// The way to put a file first: number it 1 and the rest 2..N+1. The plan
// follows the numbers exactly.
func TestPlanTargetPaths_RenumberedIntroSortsFirst(t *testing.T) {
	files := []database.BookFile{
		{ID: "t1", FilePath: "/src/book/c.mp3", TrackNumber: 2},
		{ID: "intro", FilePath: "/src/book/z intro.mp3", TrackNumber: 1},
		{ID: "t2", FilePath: "/src/book/b.mp3", TrackNumber: 3},
	}
	entries := mustCompute(t, "/root", "{author}", "{title} - {track:02d}", files, PathVars{Author: "Author", Title: "Book"})
	want := []struct{ id, base string }{
		{"intro", "Book - 01.mp3"},
		{"t1", "Book - 02.mp3"},
		{"t2", "Book - 03.mp3"},
	}
	for i, w := range want {
		if entries[i].SegmentID != w.id || filepath.Base(entries[i].TargetPath) != w.base {
			t.Fatalf("entry %d = %s -> %s, want %s -> %s", i, entries[i].SegmentID, filepath.Base(entries[i].TargetPath), w.id, w.base)
		}
	}
}
