// file: internal/applygate/evidence_test.go
// version: 1.2.0
// guid: 1b7e3d52-9c4a-4f18-a26d-5e0f8b3c7a91
// last-edited: 2026-09-13

package applygate

import (
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// TestCheckEvidence_OwnerExamples pins the rows the owner found in the
// 2026-09-13 prod dry run, each of which the three-leg gate let through.
func TestCheckEvidence_OwnerExamples(t *testing.T) {
	cases := []struct {
		name string
		book database.Book
		cand metafetch.MetadataCandidate
		want string // "" = pass
	}{
		{
			name: "Eldest matched to a 15-minute C.L.O.W.N. 242",
			book: database.Book{Title: "Eldest", Duration: intp(33 * 3600),
				FilePath: "/lib/Christopher Paolini/Inheritance Cycle/Eldest/Eldest.m4b",
				Author:   &database.Author{Name: "Christopher Paolini"}},
			cand: metafetch.MetadataCandidate{Title: "C.L.O.W.N. 242", Author: "Someone Else", DurationSec: 15 * 60},
			want: ReasonRuntimeMismatch,
		},
		{
			name: "an editor credited as the author",
			book: database.Book{Title: "Wastelands", Duration: intp(36000),
				FilePath: "/lib/John Joseph Adams/Wastelands/Wastelands.m4b"},
			cand: metafetch.MetadataCandidate{Title: "Wastelands", Author: "John Joseph Adams - editor", DurationSec: 36000},
			want: ReasonAuthorRoleCredit,
		},
		{
			name: "translator credit in a list",
			book: database.Book{Title: "Clear Threat", Duration: intp(36000),
				FilePath: "/lib/Dan Sugralinov/Clear Threat/Clear Threat.m4a"},
			cand: metafetch.MetadataCandidate{Title: "Clear Threat", Author: "Dan Sugralinov, Alix Merlin Williamson - translator", DurationSec: 36000},
			want: ReasonAuthorRoleCredit,
		},
		{
			name: "series name as the candidate title replaces a real title with no runtime to confirm",
			book: database.Book{Title: "A New Dawn: Star Wars", Duration: intp(6*3600 + 19*60),
				FilePath: "/lib/John Jackson Miller/Star Wars/A New Dawn_ Star Wars/A New Dawn_ Star Wars - John Jackson Miller.m4b"},
			cand: metafetch.MetadataCandidate{Title: "Star Wars", Author: "John Jackson Miller", Series: "Star Wars"},
			want: ReasonRuntimeUnknownOverwrite,
		},
		{
			name: "new author whose name is nowhere in the path",
			book: database.Book{Title: "Operation Vengeance", Duration: intp(36000),
				FilePath: "/lib/Unknown Author/Operation Vengeance/Operation Vengeance.mp3"},
			cand: metafetch.MetadataCandidate{Title: "Operation Vengeance", Author: "Mark O'Neill", DurationSec: 36000},
			want: ReasonAuthorNotInPath,
		},
		{
			name: "different book in the same series",
			book: database.Book{Title: "Wayfarers", Duration: intp(36000),
				FilePath: "/lib/Becky Chambers/Wayfarers/Wayfarers"},
			cand: metafetch.MetadataCandidate{Title: "The Long Way to a Small, Angry Planet", Author: "Becky Chambers", DurationSec: 36000},
			want: ReasonTitleDisagrees,
		},
		{
			name: "different narrator and a different runtime",
			book: database.Book{Title: "Dune", Duration: intp(21 * 3600), Narrator: strp("Scott Brick"),
				FilePath: "/lib/Frank Herbert/Dune/Dune.m4b"},
			cand: metafetch.MetadataCandidate{Title: "Dune", Author: "Frank Herbert", Narrator: "Simon Vance", DurationSec: 21*3600 + 90*60},
			want: ReasonNarratorMismatch,
		},
		{
			name: "ASIN conflict",
			book: database.Book{Title: "Dune", Duration: intp(36000), ASIN: strp("B002V1OF70"),
				FilePath: "/lib/Frank Herbert/Dune/Dune.m4b"},
			cand: metafetch.MetadataCandidate{Title: "Dune", Author: "Frank Herbert", ASIN: "B0XXXXXXXX", DurationSec: 36000},
			want: ReasonASINConflict,
		},
		{
			name: "title only: one agreement is not enough",
			book: database.Book{Title: "Mage Academy 2", FilePath: "/lib/x/Mage Academy 2"},
			cand: metafetch.MetadataCandidate{Title: "Mage Academy 2"},
			want: ReasonInsufficientEvidence,
		},
		{
			name: "fill-only with title and author agreeing passes without a runtime",
			book: database.Book{Title: "Cthulhu Armageddon", FilePath: "/lib/C. T. Phipps/Cthulhu Armageddon/Cthulhu Armageddon.m4b"},
			cand: metafetch.MetadataCandidate{Title: "Cthulhu Armageddon", Author: "C. T. Phipps"},
		},
		{
			name: "accented surname matches an unaccented path",
			book: database.Book{Title: "The Wayward Bard", Duration: intp(36000),
				FilePath: "/lib/Lars Machmuller/World of Chains/The Wayward Bard.m4b"},
			cand: metafetch.MetadataCandidate{Title: "The Wayward Bard", Author: "Lars Machmüller", DurationSec: 36100},
		},
		{
			name: "subtitle carries the real title",
			book: database.Book{Title: "A New Dawn: Star Wars", Duration: intp(22740),
				FilePath: "/lib/John Jackson Miller/Star Wars/A New Dawn.m4b"},
			cand: metafetch.MetadataCandidate{Title: "Star Wars", Subtitle: "A New Dawn", Author: "John Jackson Miller", Series: "Star Wars", DurationSec: 22700},
		},
		// Review findings on #3380.
		{
			name: "series name as title passes through a series-named folder",
			book: database.Book{Title: "A New Dawn: Star Wars", Duration: intp(22740),
				FilePath: "/lib/John Jackson Miller/Star Wars/A New Dawn.m4b"},
			cand: metafetch.MetadataCandidate{Title: "Star Wars", Author: "John Jackson Miller", Series: "Star Wars", DurationSec: 22700},
			want: ReasonTitleDisagrees,
		},
		{
			name: "a book whose title really is the series name still passes",
			book: database.Book{Title: "Dune", Duration: intp(36000),
				FilePath: "/lib/Frank Herbert/Dune/Dune.m4b", Author: &database.Author{Name: "Frank Herbert"}},
			cand: metafetch.MetadataCandidate{Title: "Dune", Author: "Frank Herbert", Series: "Dune", DurationSec: 36000},
		},
		{
			name: "Last, First candidate does not agree with a shared first name",
			book: database.Book{Title: "Carrie", Duration: intp(36000),
				FilePath: "/lib/Unknown/Carrie/Carrie.m4b", Author: &database.Author{Name: "Stephen King"}},
			cand: metafetch.MetadataCandidate{Title: "Carrie", Author: "Baxter, Stephen", DurationSec: 36000},
			want: ReasonAuthorNotInPath,
		},
		{
			name: "Last, First candidate agrees with the same person",
			book: database.Book{Title: "Carrie", Duration: intp(36000),
				FilePath: "/lib/Stephen King/Carrie/Carrie.m4b", Author: &database.Author{Name: "Stephen King"}},
			cand: metafetch.MetadataCandidate{Title: "Carrie", Author: "King, Stephen", DurationSec: 36000},
		},
		{
			name: "narrators sharing only a first name do not agree",
			book: database.Book{Title: "Dune", Duration: intp(21 * 3600), Narrator: strp("Scott Brick"),
				FilePath: "/lib/Frank Herbert/Dune/Dune.m4b"},
			cand: metafetch.MetadataCandidate{Title: "Dune", Author: "Frank Herbert", Narrator: "Scott Sowers", DurationSec: 21*3600 + 90*60},
			want: ReasonNarratorMismatch,
		},
		{
			name: "narrator mismatch with no runtime does not block a fill",
			book: database.Book{Title: "It", Narrator: strp("Stephen King"),
				FilePath: "/lib/Stephen King/It/It.m4b"},
			cand: metafetch.MetadataCandidate{Title: "It", Author: "Stephen King", Narrator: "Steven Weber"},
		},
		{
			name: "respelled initials are not an overwrite, so no runtime is needed",
			book: database.Book{Title: "The Hobbit", Author: &database.Author{Name: "J.R.R. Tolkien"},
				FilePath: "/lib/J.R.R. Tolkien/The Hobbit/The Hobbit.m4b"},
			cand: metafetch.MetadataCandidate{Title: "The Hobbit: Or There and Back Again", Author: "J. R. R. Tolkien"},
		},
		{
			name: "(ed) in parentheses is a role credit",
			book: database.Book{Title: "Wastelands", Duration: intp(36000),
				FilePath: "/lib/John Joseph Adams/Wastelands/Wastelands.m4b"},
			cand: metafetch.MetadataCandidate{Title: "Wastelands", Author: "John Joseph Adams (ed)", DurationSec: 36000},
			want: ReasonAuthorRoleCredit,
		},
		{
			name: "Ed as a first name is not a role credit",
			book: database.Book{Title: "Spellfire", Duration: intp(36000),
				FilePath: "/lib/Ed Greenwood/Spellfire/Spellfire.m4b"},
			cand: metafetch.MetadataCandidate{Title: "Spellfire", Author: "Ed Greenwood", DurationSec: 36000},
		},
		{
			name: "a dotted folder name is the title, not an extension",
			book: database.Book{Title: "Mage Academy 1.5", Duration: intp(36000),
				FilePath: "/lib/Jane Roe/Mage Academy 1.5"},
			cand: metafetch.MetadataCandidate{Title: "Mage Academy 1.5", Author: "Jane Roe", DurationSec: 36000},
		},
		{
			name: "slash-joined role credit is a role credit",
			book: database.Book{Title: "Best Lesbian Romance 2009", Duration: intp(24900),
				FilePath: "/lib/Radclyffe/Best Lesbian Romance 2009/Best Lesbian Romance 2009 - read by narrator.m4b"},
			cand: metafetch.MetadataCandidate{Title: "Best Lesbian Romance 2009", Author: "Radclyffe - author/editor", DurationSec: 24720},
			want: ReasonAuthorRoleCredit,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := CheckEvidence(&c.book, &c.cand, false)
			if c.want == "" {
				if !v.Pass {
					t.Fatalf("refused: %s (%s); checks %+v", v.Reason, v.Detail, v.Checks)
				}
				return
			}
			if v.Pass || v.Reason != c.want {
				t.Fatalf("pass=%v reason=%q (%s), want blocked %q; checks %+v", v.Pass, v.Reason, v.Detail, c.want, v.Checks)
			}
		})
	}
}

// TestTitleSim_SubsetIsNotAMatch pins why titleSim is not a token-set ratio:
// a series name is a subset of the full title and must not score 1.0.
func TestTitleSim_SubsetIsNotAMatch(t *testing.T) {
	if s := titleSim("Star Wars", "A New Dawn: Star Wars"); s > 0.5 {
		t.Fatalf("titleSim = %v, want <= 0.5", s)
	}
	if s := titleSim("The Awakened Spark", "Awakened Spark"); s != 1 {
		t.Fatalf("titleSim = %v, want 1 (stop words ignored)", s)
	}
}

// TestCheckRuntime_Bands pins the band edges.
func TestCheckRuntime_Bands(t *testing.T) {
	book := &database.Book{Duration: intp(10000)}
	for _, c := range []struct {
		cand int
		want string
	}{{10500, OutcomeAgree}, {10501, OutcomeNeutral}, {11000, OutcomeNeutral}, {11001, OutcomeBlock}, {900, OutcomeBlock}, {0, OutcomeUnknown}} {
		if got := checkRuntime(book, &metafetch.MetadataCandidate{DurationSec: c.cand}, false).Outcome; got != c.want {
			t.Errorf("candidate %ds vs 10000s: %s, want %s", c.cand, got, c.want)
		}
	}
}

// TestTitleSim_NumberOnlyOverlap pins that a shared number alone is no match.
func TestTitleSim_NumberOnlyOverlap(t *testing.T) {
	if s := titleSim("Book 1", "Part 1"); s != 0 {
		t.Fatalf("titleSim(Book 1, Part 1) = %v, want 0", s)
	}
	if s := titleSim("1984", "1984"); s != 1 {
		t.Fatalf("titleSim(1984, 1984) = %v, want 1", s)
	}
}

// TestOverwrites_ExtendIsNotReplace pins that only losing a token is an overwrite.
func TestOverwrites_ExtendIsNotReplace(t *testing.T) {
	book := &database.Book{Title: "A New Dawn: Star Wars", Author: &database.Author{Name: "John Jackson Miller"}}
	if got := overwrites(book, &metafetch.MetadataCandidate{Title: "Star Wars", Author: "Miller, John Jackson"}); len(got) != 1 || got[0] != "title" {
		t.Fatalf("overwrites = %v, want [title]", got)
	}
	if got := overwrites(book, &metafetch.MetadataCandidate{Title: "Star Wars", Subtitle: "A New Dawn", Author: "John Jackson Miller"}); len(got) != 0 {
		t.Fatalf("overwrites = %v, want none", got)
	}
}

// TestSurnames pins credit parsing.
func TestSurnames(t *testing.T) {
	for in, want := range map[string]string{
		"Baxter, Stephen":                        "baxter",
		"Dan Sugralinov, Alix Merlin Williamson": "sugralinov williamson",
		"Smith And Jones":                        "smith jones",
		"Martin Luther King Jr.":                 "king",
		"Radclyffe - author/editor":              "radclyffe",
		"Jane Doe (editor)":                      "doe",
	} {
		if got := strings.Join(surnames(in), " "); got != want {
			t.Errorf("surnames(%q) = %q, want %q", in, got, want)
		}
	}
}
