// file: internal/applygate/booknum_test.go
// version: 1.1.0
// guid: 5e8a0b17-4c3d-4f92-a6e1-2d9c7b0f8e35
// last-edited: 2026-09-13

package applygate

import (
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/seqnum"
)

// TestCheckSeriesNumberLost uses titles from the 2026-09-13 prod preview
// plus near misses. A number is LOST only when the result keeps it nowhere:
// kept in the new title or as the series position of a matching series is
// neutral; kept as the position of a differently named series blocks as
// series_renamed.
func TestCheckSeriesNumberLost(t *testing.T) {
	title := []string{"title"}
	cases := []struct {
		name       string
		book       database.Book
		cand       metafetch.MetadataCandidate
		overwrites []string
		want       string
	}{
		{
			name:       "kept as the position of a renamed series: Empire of Man 04 -> Prince Roger #4",
			book:       database.Book{Title: "Empire of Man 04 - We Few"},
			cand:       metafetch.MetadataCandidate{Title: "We Few", Series: "Prince Roger", SeriesPosition: "4"},
			overwrites: title, want: ReasonSeriesRenamed,
		},
		{
			name:       "number standing alone between segments, no series",
			book:       database.Book{Title: "The Legends of the First Empire - 3 - Age of War"},
			cand:       metafetch.MetadataCandidate{Title: "Age of War"},
			overwrites: title, want: ReasonSeriesNumberLost,
		},
		{
			name:       "Book N marker kept as the matching series position",
			book:       database.Book{Title: "Into the Void: Sentenced to War, Book 14"},
			cand:       metafetch.MetadataCandidate{Title: "Into the Void", Series: "Sentenced to War", SeriesPosition: "14"},
			overwrites: title,
		},
		{
			name:       "leading 00.5 kept as position 0.5",
			book:       database.Book{Title: "00.5 Tin Man"},
			cand:       metafetch.MetadataCandidate{Title: "Tin Man", Series: "Galaxy's Edge", SeriesPosition: "0.5"},
			overwrites: title,
		},
		{
			name:       "zero-padded 04 equals position 4",
			book:       database.Book{Title: "Dungeon Crawler Carl 04 - The Gate of the Feral Gods"},
			cand:       metafetch.MetadataCandidate{Title: "The Gate of the Feral Gods", Series: "Dungeon Crawler Carl", SeriesPosition: "4"},
			overwrites: title,
		},
		{
			name:       "kept under the book's own series when the candidate gives none",
			book:       database.Book{Title: "Big Cats, Book 3", Series: &database.Series{Name: "Big Cats"}, SeriesPositionRaw: strp("3")},
			cand:       metafetch.MetadataCandidate{Title: "Lions"},
			overwrites: title,
		},
		{
			name:       "marker number, different position: lost",
			book:       database.Book{Title: "Big Cats, Book 3"},
			cand:       metafetch.MetadataCandidate{Title: "Lions", Series: "Big Cats", SeriesPosition: "5"},
			overwrites: title, want: ReasonSeriesNumberLost,
		},
		{
			name:       "file-derived trailing track number is not a volume",
			book:       database.Book{Title: "The Hobbit - 01"},
			cand:       metafetch.MetadataCandidate{Title: "The Hobbit"},
			overwrites: title,
		},
		{
			name:       "trailing number corroborated by the stored position, then dropped",
			book:       database.Book{Title: "Big Cats 3", SeriesPositionRaw: strp("3")},
			cand:       metafetch.MetadataCandidate{Title: "Lions", Series: "Zoo", SeriesPosition: "7"},
			overwrites: title, want: ReasonSeriesNumberLost,
		},
		{
			name:       "1984 is a year, not a volume",
			book:       database.Book{Title: "1984"},
			cand:       metafetch.MetadataCandidate{Title: "Nineteen Eighty-Four"},
			overwrites: title,
		},
		{
			name:       "Catch-22 kept by the candidate",
			book:       database.Book{Title: "Catch-22"},
			cand:       metafetch.MetadataCandidate{Title: "Catch-22", Subtitle: "50th Anniversary Edition"},
			overwrites: title,
		},
		{
			name:       "Fahrenheit 451 kept by the candidate",
			book:       database.Book{Title: "Fahrenheit 451 (Unabridged)"},
			cand:       metafetch.MetadataCandidate{Title: "Fahrenheit 451"},
			overwrites: title,
		},
		{
			name:       "sequence kept in the series field, not the title",
			book:       database.Book{Title: "Dune", Series: &database.Series{Name: "Dune Chronicles"}, SeriesPositionRaw: strp("1")},
			cand:       metafetch.MetadataCandidate{Title: "Dune: Deluxe Edition", Series: "Dune Chronicles", SeriesPosition: "1"},
			overwrites: title,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := checkSeriesNumberLost(&c.book, &c.cand, c.overwrites)
			if r.Outcome == OutcomeAgree {
				t.Fatalf("series_number must never agree: %+v", r)
			}
			if r.Reason != c.want {
				t.Fatalf("reason %q (%s), want %q", r.Reason, r.Detail, c.want)
			}
			if r.Reason != ReasonSeriesNumberLost && strings.Contains(r.Detail, "drops it") {
				t.Fatalf("detail says the number was dropped, but it was not lost: %s", r.Detail)
			}
		})
	}
}

// TestCheckEvidence_FillOnlyTitleIsNotChecked drives a real fill-only pair
// through CheckEvidence: every stored title word survives, so overwrites()
// does not list "title" and series_number stays neutral, although the same
// pair blocks when the title IS overwritten.
func TestCheckEvidence_FillOnlyTitleIsNotChecked(t *testing.T) {
	book := database.Book{Title: "Pixel Dust Book 1", Duration: intp(30000),
		Author: &database.Author{Name: "Ann Author"}, FilePath: "/lib/Ann Author/Pixel Dust/Pixel Dust Book 1.m4b"}
	cand := metafetch.MetadataCandidate{Title: "Pixel Dust 1 Party Hard", Author: "Ann Author", DurationSec: 30000}
	v := CheckEvidence(&book, &cand, false)
	if contains(v.Overwrites, "title") {
		t.Fatalf("overwrites = %v; the pair was meant to be fill-only", v.Overwrites)
	}
	for _, ch := range v.Checks {
		if ch.Name == "series_number" && ch.Outcome != OutcomeNeutral {
			t.Fatalf("series_number ran on a fill-only apply: %+v", ch)
		}
	}
	if r := checkSeriesNumberLost(&book, &cand, []string{"title"}); r.Reason != ReasonSeriesNumberLost {
		t.Fatalf("control: with a title overwrite the pair must block, got %+v", r)
	}
}

// TestNumberContext pins the words the "kept as the series position" rule
// compares with the series name.
func TestNumberContext(t *testing.T) {
	n := func(title string) map[string]bool {
		num, ok := seqnum.ParseTitle(title)
		if !ok {
			t.Fatalf("no number in %q", title)
		}
		return numberContext(title, num)
	}
	if ctx := n("Dungeon Crawler Carl Book 4 - The Gate of the Feral Gods"); !seriesCovers(ctx, "Dungeon Crawler Carl") {
		t.Fatalf("DCC context %v should be covered by its series", ctx)
	}
	if ctx := n("Empire of Man 04 - We Few"); seriesCovers(ctx, "Prince Roger") {
		t.Fatalf("Empire of Man context %v must not be covered by Prince Roger", ctx)
	}
}
