// file: internal/itunes/service/grouping_test.go
// version: 1.0.0
// guid: 3c4d5e6f-7a8b-9c0d-1e2f-3a4b5c6d7e8f
// last-edited: 2026-06-19

package itunesservice

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/itunes"
)

// ab builds an audiobook track (Kind set so IsAudiobook is true).
func ab(name, artist, album string, trackNum int) *itunes.Track {
	return &itunes.Track{
		Kind:        "Audiobook",
		Name:        name,
		Artist:      artist,
		Album:       album,
		TrackNumber: trackNum,
		Location:    "file://localhost/W:/itunes/iTunes%20Media/Audiobooks/x/" + name + ".mp3",
	}
}

func lib(tracks ...*itunes.Track) *itunes.Library {
	m := make(map[string]*itunes.Track, len(tracks))
	for i, t := range tracks {
		m[string(rune('a'+i))+t.Name] = t
	}
	return &itunes.Library{Tracks: m}
}

// groupSizes returns a multiset of group track-counts, order-independent so the
// assertion survives the non-deterministic map iteration in groupTracksByAlbum.
func groupSizes(groups []albumGroup) map[int]int {
	out := map[int]int{}
	for _, g := range groups {
		out[len(g.tracks)]++
	}
	return out
}

func TestGroupTracksByAlbum_Fragmentation(t *testing.T) {
	imp := &Importer{}

	t.Run("multi-author anthology merges into one book", func(t *testing.T) {
		// "Wild Cards I": one album, per-story authors, DISTINCT track numbers.
		l := lib(
			ab("Strings", "Stephen Leigh", "Wild Cards I", 17),
			ab("The Long Sleep", "Michael Cassutt", "Wild Cards I", 3),
			ab("Interlude", "Roger Zelazny", "Wild Cards I", 9),
		)
		got := imp.groupTracksByAlbum(l)
		if len(got) != 1 {
			t.Fatalf("anthology: want 1 group, got %d: %+v", len(got), groupSizes(got))
		}
		if len(got[0].tracks) != 3 {
			t.Fatalf("anthology: want 3 tracks in the group, got %d", len(got[0].tracks))
		}
	})

	t.Run("empty-album part chapters merge into one book", func(t *testing.T) {
		// "Aces Abroad - Part NN": no album, single author, bare part suffix.
		l := lib(
			ab("Aces Abroad - Part 1", "George R. R. Martin", "", 1),
			ab("Aces Abroad - Part 2", "George R. R. Martin", "", 2),
			ab("Aces Abroad - Part 3", "George R. R. Martin", "", 3),
		)
		got := imp.groupTracksByAlbum(l)
		if len(got) != 1 {
			t.Fatalf("parts: want 1 group, got %d: %+v", len(got), groupSizes(got))
		}
	})

	t.Run("distinct albums stay separate", func(t *testing.T) {
		l := lib(
			ab("Ch1", "Author X", "Book A", 1),
			ab("Ch1", "Author X", "Book B", 1),
		)
		got := imp.groupTracksByAlbum(l)
		if len(got) != 2 {
			t.Fatalf("distinct albums: want 2 groups, got %d", len(got))
		}
	})

	t.Run("series volume titles stay separate (Book N not a chapter)", func(t *testing.T) {
		// Empty album; "Book N" is a series volume and must NOT collapse.
		l := lib(
			ab("Eternal Dominion, Book 1", "Author Y", "", 0),
			ab("Eternal Dominion, Book 2", "Author Y", "", 0),
		)
		got := imp.groupTracksByAlbum(l)
		if len(got) != 2 {
			t.Fatalf("series volumes: want 2 groups, got %d: %+v", len(got), groupSizes(got))
		}
	})

	t.Run("over-merge guard: same album, repeated track numbers, splits by artist", func(t *testing.T) {
		// Two books erroneously sharing one album string, each numbered 1..2.
		// Repeated track numbers + differing artists => split back apart.
		l := lib(
			ab("A-Ch1", "Author P", "Shared Collection", 1),
			ab("A-Ch2", "Author P", "Shared Collection", 2),
			ab("B-Ch1", "Author Q", "Shared Collection", 1),
			ab("B-Ch2", "Author Q", "Shared Collection", 2),
		)
		got := imp.groupTracksByAlbum(l)
		if len(got) != 2 {
			t.Fatalf("over-merge guard: want 2 groups, got %d: %+v", len(got), groupSizes(got))
		}
	})

	t.Run("over-merge guard: generic shared album, no track numbers, splits by artist", func(t *testing.T) {
		// Distinct single-file books sharing a generic album with UNSET track
		// numbers must not silently merge into one book.
		l := lib(
			ab("The First Book", "Author P", "Audiobook", 0),
			ab("A Different Book", "Author Q", "Audiobook", 0),
		)
		got := imp.groupTracksByAlbum(l)
		if len(got) != 2 {
			t.Fatalf("generic album guard: want 2 groups, got %d: %+v", len(got), groupSizes(got))
		}
	})

	t.Run("single-artist multi-file with unset track numbers stays merged", func(t *testing.T) {
		// One book, one author, album set, track numbers unset — only one artist
		// so there is no safe split signal; must stay merged.
		l := lib(
			ab("Chapter One", "Solo Author", "My Book", 0),
			ab("Chapter Two", "Solo Author", "My Book", 0),
		)
		got := imp.groupTracksByAlbum(l)
		if len(got) != 1 {
			t.Fatalf("single-artist merge: want 1 group, got %d: %+v", len(got), groupSizes(got))
		}
	})
}

func TestAgreedStrippedTitle(t *testing.T) {
	cases := []struct {
		name   string
		tracks []*itunes.Track
		want   string
	}{
		{
			// CONS-17b: every chapter strips to the same title → use it, so the
			// book is NOT mistakenly titled after the flat author folder.
			name: "all parts agree",
			tracks: []*itunes.Track{
				ab("Aces Abroad - Part 1", "GRRM", "", 1),
				ab("Aces Abroad - Part 2", "GRRM", "", 2),
				ab("Aces Abroad - Part 14", "GRRM", "", 14),
			},
			want: "Aces Abroad",
		},
		{
			// Generic per-chapter names that disagree → no agreed title (caller
			// falls back to the folder name).
			name: "generic chapter names disagree",
			tracks: []*itunes.Track{
				ab("Opening Credits", "Narrator", "", 1),
				ab("Big Finish Ident", "Narrator", "", 2),
			},
			want: "",
		},
		{
			name:   "single track strips its own name",
			tracks: []*itunes.Track{ab("The Hobbit - Part 3", "Tolkien", "", 3)},
			want:   "The Hobbit",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := agreedStrippedTitle(tc.tracks); got != tc.want {
				t.Errorf("agreedStrippedTitle = %q, want %q", got, tc.want)
			}
		})
	}
}
