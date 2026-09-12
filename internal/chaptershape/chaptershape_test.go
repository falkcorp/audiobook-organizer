// file: internal/chaptershape/chaptershape_test.go
// version: 1.2.0
// guid: 0b7e3d91-4c2f-4a86-9e15-6f8a2d0c7b34
// last-edited: 2026-09-12

package chaptershape

import (
	"path/filepath"
	"testing"
)

// Composed (NFC) and decomposed (NFD) spellings of the same names. A decomposed
// accent or Hangul jamo is not a letter on its own, so without NFC the two forms
// normalise differently and the prefix-in-parent guard fails.
const (
	cafeNFC   = "Café"
	cafeNFD   = "Café"
	koreaNFC  = "한국"
	koreaNFD  = "한국"
	chapterOf = " - 1"
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
		{filepath.Join("lib", "--", "-- - 1", "a.mp3"), false},
		// Non-Latin titles: Cyrillic and CJK chapter folders are chapter
		// folders too. An ASCII-only normaliser turned these prefixes into ""
		// and rejected them.
		{filepath.Join("lib", "incoming", "Сияние", "Сияние - 1", "58.MP3"), true},
		{filepath.Join("lib", "incoming", "Сияние", "Сияние - 11", "58.MP3"), true},
		{filepath.Join("lib", "三体", "三体 - 2", "a.mp3"), true},
		{filepath.Join("lib", "ノルウェイの森", "ノルウェイの森 - 3", "a.mp3"), true},
		// Non-Latin series volumes: the parent is the author, not the book.
		{filepath.Join("lib", "Автор", "Сияние - 1", "a.mp3"), false},
		{filepath.Join("lib", "刘慈欣", "三体 - 2", "a.mp3"), false},
		// Book folder and chapter folder in different Unicode forms, both ways.
		{filepath.Join("lib", cafeNFC, cafeNFD+chapterOf, "a.mp3"), true},
		{filepath.Join("lib", cafeNFD, cafeNFC+chapterOf, "a.mp3"), true},
		{filepath.Join("lib", koreaNFC, koreaNFD+chapterOf, "a.mp3"), true},
		{filepath.Join("lib", koreaNFD, koreaNFC+chapterOf, "a.mp3"), true},
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

func TestNormPrefix(t *testing.T) {
	cases := map[string]string{
		"The Shining":    "theshining",
		"Сияние":         "сияние",
		"СИЯНИЕ - Том 2": "сияниетом2",
		"三体":             "三体",
		"--":             "",
		"  - . ":         "",
		cafeNFC:          "café",
		cafeNFD:          "café",
		koreaNFD:         koreaNFC,
	}
	for in, want := range cases {
		if got := NormPrefix(in); got != want {
			t.Errorf("NormPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}
