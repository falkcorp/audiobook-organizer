// file: internal/applygate/title_gate_matrix_test.go
// version: 2.2.0
// guid: 9a1e4c73-5d2b-4f08-b6a1-3c7e0d58f924
// last-edited: 2026-11-17

package applygate

import (
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// gateRun runs the whole gate (Evaluate) with one runtime situation; the
// author agrees and the score passes in all of them, so the title check and
// the runtime are the only things that differ.
//   - rt5:  files 36000 s, candidate 36600 s (1.7% off, the runtime agrees)
//   - rt7:  files 36000 s, candidate 38520 s (7% off, the runtime is neutral)
//   - none: the files have no runtime at all (unknown)
func gateRun(h string, book database.Book, pos string, cand metafetch.MetadataCandidate) Verdict {
	book.Author = &database.Author{Name: "Bruce Wayne"}
	if pos != "" {
		book.SeriesPositionRaw = new(pos)
	}
	cand.Author = "Bruce Wayne"
	cand.Score = 0.95
	switch h {
	case "rt5":
		book.Duration = new(36000)
		cand.DurationSec = 36600
	case "rt7":
		book.Duration = new(36000)
		cand.DurationSec = 38520
	default:
		book.Duration = nil
		cand.DurationSec = 36600
	}
	return Evaluate(&book, snap(&book), database.ComputeBookRuntime(&book, nil), &cand, nil)
}

// TestEvaluate_TitleGateMatrix is the reproduction table of review r1 of #4688
// as assertions: every row says what the title check answers and where the pair
// may apply unattended.
//   - "never": refused in all three runtime situations (a conflict, or another
//     leg refuses it).
//   - "rt5": a NEUTRAL title, applied only when the runtime agrees (rt5) and
//     refused with a runtime 7% off (rt7) or none.
//   - "any": the title AGREES, so today's requirements hold and nothing here
//     depends on the runtime.
//
// The Batman rows are the fixtures: the pairs are published as different books
// by the owner's catalogue, not data from a real library.
//
// M1a ("Year One" against "Grayson: Year One") is neutral and applies at rt5:
// one text continues past the other's, and nothing in the fields says "Grayson"
// is a franchise word and not part of the title. A rule that told them apart
// would have to excuse text by what another field says, the move that made
// round 1 leaky. It is recorded here as the open class it is, and the policy
// (a neutral title applies with an agreeing runtime) is under owner review: see
// title_owner_review_pins_test.go.
//
// Two rows CHANGED when the combined round adopted the part-marker and
// number-by-kind rules (see the changelog fragment, "Reversals"):
//   - M3d, "Hush" against "Hush: Part 2": was neutral and applied at rt5. A
//     candidate that is part 2 of a book whose files say nothing of parts is
//     refused as partial_book whatever the runtime says, since two parts of one
//     book are often within 5% of each other's length.
//   - O5, "Knightfall 2: Book 3" against "Knightfall 3: Book 2": was neutral.
//     The two sides state different volumes in their fields, so it is a title
//     conflict; one side holding the other's numbers settles nothing.
func TestEvaluate_TitleGateMatrix(t *testing.T) {
	bat, tlh := "Batman", "The Long Halloween"
	legends := "Legends of the Dark Knight"
	narr := "Dick Grayson"
	rows := []struct {
		id      string
		title   string
		pattern string
		book    database.Book
		pos     string
		cand    metafetch.MetadataCandidate
	}{
		{"R1 repro Hush 1<-2", "block", "never", database.Book{Title: "Hush: Part 1", FilePath: "/lib/Bruce Wayne/Hush: Part 1.m4b"}, "2", metafetch.MetadataCandidate{Title: "Hush: Part 2", Series: legends, SeriesPosition: "2"}},
		{"R2 repro Hush 2<-1", "block", "never", database.Book{Title: "Hush: Part 2", FilePath: "/lib/Bruce Wayne/Hush: Part 2.m4b"}, "1", metafetch.MetadataCandidate{Title: "Hush: Part 1", Series: legends, SeriesPosition: "1"}},
		{"R3 repro Hush I<-II", "block", "never", database.Book{Title: "Hush: Part I", FilePath: "/lib/Bruce Wayne/Hush Part I.m4b"}, "2", metafetch.MetadataCandidate{Title: "Hush: Part II", Series: legends, SeriesPosition: "2"}},
		{"R4 repro Hush II<-I", "block", "never", database.Book{Title: "Hush: Part II", FilePath: "/lib/Bruce Wayne/Hush Part II.m4b"}, "1", metafetch.MetadataCandidate{Title: "Hush: Part I", Series: legends, SeriesPosition: "1"}},
		{"R5 repro DV<-TLH", "block", "never", database.Book{Title: "Batman: Dark Victory"}, "1", metafetch.MetadataCandidate{Title: "Batman: The Long Halloween", Subtitle: "The Prelude to Dark Victory", Series: legends, SeriesPosition: "1"}},
		{"R6 repro TLH<-DV", "block", "never", database.Book{Title: "Batman: The Long Halloween"}, "1", metafetch.MetadataCandidate{Title: "Batman: Dark Victory", Series: legends, SeriesPosition: "1"}},

		{"B1a series=other storyline, DV<-TLH", "block", "never", database.Book{Title: "Batman: Dark Victory"}, "", metafetch.MetadataCandidate{Title: "Batman: The Long Halloween", Series: tlh, SeriesPosition: "1"}},
		{"B1b series=other storyline, TLH<-DV", "block", "never", database.Book{Title: "Batman: The Long Halloween"}, "", metafetch.MetadataCandidate{Title: "Batman: Dark Victory", Series: tlh, SeriesPosition: "2"}},
		{"B1c series=other storyline, DV<-'TLH: Batman'", "block", "never", database.Book{Title: "Batman: Dark Victory"}, "", metafetch.MetadataCandidate{Title: "The Long Halloween: Batman", Series: tlh}},

		{"M1a credit words, 'Year One'<-'Grayson: Year One'", "neutral", "rt5", database.Book{Title: "Year One"}, "", metafetch.MetadataCandidate{Title: "Grayson: Year One", Narrator: narr}},
		{"M1c credit words, 'Batman: Year One'<-'Grayson: Year One'", "block", "never", database.Book{Title: "Batman: Year One"}, "", metafetch.MetadataCandidate{Title: "Grayson: Year One", Narrator: narr}},

		{"M2a of-number words", "block", "never", database.Book{Title: "Night of Seven Owls"}, "", metafetch.MetadataCandidate{Title: "Night of Five Owls"}},
		{"M2b of-number digits", "block", "never", database.Book{Title: "Night of 7 Owls"}, "", metafetch.MetadataCandidate{Title: "Night of 5 Owls"}},

		{"M3a one-sided number: Subtitle 'Book 2'", "neutral", "rt5", database.Book{Title: "Knightfall"}, "", metafetch.MetadataCandidate{Title: "Knightfall", Subtitle: "Book 2", SeriesPosition: "2"}},
		{"M3b one-sided number: '(Book 2)'", "neutral", "rt5", database.Book{Title: "Knightfall"}, "", metafetch.MetadataCandidate{Title: "Knightfall (Book 2)"}},
		{"M3c one-sided number: ' - 2'", "neutral", "rt5", database.Book{Title: "Knightfall"}, "", metafetch.MetadataCandidate{Title: "Knightfall - 2"}},
		{"M3d lone part marker 'Hush'<-'Hush: Part 2' (REVERSED: partial_book)", "neutral", "never", database.Book{Title: "Hush"}, "", metafetch.MetadataCandidate{Title: "Hush: Part 2"}},
		{"M3e control 'Knightfall'<-'Knightfall: Book 2'", "neutral", "rt5", database.Book{Title: "Knightfall"}, "", metafetch.MetadataCandidate{Title: "Knightfall: Book 2"}},

		{"D2a franchise-only candidate", "neutral", "rt5", database.Book{Title: "Batman: Dark Victory"}, "", metafetch.MetadataCandidate{Title: "Batman"}},
		{"D2b franchise-only stored", "neutral", "rt5", database.Book{Title: "Batman"}, "", metafetch.MetadataCandidate{Title: "Batman: Dark Victory"}},
		{"D2c franchise-only candidate, series declared", "block", "never", database.Book{Title: "Batman: Dark Victory"}, "", metafetch.MetadataCandidate{Title: "Batman", Series: bat}},
		{"D2d franchise-only stored, series declared", "block", "never", database.Book{Title: "Batman"}, "", metafetch.MetadataCandidate{Title: "Batman: Dark Victory", Series: bat}},
		{"D2e stored 'DV', candidate Title 'Batman' Subtitle 'Dark Victory'", "neutral", "rt5", database.Book{Title: "Batman"}, "", metafetch.MetadataCandidate{Title: "Batman", Subtitle: "Dark Victory"}},

		{"O1 sequel by suffix", "neutral", "rt5", database.Book{Title: "Dark Victory"}, "", metafetch.MetadataCandidate{Title: "Dark Victory Returns"}},
		{"O2 sequel by number", "neutral", "rt5", database.Book{Title: "Hush"}, "", metafetch.MetadataCandidate{Title: "Hush 2"}},
		{"O3 ordinal words", "neutral", "rt5", database.Book{Title: "The Second Robin"}, "", metafetch.MetadataCandidate{Title: "The Third Robin"}},
		{"O4 seven words, one differs", "neutral", "rt5", database.Book{Title: "Gotham City Dark Knight Detective Tales Batman"}, "", metafetch.MetadataCandidate{Title: "Gotham City Dark Knight Noir Tales Batman"}},
		{"O5 numbers swapped between segments (REVERSED: block)", "block", "never", database.Book{Title: "Knightfall 2: Book 3"}, "", metafetch.MetadataCandidate{Title: "Knightfall 3: Book 2"}},
		{"O6 unicode roman numerals", "block", "never", database.Book{Title: "Hush Ⅱ"}, "", metafetch.MetadataCandidate{Title: "Hush Ⅲ"}},

		{"N1a year folder vs volume", "neutral", "rt5", database.Book{Title: "Knightfall", FilePath: "/lib/Bruce Wayne/Knightfall (1993)/Knightfall (1993).m4b"}, "", metafetch.MetadataCandidate{Title: "Knightfall (Batman, Book 1)", Series: bat, SeriesPosition: "1"}},
		{"N1b year folder vs volume, 'Knightfall: Book 1'", "neutral", "rt5", database.Book{Title: "Knightfall", FilePath: "/lib/Bruce Wayne/Knightfall (1993)/Knightfall (1993).m4b"}, "", metafetch.MetadataCandidate{Title: "Knightfall: Book 1", Series: bat, SeriesPosition: "1"}},
		{"N2 stored 'Book 2', candidate position 2", "neutral", "never", database.Book{Title: "Knightfall: Book 2"}, "", metafetch.MetadataCandidate{Title: "Knightfall", SeriesPosition: "2"}},
		{"N3a disc file vs part title", "agree", "any", database.Book{Title: "Hush: Part 1", FilePath: "/lib/Bruce Wayne/Hush - Disc 2.m4b"}, "", metafetch.MetadataCandidate{Title: "Hush: Part 1"}},
		{"N3b ordinal word vs digit (CHANGED: agree, the ordinal beside a marker is its number)", "agree", "any", database.Book{Title: "Hush: Book 3"}, "", metafetch.MetadataCandidate{Title: "Hush: Third Book"}},
		{"N3c compound number word", "agree", "any", database.Book{Title: "Gotham Year Twenty-One"}, "", metafetch.MetadataCandidate{Title: "Gotham Year 21"}},
		{"N3d pronoun I", "neutral", "rt5", database.Book{Title: "Who Am I"}, "", metafetch.MetadataCandidate{Title: "Who Am I 2"}},

		{"B1d stored series names the other storyline, DV<-TLH", "block", "never", database.Book{Title: "Batman: Dark Victory", Series: &database.Series{Name: "The Long Halloween"}}, "", metafetch.MetadataCandidate{Title: "Batman: The Long Halloween"}},
		{"B1e the same, mirrored", "block", "never", database.Book{Title: "Batman: The Long Halloween", Series: &database.Series{Name: "Dark Victory"}}, "", metafetch.MetadataCandidate{Title: "Batman: Dark Victory"}},
	}
	for _, r := range rows {
		t.Run(r.id, func(t *testing.T) {
			got := map[string]Verdict{}
			for _, h := range []string{"rt5", "rt7", "none"} {
				got[h] = gateRun(h, r.book, r.pos, r.cand)
			}
			if title := titleCheck(got["rt5"].Evidence).Outcome; string(title) != r.title && r.title != "" {
				// A leg earlier in the gate can refuse before the evidence
				// leg runs (the sequence leg); then there is no title check.
				if len(got["rt5"].Evidence.Checks) != 0 {
					t.Errorf("title check = %s, want %s (%s)", title, r.title, titleCheck(got["rt5"].Evidence).Detail)
				}
			}
			switch r.pattern {
			case "never":
				for h, v := range got {
					if v.Allowed {
						t.Errorf("%s: APPLIED unattended, want refused", h)
					}
				}
			case "rt5":
				if !got["rt5"].Allowed {
					t.Errorf("rt5: refused as %s (%s), want applied: the runtime agrees", got["rt5"].Reason, got["rt5"].Detail)
				}
				for _, h := range []string{"rt7", "none"} {
					if got[h].Allowed {
						t.Errorf("%s: a neutral title APPLIED without an agreeing runtime", h)
					}
				}
			case "any":
				if titleCheck(got["rt5"].Evidence).Outcome != OutcomeAgree {
					t.Errorf("title does not agree")
				}
				for h, v := range got {
					if v.Reason == ReasonTitleDisagrees || v.Reason == ReasonTitleUnconfirmed {
						t.Errorf("%s: refused by the title (%s)", h, v.Reason)
					}
				}
			}
		})
	}
}
