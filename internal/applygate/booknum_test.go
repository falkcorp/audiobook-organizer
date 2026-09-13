// file: internal/applygate/booknum_test.go
// version: 1.0.0
// guid: 5e8a0b17-4c3d-4f92-a6e1-2d9c7b0f8e35
// last-edited: 2026-09-13

package applygate

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/seqnum"
)

// TestCheckSeriesNumberLost uses titles from the 2026-09-13 prod preview that
// passed with their volume number dropped, plus near misses: numbers that are
// part of the title, and a volume number kept in the series field rather than
// the title.
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
			name: "mid-title number: Empire of Man 04 - We Few",
			book: database.Book{Title: "Empire of Man 04 - We Few"},
			cand: metafetch.MetadataCandidate{Title: "We Few", Series: "Prince Roger", SeriesPosition: "4"},
			overwrites: title, want: ReasonSeriesNumberLost,
		},
		{
			name: "number standing alone between segments",
			book: database.Book{Title: "The Legends of the First Empire - 3 - Age of War"},
			cand: metafetch.MetadataCandidate{Title: "Age of War"},
			overwrites: title, want: ReasonSeriesNumberLost,
		},
		{
			name: "Book N marker dropped, series keeps the number (exception off)",
			book: database.Book{Title: "Into the Void: Sentenced to War, Book 14"},
			cand: metafetch.MetadataCandidate{Title: "Into the Void", Series: "Sentenced to War", SeriesPosition: "14"},
			overwrites: title, want: ReasonSeriesNumberLost,
		},
		{
			name: "leading novella number 00.5",
			book: database.Book{Title: "00.5 Tin Man"},
			cand: metafetch.MetadataCandidate{Title: "Tin Man", Series: "Galaxy's Edge", SeriesPosition: "0.5"},
			overwrites: title, want: ReasonSeriesNumberLost,
		},
		{
			name: "1984 is a year, not a volume",
			book: database.Book{Title: "1984"},
			cand: metafetch.MetadataCandidate{Title: "Nineteen Eighty-Four"},
			overwrites: title,
		},
		{
			name: "Catch-22 kept by the candidate",
			book: database.Book{Title: "Catch-22"},
			cand: metafetch.MetadataCandidate{Title: "Catch-22", Subtitle: "50th Anniversary Edition"},
			overwrites: title,
		},
		{
			name: "Fahrenheit 451 kept by the candidate",
			book: database.Book{Title: "Fahrenheit 451 (Unabridged)"},
			cand: metafetch.MetadataCandidate{Title: "Fahrenheit 451"},
			overwrites: title,
		},
		{
			name: "sequence kept in the series field, not the title",
			book: database.Book{Title: "Dune", Series: &database.Series{Name: "Dune Chronicles"}, SeriesPositionRaw: strp("1")},
			cand: metafetch.MetadataCandidate{Title: "Dune: Deluxe Edition", Series: "Dune Chronicles", SeriesPosition: "1"},
			overwrites: title,
		},
		{
			name: "fill-only apply is not checked",
			book: database.Book{Title: "Party Hard: Pixel Dust, Book 1"},
			cand: metafetch.MetadataCandidate{Title: "Party Hard"},
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
		})
	}
}

// TestNumberContext pins the words the exception would compare with the
// series name, so KeepNumberViaSeries behaves as documented when flipped.
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
