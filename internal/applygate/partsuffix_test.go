// file: internal/applygate/partsuffix_test.go
// version: 1.0.0
// guid: 4a7e2c91-3b6d-4f08-9e15-c2d8a0b7f364
// last-edited: 2026-09-14

package applygate

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/seqnum"
)

// TestCheckSequence_PartSuffix uses titles refused on the 2026-09-14 prod
// preview with "sequence_missing_on_candidate: book is #1 (series_position=1,
// title=1)". The " - 001" is the part number of a multi-part rip, and the
// series position 1 was derived from that same suffix at import
// (matcher.IdentifySeries), so neither is a book number. Rows marked
// refuse=true are the controls: a real series, a real number, or a source
// independent of the suffix must still refuse.
func TestCheckSequence_PartSuffix(t *testing.T) {
	one := intp(1)
	cases := []struct {
		name   string
		book   database.Book
		cand   metafetch.MetadataCandidate
		refuse bool
	}{
		{
			name: "Rogue Lawyer - 001, position derived from the suffix",
			book: database.Book{Title: "Rogue Lawyer - 001", SeriesSequence: one,
				FilePath: "/lib/John Grisham/Rogue Lawyer/Rogue Lawyer - 001.mp3"},
			cand: metafetch.MetadataCandidate{Title: "Rogue Lawyer"},
		},
		{
			name: "All Tomorrow's Parties - 001, series row is the title base",
			book: database.Book{Title: "All Tomorrow's Parties - 001", SeriesSequence: one,
				Series:   &database.Series{Name: "All Tomorrow's Parties"},
				FilePath: "/lib/William Gibson/All Tomorrow's Parties - 001.mp3"},
			cand: metafetch.MetadataCandidate{Title: "All Tomorrow's Parties"},
		},
		{
			name: "The Rooster Bar - 001, raw position",
			book: database.Book{Title: "The Rooster Bar - 001", SeriesPositionRaw: strp("1")},
			cand: metafetch.MetadataCandidate{Title: "The Rooster Bar"},
		},
		{
			name: "Witness to a Trial - 001, directory book",
			book: database.Book{Title: "Witness to a Trial - 001", SeriesSequence: one,
				FilePath: "/lib/John Grisham/Witness to a Trial - 001"},
			cand: metafetch.MetadataCandidate{Title: "Witness to a Trial"},
		},
		// Controls: these keep refusing.
		{
			name: "part suffix under a real series name",
			book: database.Book{Title: "Rogue Lawyer - 001", SeriesSequence: one,
				Series: &database.Series{Name: "Theodore Boone"}},
			cand:   metafetch.MetadataCandidate{Title: "Rogue Lawyer"},
			refuse: true,
		},
		{
			name: "position differs from the part number",
			book: database.Book{Title: "Rogue Lawyer - 001", SeriesSequence: intp(3)},
			cand: metafetch.MetadataCandidate{Title: "Rogue Lawyer"}, refuse: true,
		},
		{
			name: "independent folder source carries the number",
			book: database.Book{Title: "Rogue Lawyer - 001", SeriesSequence: one,
				FilePath: "/lib/Grisham/Book 1/Rogue Lawyer - 001.mp3"},
			cand: metafetch.MetadataCandidate{Title: "Rogue Lawyer"}, refuse: true,
		},
		{
			name: "Mistborn 01",
			book: database.Book{Title: "Mistborn 01", SeriesSequence: one},
			cand: metafetch.MetadataCandidate{Title: "The Final Empire"}, refuse: true,
		},
		{
			name: "Book 04",
			book: database.Book{Title: "Book 04"},
			cand: metafetch.MetadataCandidate{Title: "Something Else"}, refuse: true,
		},
		{
			name: "Wheel of Time 03",
			book: database.Book{Title: "Wheel of Time 03"},
			cand: metafetch.MetadataCandidate{Title: "The Dragon Reborn"}, refuse: true,
		},
		{
			name: "The Sorcerer's Ring - 04 - A Cry of Honor",
			book: database.Book{Title: "The Sorcerer's Ring - 04 - A Cry of Honor", SeriesPositionRaw: strp("4"),
				Series: &database.Series{Name: "The Sorcerer's Ring"}},
			cand: metafetch.MetadataCandidate{Title: "A Cry of Honor"}, refuse: true,
		},
		{
			name: "two-digit dash suffix still reads as a volume",
			book: database.Book{Title: "Big Cats - 03"},
			cand: metafetch.MetadataCandidate{Title: "Big Cats"}, refuse: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := CheckSequence(&c.book, &c.cand)
			if c.refuse && v.Pass {
				t.Fatalf("passed, want a refusal; book numbers %v", v.BookNumbers)
			}
			if !c.refuse && !v.Pass {
				t.Fatalf("refused %s: %s", v.Reason, v.Detail)
			}
		})
	}
}

// TestCheckSeriesNumberLost_PartSuffix: the same suffix must not make a title
// overwrite "lose" a volume number the book never had.
func TestCheckSeriesNumberLost_PartSuffix(t *testing.T) {
	book := database.Book{Title: "Rogue Lawyer - 001", SeriesSequence: intp(1)}
	cand := metafetch.MetadataCandidate{Title: "Rogue Lawyer"}
	if r := checkSeriesNumberLost(&book, &cand, []string{"title"}); r.Outcome == OutcomeBlock {
		t.Fatalf("blocked %s: %s", r.Reason, r.Detail)
	}
	// Control: under a real series the suffix counts again, so a result that
	// keeps #1 nowhere (other series, other position) must block.
	real := database.Book{Title: "Rogue Lawyer - 001", SeriesSequence: intp(1), Series: &database.Series{Name: "Theodore Boone"}}
	moved := metafetch.MetadataCandidate{Title: "Rogue Lawyer", Series: "Zoo", SeriesPosition: "7"}
	if r := checkSeriesNumberLost(&real, &moved, []string{"title"}); r.Outcome != OutcomeBlock {
		t.Fatalf("control: under a real series the dropped #1 must block, got %+v", r)
	}
}

// TestPartSuffixNegatives_StillCarryANumber pins that the real series titles
// from the task keep their number in the parser each leg uses.
func TestPartSuffixNegatives_StillCarryANumber(t *testing.T) {
	for title, want := range map[string]string{
		"Mistborn 01 - The Final Empire":            "1",
		"The Sorcerer's Ring - 04 - A Cry of Honor": "4",
		"Book 04":          "4",
		"Wheel of Time 03": "3",
	} {
		n, ok := seqnum.ParseTitle(title)
		if !ok || n.Text != want {
			t.Errorf("ParseTitle(%q) = %q (ok=%v), want %q", title, n.Text, ok, want)
		}
	}
}
