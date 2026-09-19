// file: internal/database/pebble_store_books_under_dir_test.go
// version: 1.0.0
// guid: 0d0bd7bb-5685-4cf5-b575-b201a2d264df
// last-edited: 2026-09-19

package database

import (
	"maps"
	"testing"
)

// LiveBookPathsUnderDir lists every live book whose FilePath lies beneath
// dir (any depth, never dir itself or a sibling that merely shares the
// prefix), from the index once it is built and from a full scan before, and
// the two agree.
func TestLiveBookPathsUnderDir(t *testing.T) {
	s := newAtPathStore(t)
	mk := func(path string) string {
		b, err := s.CreateBook(&Book{Title: "t", FilePath: path})
		if err != nil {
			t.Fatalf("CreateBook: %v", err)
		}
		return b.ID
	}
	a := mk("/lib/A/Tale/01 - Tale.mp3")
	b := mk("/lib/A/Tale/02 - Tale.mp3")
	c := mk("/lib/A/Tale/Disc 2/01 - Tale.mp3")
	mk("/lib/A/Tale 2/01 - Other.mp3") // shares the prefix "Tale", not the dir
	mk("/lib/A/Other/01 - x.mp3")
	gone := mk("/lib/A/Tale/03 - Tale.mp3")
	yes := true
	if _, err := s.ModifyBook(gone, func(x *Book) error { x.MarkedForDeletion = &yes; return nil }); err != nil {
		t.Fatalf("ModifyBook: %v", err)
	}
	want := map[string]string{
		a: "/lib/A/Tale/01 - Tale.mp3",
		b: "/lib/A/Tale/02 - Tale.mp3",
		c: "/lib/A/Tale/Disc 2/01 - Tale.mp3",
	}

	resetAtPathIndex(t, s)
	scan, err := s.LiveBookPathsUnderDir("/lib/A/Tale")
	if err != nil || !maps.Equal(scan, want) {
		t.Fatalf("full-scan answer = %v (%v), want %v", scan, err, want)
	}
	mustBackfill(t, s)
	idx, err := s.LiveBookPathsUnderDir("/lib/A/Tale")
	if err != nil || !maps.Equal(idx, want) {
		t.Fatalf("index answer = %v (%v), want %v", idx, err, want)
	}
	// A trailing slash names the same directory.
	if got, _ := s.LiveBookPathsUnderDir("/lib/A/Tale/"); !maps.Equal(got, want) {
		t.Fatalf("trailing slash answer = %v", got)
	}
}
