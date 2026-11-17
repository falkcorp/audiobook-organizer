// file: internal/seriesdenumber/demote_test.go
// version: 2.0.0
// guid: c2a81e47-5f30-4d96-b7a3-19e60d8f4b25
// last-edited: 2026-11-17

package seriesdenumber

import (
	"math/rand"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

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

// Every plan the pipeline lets through is rebuilt exactly from its fields by an
// oracle that shares no code with ParseName: the name is base + separator +
// optional marker + N, the base has no digit, and N is the position written.
func TestDemote_EligiblePlansRebuildTheirNameExactly(t *testing.T) {
	pool := []string{
		"Gotham", "Knights", "Court", "of", "Owls", "Part", "Pt.", "Unabridged", "Disc", "CD", "Book", "Vol.", "Volume",
		"No", "#", "Tome", "Band", "and", "&", "to", "-", ":", ",", "01", "2", "05", "66", "1989", ".5", "2.5", "(Special)",
		"'", "III", "Bat", "BW", "Edition", "99", "100", "0", "Wayne", "Bruce",
	}
	oracle := regexp.MustCompile(`(?i)^(.+?)(?:\s*[-:,\x{2013}\x{2014}]\s*|\s+)(?:(?:book|bk|vol|volume|no)\.?\s*|#\s*)?(\d{1,3})$`)
	rng := rand.New(rand.NewSource(7))
	authors := NewAuthorSet([]string{"Bruce Wayne"})
	eligible := 0
	for i := 0; i < 60000; i++ {
		var b strings.Builder
		for j, n := 0, 1+rng.Intn(6); j < n; j++ {
			if j > 0 && rng.Intn(6) != 0 {
				b.WriteByte(' ')
			}
			b.WriteString(pool[rng.Intn(len(pool))])
		}
		name := b.String()
		norm := strings.Join(strings.Fields(name), " ")
		// The planner's reading is taken from the oracle, so the property does not
		// lean on ParseName for the plan's fields.
		c := Candidate{FromName: name, AuthorID: 3, Authors: authors}
		if m := oracle.FindStringSubmatch(norm); m != nil {
			c.Base = m[1]
			c.Position, _ = strconv.Atoi(m[2])
		} else {
			c.Base, c.Position = norm, 1
		}
		if _, held := Demote(c); held {
			continue
		}
		eligible++
		m := oracle.FindStringSubmatch(norm)
		if m == nil {
			t.Fatalf("%q is eligible but is not base + separator + marker + number", name)
		}
		if strings.ContainsAny(c.Base, "0123456789") {
			t.Fatalf("%q is eligible with a digit in the base %q", name, c.Base)
		}
		if n, _ := strconv.Atoi(m[2]); n != c.Position || n < 1 || n > MaxPosition {
			t.Fatalf("%q is eligible at position %d, the name says %s", name, c.Position, m[2])
		}
		if !strings.HasPrefix(strings.ToLower(norm), strings.ToLower(c.Base)) {
			t.Fatalf("%q: base %q is not a prefix of the name", name, c.Base)
		}
		if strings.EqualFold(norm[:len(c.Base)], "Bruce Wayne") {
			t.Fatalf("%q is eligible with an author's name as its base", name)
		}
	}
	if eligible < 200 {
		t.Fatalf("only %d of 60000 generated plans were eligible: the generator no longer reaches the accept path", eligible)
	}
	t.Logf("%d of 60000 generated plans eligible", eligible)
}
