// file: internal/seriesdenumber/demote_test.go
// version: 3.0.0
// guid: c2a81e47-5f30-4d96-b7a3-19e60d8f4b25
// last-edited: 2026-11-17

package seriesdenumber

import "testing"

const testAuthor = "Alfred Pennyworth"

// cand builds a candidate whose planner base and position agree with the name,
// the common case; tests that need a disagreement build the struct themselves.
func cand(name string) Candidate {
	c := Candidate{FromName: name, AuthorID: 3, AuthorName: testAuthor}
	if p, _, ok := ParseName(name); ok {
		c.Base, c.Position = p.Base, p.N
	} else {
		c.Base, c.Position = name, 1
	}
	return c
}

func TestDemote_AppliesTheNamesTheOpExistsFor(t *testing.T) {
	for _, name := range []string{
		"Gotham Knights: Book 11", "Gotham Knights, Volume 3", "Court of Owls 05", "Discord Knights 05",
		"BAT 07", "Arkham Asylum 99", "Gotham Knights - 03", "Court of Owls #5",
	} {
		if v, held := Demote(cand(name)); held {
			t.Errorf("%q held as %s (%s), want it to pass", name, v.Code, v.Reason)
		}
	}
	// An author name that is unknown skips only the author check.
	c := cand("Alfred Pennyworth 12")
	c.AuthorName = ""
	if v, held := Demote(c); held {
		t.Errorf("held as %s with no author name to compare", v.Code)
	}
}

func TestDemote_HoldsWithNamedReason(t *testing.T) {
	cases := []struct {
		name string
		c    Candidate
		want Code
	}{
		{"no author", func() Candidate { c := cand("Gotham Knights: Book 11"); c.AuthorID = 0; return c }(), CodeNoAuthor},
		{"no author wins over everything", func() Candidate { c := cand("Hush Disc 2"); c.AuthorID = 0; return c }(), CodeNoAuthor},
		{"placeholder author", func() Candidate { c := cand("Gotham Knights: Book 11"); c.AuthorPlaceholder = true; return c }(), CodePlaceholderAuthor},
		{"part", cand("Hush Part: 2"), CodePartOrFormatWord},
		{"base with a second number", cand("Gotham Knights 01 of 03"), CodeBaseHasDigit},
		{"fraction", cand("Gotham Knights Book .5"), CodeFractionalNumber},
		{"too large", cand("Knightfall 100"), CodeNumberOutOfRange},
		{"the planner disagrees about the base", Candidate{FromName: "Court of Owls 05", Base: "Court", Position: 5, AuthorID: 3}, CodeNamesDisagree},
		{"the planner disagrees about the position", Candidate{FromName: "Court of Owls 05", Base: "Court of Owls", Position: 6, AuthorID: 3}, CodeNamesDisagree},
		{"base is the author", cand("Alfred Pennyworth 12"), CodeAuthorBase},
		{"base is the author, punctuation differs", cand("alfred-pennyworth 12"), CodeAuthorBase},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, held := Demote(tc.c)
			if !held {
				t.Fatalf("applied, want held as %s", tc.want)
			}
			if v.Code != tc.want {
				t.Fatalf("held as %s (%s), want %s", v.Code, v.Reason, tc.want)
			}
			if v.Reason == "" {
				t.Fatalf("held as %s with no reason", v.Code)
			}
		})
	}
}

// B5: a base that is ANY author's name, in any word order, as a part of a
// composite credit, or spelled with a decomposed accent.
func TestDemote_BaseEqualToAnyAuthorsName(t *testing.T) {
	authors := NewAuthorSet([]string{
		"Bruce Wayne", "  alfred   PENNYWORTH ", "", "Dick Grayson & Barbara Gordon", "Renée Montoya",
		"Gordon, James", "Kyle, Selina and Harvey Dent", "Tim Drake, Jason Todd",
	})
	for _, name := range []string{
		"Alfred Pennyworth 05",             // another author's name
		"Wayne, Bruce 03",                  // inverted Last, First
		"Barbara Gordon 03",                // part of a composite credit
		"Dick Grayson 03",                  // the other part
		"Renée Montoya 03",                // decomposed accent against a composed one
		"James Gordon 03",                  // stored inverted, read upright
		"Selina Kyle 03",                   // composite with an inverted part
		"Harvey Dent 03",                   // the other part of that credit
		"Jason Todd 03",                    // comma-listed credit
		"Gordon, James 03",                 // inverted in both
		"Bruce Wayne 03",                   // plain
		"BRUCE   WAYNE 03",                 // case and spacing
		"Dick Grayson & Barbara Gordon 03", // the composite itself
	} {
		c := cand(name)
		c.AuthorName = "Someone Else"
		c.Authors = authors
		if v, ok := Demote(c); !ok || v.Code != CodeAuthorBase {
			t.Errorf("%q: got %+v held=%v, want %s", name, v, ok, CodeAuthorBase)
		}
	}
	c := cand("Court of Owls 05")
	c.AuthorName = "Bruce Wayne"
	c.Authors = authors
	if v, ok := Demote(c); ok {
		t.Fatalf("a base that is nobody's name was held: %+v", v)
	}
	if (AuthorSet)(nil).Has("Bruce Wayne") {
		t.Fatalf("a nil set matched")
	}
}

// M1 (PR #4698 round 2): a base that is a PART of the series' own author's name
// is the author's name, not a series: the surname alone, the given name alone,
// the name with its initials taken out, in either word order, for every credit of
// a composite name. Built from the name's words, with no shape special-cased.
func TestDemote_BaseIsAPartOfTheSeriesOwnAuthorsName(t *testing.T) {
	cases := []struct{ author, base string }{
		{"Bruce Wayne", "Wayne"},
		{"Bruce Wayne", "Bruce"},
		{"Bruce T. Wayne", "Bruce Wayne"},
		{"Bruce T. Wayne", "Wayne"},
		{"Bruce T. Wayne", "Bruce"},
		{"Bruce Thomas Wayne", "Thomas Wayne"},
		{"Bruce Thomas Wayne", "Bruce Thomas"},
		{"Bruce Thomas Wayne", "Thomas"},
		{"Wayne, Bruce", "Wayne"},
		{"Wayne, Bruce", "Bruce"},
		{"Wayne, Bruce T.", "Bruce Wayne"},
		{"B. T. Wayne", "Wayne"},
		{"Bruce Wayne & Dick Grayson", "Grayson"},
		{"Bruce Wayne & Dick Grayson", "Dick"},
		{"Bruce Wayne and Dick Grayson", "Bruce"},
		{"Bruce Wayne, Dick Grayson", "Dick Grayson"},
		{"Ren\u00e9e Montoya", "Montoya"},
		{"Ren\u00e9e Montoya", "Rene\u0301e"}, // a decomposed accent
		{"Ludwig van Beethoven", "van Beethoven"},
		{"Ludwig van Beethoven", "Beethoven"},
		{"Ludwig van Beethoven", "Ludwig"},
	}
	for _, tc := range cases {
		c := cand(tc.base + " 03")
		c.AuthorName = tc.author
		if v, held := Demote(c); !held || v.Code != CodeAuthorBase {
			t.Errorf("author %q base %q: got %+v held=%v, want %s", tc.author, tc.base, v, held, CodeAuthorBase)
		}
	}
}

// The part check does not reach for names that are not parts of the author's.
func TestDemote_ABaseThatIsNotAPartOfTheAuthorsNameIsNotHeld(t *testing.T) {
	for _, tc := range []struct{ author, base string }{
		{"Bruce Wayne", "Court of Owls"},
		{"Bruce Wayne", "Waynes World"},    // contains a name, is not one
		{"Bruce Wayne", "Wayne Manor"},     // a name plus a word
		{"Bruce Wayne", "Brucey"},          // a longer word
		{"Bruce Wayne", "Way"},             // a prefix of a name
		{"Bruce T. Wayne", "Thomas Wayne"}, // the initial is not Thomas
		{"Bruce Wayne & Dick Grayson", "Wayne Grayson"},
		{"Bruce Wayne & Dick Grayson", "Bruce Grayson"}, // across two credits
	} {
		c := cand(tc.base + " 03")
		c.AuthorName = tc.author
		if v, held := Demote(c); held {
			t.Errorf("author %q base %q: held as %s (%s), want it to pass", tc.author, tc.base, v.Code, v.Reason)
		}
	}
}
