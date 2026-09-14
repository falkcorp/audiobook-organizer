// file: internal/metadata/album_artist_guard_test.go
// version: 1.0.0
// guid: 8f4b1a6d-2e7c-4d93-a05b-6c1e9f3d2b78
// last-edited: 2026-09-14

package metadata

import "testing"

// A file whose ALBUMARTIST holds its own narrator (organize wrote that
// between da064ef4c and c81b39801 on 2026-09-13) must not read the narrator
// back as the author when ARTIST names the author.
func TestBuildMetadataFromTaglibMap_AlbumArtistHoldingNarratorIsNotTheAuthor(t *testing.T) {
	md := BuildMetadataFromTaglibMap(map[string][]string{
		"TITLE":       {"The Hobbit"},
		"ALBUMARTIST": {"Rob Inglis"},
		"ARTIST":      {"J.R.R. Tolkien"},
		"NARRATOR":    {"Rob Inglis"},
	}, "/x/The Hobbit.m4b", nil)
	if md.Artist != "J.R.R. Tolkien" {
		t.Fatalf("author = %q, want %q (ALBUMARTIST equals the narrator tag)", md.Artist, "J.R.R. Tolkien")
	}
	if md.Narrator != "Rob Inglis" {
		t.Fatalf("narrator = %q, want %q", md.Narrator, "Rob Inglis")
	}
}

// ALBUMARTIST is the author whenever it is not the file's narrator.
func TestBuildMetadataFromTaglibMap_AlbumArtistIsTheAuthor(t *testing.T) {
	md := BuildMetadataFromTaglibMap(map[string][]string{
		"ALBUMARTIST": {"J.R.R. Tolkien"},
		"ARTIST":      {"Rob Inglis"},
	}, "/x/The Hobbit.m4b", nil)
	if md.Artist != "J.R.R. Tolkien" {
		t.Fatalf("author = %q, want %q", md.Artist, "J.R.R. Tolkien")
	}
	if md.Narrator != "Rob Inglis" {
		t.Fatalf("narrator = %q, want %q (ARTIST falls back to narrator)", md.Narrator, "Rob Inglis")
	}
}

func TestAlbumArtistIsNarrator(t *testing.T) {
	cases := []struct {
		albumArtist, artist, narrator string
		want                          bool
	}{
		{"Rob Inglis", "J.R.R. Tolkien", "Rob Inglis", true},
		{"rob inglis ", "J.R.R. Tolkien", "Rob Inglis", true},
		{"J.R.R. Tolkien", "Rob Inglis", "Rob Inglis", false},
		{"Rob Inglis", "", "Rob Inglis", false},           // no other author to use
		{"Rob Inglis", "Rob Inglis", "Rob Inglis", false}, // author reads own book
		{"J.R.R. Tolkien", "J.R.R. Tolkien", "", false},   // no narrator tag
		{"", "J.R.R. Tolkien", "Rob Inglis", false},
	}
	for _, tc := range cases {
		if got := AlbumArtistIsNarrator(tc.albumArtist, tc.artist, tc.narrator); got != tc.want {
			t.Errorf("AlbumArtistIsNarrator(%q, %q, %q) = %v, want %v", tc.albumArtist, tc.artist, tc.narrator, got, tc.want)
		}
	}
}
