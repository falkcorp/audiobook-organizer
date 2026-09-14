// file: internal/itunes/album_artist_test.go
// version: 1.0.0
// guid: 5c2d8e41-9a3f-4b17-8e6c-0d4f2a7b9e13
// last-edited: 2026-09-14

package itunes

import "testing"

// Album Artist is the author (owner decision 2026-09-14); the importers read
// it as the narrator before, disagreeing with the file-tag readers.
func TestAuthorAndNarrator_AlbumArtistIsTheAuthor(t *testing.T) {
	cases := []struct {
		name                 string
		artist, albumArtist  string
		wantAuthor, wantNarr string
	}{
		{"both set and differ", "Rob Inglis", "J.R.R. Tolkien", "J.R.R. Tolkien", "Rob Inglis"},
		{"same name", "J.R.R. Tolkien", "J.R.R. Tolkien", "J.R.R. Tolkien", ""},
		{"album artist only", "", "J.R.R. Tolkien", "J.R.R. Tolkien", ""},
		{"artist only", "J.R.R. Tolkien", "", "J.R.R. Tolkien", ""},
		{"neither", "", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			author, narrator := AuthorAndNarrator(&Track{Artist: tc.artist, AlbumArtist: tc.albumArtist})
			if author != tc.wantAuthor || narrator != tc.wantNarr {
				t.Fatalf("AuthorAndNarrator(artist=%q, albumArtist=%q) = (%q, %q), want (%q, %q)",
					tc.artist, tc.albumArtist, author, narrator, tc.wantAuthor, tc.wantNarr)
			}
		})
	}
	if a, n := AuthorAndNarrator(nil); a != "" || n != "" {
		t.Fatalf("AuthorAndNarrator(nil) = (%q, %q), want empty", a, n)
	}
}
