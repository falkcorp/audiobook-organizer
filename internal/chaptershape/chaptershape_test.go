// file: internal/chaptershape/chaptershape_test.go
// version: 1.0.0
// guid: 0b7e3d91-4c2f-4a86-9e15-6f8a2d0c7b34
// last-edited: 2026-09-12

package chaptershape

import (
	"path/filepath"
	"testing"
)

func TestIsChapterFolderFile(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{filepath.Join("lib", "The Shining", "The Shining - 1", "58.MP3"), true},
		{filepath.Join("lib", "The Shining", "The Shining - 11", "58.MP3"), true},
		{filepath.Join("lib", "Cage of Souls - Cage of Souls", "Cage of Souls - 3", "a.mp3"), true},
		// Series volume: the prefix is not in the parent folder's name.
		{filepath.Join("lib", "Some Author", "Discworld - 3", "a.mp3"), false},
		// Flat dump: no " - N" folder.
		{filepath.Join("lib", "abooks", "Throne of Jade 01", "a.mp3"), false},
		{filepath.Join("lib", "incoming", "one", "x.mp3"), false},
		// Prefix that normalises to nothing.
		{filepath.Join("lib", "Book", "-- - 2", "a.mp3"), false},
	}
	for _, c := range cases {
		if _, _, got := IsChapterFolderFile(c.path); got != c.want {
			t.Errorf("IsChapterFolderFile(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestParts(t *testing.T) {
	parent, prefix, n, ok := Parts(filepath.Join("lib", "Book", "Book - 07", "f.mp3"))
	if !ok || parent != filepath.Join("lib", "Book") || prefix != "Book" || n != 7 {
		t.Fatalf("Parts = %q %q %d %v", parent, prefix, n, ok)
	}
}
