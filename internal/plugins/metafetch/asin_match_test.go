// file: internal/plugins/metafetch/asin_match_test.go
// version: 1.1.0
// guid: 8a2d6e41-0b7c-4f93-a5e8-1c9f3d7b2a60
// last-edited: 2026-10-01

package metafetch

import (
	"reflect"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

func TestASINTitleMatches(t *testing.T) {
	cases := []struct {
		name            string
		book, cand, sub string
		want            bool
	}{
		{"exact", "Red Rising", "Red Rising", "", true},
		{"case and punctuation", "Ender's Game", "enders game", "", true},
		{"leading the", "The Way of Kings", "Way of Kings", "", true},
		{"ampersand", "Pride & Prejudice", "Pride and Prejudice", "", true},
		{"numbered title rejects", "Red Rising", "Red Rising 2", "", false},
		{"numbered title rejects reversed", "Red Rising 2", "Red Rising", "", false},
		{"book N suffix rejects", "Red Rising", "Red Rising Book 2", "", false},
		{"colon number designator rejects", "Red Rising", "Red Rising: Book 2", "", false},
		{"colon roman designator rejects", "Dune", "Dune: Part II", "", false},
		{"dash number designator rejects", "Dune - 2", "Dune", "", false},
		{"hash designator rejects", "Dune: #3", "Dune", "", false},
		{"prefix without separator rejects (old 60% rule)", "Shadows of Self", "Shadows of Selfish Things", "", false},
		{"different work rejects", "Dune", "Dune Messiah", "", false},
		{"book subtitle, cand bare", "Shadows of Self: A Mistborn Novel", "Shadows of Self", "", true},
		{"cand subtitle in title, book bare", "Shadows of Self", "Shadows of Self: A Mistborn Novel", "", true},
		{"audible subtitle field completes title", "Golden Son: Book II of the Red Rising Trilogy", "Golden Son", "Book II of the Red Rising Trilogy", true},
		{"audible subtitle field, book bare", "Golden Son", "Golden Son", "Book II of the Red Rising Trilogy", true},
		{"audible subtitle is only a number rejects", "Foundation", "Foundation", "Book 2", false},
		{"different subtitles both sides reject", "Foo: Part One of Three", "Foo: The Return", "", false},
		{"book subtitle vs different audible subtitle rejects", "Foo: A Novel", "Foo", "The Lost Years", false},
		{"series parenthetical stripped", "Red Rising (Red Rising Saga, Book 1)", "Red Rising", "", true},
		{"unabridged parenthetical stripped", "Red Rising (Unabridged)", "Red Rising", "", true},
		{"dramatized bracket kept", "Light Bringer", "Light Bringer [Dramatized Adaptation]", "", false},
		{"split release kept", "Light Bringer", "Light Bringer (1 of 3)", "", false},
		{"book N of M split release kept", "Light Bringer", "Light Bringer (Book 1 of 3)", "", false},
		{"bracket naming a book is kept", "Red Rising", "Red Rising [Dramatized Adaptation of Book 1]", "", false},
		{"hyphenated word is not a separator", "Harry Potter and the Half-Blood Prince", "Harry Potter and the Half-Blood Prince", "", true},
		{"empty book title rejects", "", "Anything", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := asinTitleMatches(tc.book, tc.cand, tc.sub); got != tc.want {
				t.Fatalf("asinTitleMatches(%q, %q, %q) = %v, want %v", tc.book, tc.cand, tc.sub, got, tc.want)
			}
		})
	}
}

func TestASINAuthorMatches(t *testing.T) {
	cases := []struct {
		name       string
		book, cand []string
		want       bool
	}{
		{"same", []string{"Pierce Brown"}, []string{"Pierce Brown"}, true},
		{"initials spacing", []string{"J.R.R. Tolkien"}, []string{"J. R. R. Tolkien"}, true},
		{"last, first", []string{"Brown, Pierce"}, []string{"Pierce Brown"}, true},
		{"co-author on book side", []string{"Someone Else", "Pierce Brown"}, []string{"Pierce Brown"}, true},
		{"co-author on product side", []string{"Pierce Brown"}, []string{"A Translator", "Pierce Brown"}, true},
		{"role suffix", []string{"Ken Liu"}, []string{"Ken Liu - translator"}, true},
		{"mismatch", []string{"Pierce Brown"}, []string{"Dan Brown"}, false},
		{"no book author", nil, []string{"Pierce Brown"}, false},
		{"multi-word last name swapped", []string{"Le Guin, Ursula K."}, []string{"Ursula K. Le Guin"}, true},
		{"list on product side is not a person", []string{"Tim Reynolds"}, []string{"Pierce Brown, Tim Reynolds"}, false},
		{"role cut before comparing", []string{"Ken Liu"}, []string{"Ken Liu - foreword"}, true},
		{"suffix not swapped", []string{"Martin Luther King, Jr."}, []string{"Jr. Martin Luther King"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := asinAuthorMatches(tc.book, tc.cand); got != tc.want {
				t.Fatalf("asinAuthorMatches(%v, %v) = %v, want %v", tc.book, tc.cand, got, tc.want)
			}
		})
	}
}

func TestNormISBN13(t *testing.T) {
	cases := map[string]string{
		"9781470380281":     "9781470380281",
		"978-1-4703-8028-1": "9781470380281",
		"0345539788":        "9780345539786", // ISBN-10 -> 13 with recomputed check digit
		"080442957X":        "9780804429573",
		"12345":             "",
		"":                  "",
	}
	for in, want := range cases {
		if got := normISBN13(in); got != want {
			t.Errorf("normISBN13(%q) = %q, want %q", in, got, want)
		}
	}
}

// redRising is a well-formed Audible product for the book below.
func redRising() metadata.AudibleIdentity {
	return metadata.AudibleIdentity{
		ASIN: "B00I2VWW5U", Title: "Red Rising", Authors: []string{"Pierce Brown"},
		Series:     []metadata.AudibleSeriesRef{{Title: "Red Rising", Sequence: "1"}},
		RuntimeMin: 972, ISBN: "9781470380281",
	}
}

func redRisingBook() asinBookFacts {
	return asinBookFacts{Title: "Red Rising", Authors: []string{"Pierce Brown"}}
}

func TestEvaluateASINCandidate(t *testing.T) {
	cases := []struct {
		name   string
		book   func() asinBookFacts
		cand   func() metadata.AudibleIdentity
		pass   bool
		reason string
		ev     []string
	}{
		{
			name: "no corroboration rejects even with title+author",
			book: redRisingBook, cand: redRising,
			reason: rejectUncorroborated,
		},
		{
			name: "runtime within 10% corroborates",
			book: func() asinBookFacts { b := redRisingBook(); b.RuntimeSec = 972*60 + 300; return b },
			cand: redRising, pass: true, ev: []string{evidenceRuntime},
		},
		{
			name: "runtime 15% off neither corroborates nor vetoes",
			book: func() asinBookFacts { b := redRisingBook(); b.RuntimeSec = int(972 * 60 * 1.15); return b },
			cand: redRising, reason: rejectUncorroborated,
		},
		{
			name: "runtime 40% off vetoes despite ISBN agreement",
			book: func() asinBookFacts {
				b := redRisingBook()
				b.RuntimeSec = int(972 * 60 * 0.6)
				b.ISBNs = []string{"9781470380281"}
				return b
			},
			cand: redRising, reason: rejectRuntimeConflict,
		},
		{
			name: "ISBN agreement corroborates",
			book: func() asinBookFacts { b := redRisingBook(); b.ISBNs = []string{"978-1-4703-8028-1"}; return b },
			cand: redRising, pass: true, ev: []string{evidenceISBN},
		},
		{
			name: "print ISBN disagreement is not a veto, just no evidence",
			book: func() asinBookFacts { b := redRisingBook(); b.ISBNs = []string{"9780345539786"}; return b },
			cand: redRising, reason: rejectUncorroborated,
		},
		{
			name: "series name and sequence corroborate",
			book: func() asinBookFacts {
				b := redRisingBook()
				b.SeriesName, b.SeriesSeq = "Red Rising Saga", 1
				return b
			},
			cand: redRising, pass: true, ev: []string{evidenceSeries},
		},
		{
			name: "same series, different sequence vetoes",
			book: func() asinBookFacts {
				b := redRisingBook()
				b.SeriesName, b.SeriesSeq = "Red Rising", 2
				b.RuntimeSec = 972 * 60
				return b
			},
			cand: redRising, reason: rejectSeriesConflict,
		},
		{
			name: "all three corroborate",
			book: func() asinBookFacts {
				b := redRisingBook()
				b.SeriesName, b.SeriesSeq = "Red Rising", 1
				b.RuntimeSec = 972 * 60
				b.ISBNs = []string{"9781470380281"}
				return b
			},
			cand: redRising, pass: true, ev: []string{evidenceISBN, evidenceRuntime, evidenceSeries},
		},
		{
			name: "author mismatch rejects",
			book: func() asinBookFacts { b := redRisingBook(); b.Authors = []string{"Dan Brown"}; b.RuntimeSec = 972 * 60; return b },
			cand: redRising, reason: rejectAuthor,
		},
		{
			name: "numbered title rejects before anything else",
			book: func() asinBookFacts { b := redRisingBook(); b.RuntimeSec = 972 * 60; return b },
			cand: func() metadata.AudibleIdentity { c := redRising(); c.Title = "Red Rising 2"; return c },
			reason: rejectTitle,
		},
		{
			name: "empty ASIN rejects",
			book: func() asinBookFacts { b := redRisingBook(); b.RuntimeSec = 972 * 60; return b },
			cand: func() metadata.AudibleIdentity { c := redRising(); c.ASIN = ""; return c },
			reason: rejectNoASIN,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := evaluateASINCandidate(tc.book(), tc.cand())
			if v.Pass != tc.pass {
				t.Fatalf("pass = %v (reason %q), want %v", v.Pass, v.Reason, tc.pass)
			}
			if !tc.pass && v.Reason != tc.reason {
				t.Fatalf("reason = %q, want %q", v.Reason, tc.reason)
			}
			if tc.pass && !reflect.DeepEqual(v.Evidence, tc.ev) {
				t.Fatalf("evidence = %v, want %v", v.Evidence, tc.ev)
			}
		})
	}
}

func TestDecideASIN(t *testing.T) {
	book := redRisingBook()
	book.RuntimeSec = 972 * 60

	t.Run("exactly one passer matches", func(t *testing.T) {
		other := redRising()
		other.ASIN, other.Title = "B0OTHER001", "Golden Son"
		d := decideASIN(book, []metadata.AudibleIdentity{other, redRising()})
		if d.Outcome != asinOutcomeMatched || d.ASIN != "B00I2VWW5U" {
			t.Fatalf("got %+v, want matched B00I2VWW5U", d)
		}
		if d.Rejects[rejectTitle] != 1 {
			t.Fatalf("rejects = %v, want one title reject", d.Rejects)
		}
	})

	t.Run("two distinct passers are ambiguous and match nothing", func(t *testing.T) {
		twin := redRising()
		twin.ASIN = "B0TWIN0001"
		d := decideASIN(book, []metadata.AudibleIdentity{redRising(), twin})
		if d.Outcome != asinOutcomeAmbiguous || d.ASIN != "" {
			t.Fatalf("got %+v, want ambiguous with no ASIN", d)
		}
		if len(d.Passing) != 2 {
			t.Fatalf("passing = %v, want 2", d.Passing)
		}
	})

	t.Run("the same ASIN from two searches is one candidate", func(t *testing.T) {
		d := decideASIN(book, []metadata.AudibleIdentity{redRising(), redRising()})
		if d.Outcome != asinOutcomeMatched {
			t.Fatalf("got %+v, want matched", d)
		}
	})

	t.Run("nothing passes", func(t *testing.T) {
		c := redRising()
		c.Authors = []string{"Someone Else"}
		d := decideASIN(book, []metadata.AudibleIdentity{c})
		if d.Outcome != asinOutcomeNoMatch || d.Rejects[rejectAuthor] != 1 {
			t.Fatalf("got %+v, want no_match with an author reject", d)
		}
	})
}
