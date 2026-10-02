// file: internal/plugins/metafetch/asin_match_test.go
// version: 1.3.0
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
			if _, got := asinTitleMatch(tc.book, tc.cand, tc.sub); got != tc.want {
				t.Fatalf("asinTitleMatch(%q, %q, %q) = %v, want %v", tc.book, tc.cand, tc.sub, got, tc.want)
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
			book: func() asinBookFacts {
				b := redRisingBook()
				b.Authors = []string{"Dan Brown"}
				b.RuntimeSec = 972 * 60
				return b
			},
			cand: redRising, reason: rejectAuthor,
		},
		{
			name:   "numbered title rejects before anything else",
			book:   func() asinBookFacts { b := redRisingBook(); b.RuntimeSec = 972 * 60; return b },
			cand:   func() metadata.AudibleIdentity { c := redRising(); c.Title = "Red Rising 2"; return c },
			reason: rejectTitle,
		},
		{
			name:   "empty ASIN rejects",
			book:   func() asinBookFacts { b := redRisingBook(); b.RuntimeSec = 972 * 60; return b },
			cand:   func() metadata.AudibleIdentity { c := redRising(); c.ASIN = ""; return c },
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

// TestDecideASIN_ReviewFalsePositives pins the false positives the 2026-10-01
// adversarial review reproduced against the gate. Every case used to come
// back matched.
func TestDecideASIN_ReviewFalsePositives(t *testing.T) {
	zahn := []string{"Timothy Zahn"}
	cases := []struct {
		name  string
		book  asinBookFacts
		cands []metadata.AudibleIdentity
	}{
		{
			name: "franchise head title, runtime alone",
			book: asinBookFacts{Title: "Star Wars", Authors: zahn, RuntimeSec: 15 * 3600},
			cands: []metadata.AudibleIdentity{
				{ASIN: "B1", Title: "Star Wars: Heir to the Empire", Authors: zahn, RuntimeMin: 860},
				{ASIN: "B2", Title: "Star Wars: Dark Force Rising", Authors: zahn, RuntimeMin: 1020},
				{ASIN: "B3", Title: "Star Wars: Thrawn", Authors: zahn, RuntimeMin: 1250},
			},
		},
		{
			name: "book's own (Book 2) stripped, runtime alone",
			book: asinBookFacts{Title: "Red Rising (Book 2)", Authors: []string{"Pierce Brown"}, RuntimeSec: 17 * 3600},
			cands: []metadata.AudibleIdentity{
				{ASIN: "B00I2VWW5U", Title: "Red Rising", Authors: []string{"Pierce Brown"}, RuntimeMin: 973,
					Series: []metadata.AudibleSeriesRef{{Title: "Red Rising", Sequence: "1"}}},
			},
		},
		{
			name: "box set by series range",
			book: asinBookFacts{Title: "Red Rising", Authors: []string{"Pierce Brown"}, SeriesName: "Red Rising Saga", SeriesSeq: 1},
			cands: []metadata.AudibleIdentity{
				{ASIN: "BOX", Title: "Red Rising", Subtitle: "Books 1-3", Authors: []string{"Pierce Brown"},
					Series: []metadata.AudibleSeriesRef{{Title: "Red Rising Saga", Sequence: "1-3"}}},
			},
		},
		{
			name: "box set by range sequence only",
			book: asinBookFacts{Title: "Red Rising", Authors: []string{"Pierce Brown"}, SeriesName: "Red Rising Saga", SeriesSeq: 1},
			cands: []metadata.AudibleIdentity{
				{ASIN: "BOX", Title: "Red Rising", Authors: []string{"Pierce Brown"},
					Series: []metadata.AudibleSeriesRef{{Title: "Red Rising Saga", Sequence: "1-3"}}},
			},
		},
		{
			name: "abridged, series only",
			book: asinBookFacts{Title: "Dune", Authors: []string{"Frank Herbert"}, SeriesName: "Dune", SeriesSeq: 1},
			cands: []metadata.AudibleIdentity{
				{ASIN: "ABR", Title: "Dune", FormatType: "abridged", Authors: []string{"Frank Herbert"},
					Series: []metadata.AudibleSeriesRef{{Title: "Dune", Sequence: "1"}}},
			},
		},
		{
			name: "dramatized, series only",
			book: asinBookFacts{Title: "Dune", Authors: []string{"Frank Herbert"}, SeriesName: "Dune", SeriesSeq: 1},
			cands: []metadata.AudibleIdentity{
				{ASIN: "DRA", Title: "Dune", Subtitle: "Dramatized Adaptation", Authors: []string{"Frank Herbert"},
					Series: []metadata.AudibleSeriesRef{{Title: "Dune", Sequence: "1"}}},
			},
		},
		{
			name:  "spelled-out volume",
			book:  asinBookFacts{Title: "The Wheel of Time", Authors: []string{"Robert Jordan"}, RuntimeSec: 30 * 3600},
			cands: []metadata.AudibleIdentity{{ASIN: "W13", Title: "The Wheel of Time: Book Thirteen", Authors: []string{"Robert Jordan"}, RuntimeMin: 1800}},
		},
		{
			name:  "season N",
			book:  asinBookFacts{Title: "Hitchhiker", Authors: []string{"Douglas Adams"}, RuntimeSec: 5 * 3600},
			cands: []metadata.AudibleIdentity{{ASIN: "S2", Title: "Hitchhiker: Season 2", Authors: []string{"Douglas Adams"}, RuntimeMin: 300}},
		},
		{
			name:  "part N of M",
			book:  asinBookFacts{Title: "Les Miserables", Authors: []string{"Victor Hugo"}, RuntimeSec: 20 * 3600},
			cands: []metadata.AudibleIdentity{{ASIN: "P1", Title: "Les Miserables: Part 1 of 3", Authors: []string{"Victor Hugo"}, RuntimeMin: 1200}},
		},
		{
			name:  "Henry, James is not Henry James",
			book:  asinBookFacts{Title: "The Ambassadors", Authors: []string{"Henry, James"}, RuntimeSec: 18 * 3600},
			cands: []metadata.AudibleIdentity{{ASIN: "HJ", Title: "The Ambassadors", Authors: []string{"Henry James"}, RuntimeMin: 1080}},
		},
		{
			name:  "omnibus subtitle",
			book:  asinBookFacts{Title: "Foundation", Authors: []string{"Isaac Asimov"}, SeriesName: "Foundation", SeriesSeq: 1},
			cands: []metadata.AudibleIdentity{{ASIN: "OMN", Title: "Foundation", Subtitle: "The Omnibus", Authors: []string{"Isaac Asimov"}, Series: []metadata.AudibleSeriesRef{{Title: "Foundation", Sequence: "1"}}}},
		},
	}
	ol := []string{"Kugane Maruyama"}
	cases = append(cases, []struct {
		name  string
		book  asinBookFacts
		cands []metadata.AudibleIdentity
	}{
		{
			name:  "series-note subtitle with another volume, no known position",
			book:  asinBookFacts{Title: "Overlord", Authors: ol, RuntimeSec: 10 * 3600},
			cands: []metadata.AudibleIdentity{{ASIN: "O2", Title: "Overlord", Subtitle: "Overlord, Vol. 2", Authors: ol, RuntimeMin: 610}},
		},
		{
			name:  "Book N of the X Series, no known position",
			book:  asinBookFacts{Title: "Overlord", Authors: ol, RuntimeSec: 10 * 3600},
			cands: []metadata.AudibleIdentity{{ASIN: "O3", Title: "Overlord", Subtitle: "Book 2 of the Overlord Series", Authors: ol, RuntimeMin: 610}},
		},
		{
			name:  "companion novel subtitle",
			book:  asinBookFacts{Title: "Overlord", Authors: ol, RuntimeSec: 10 * 3600},
			cands: []metadata.AudibleIdentity{{ASIN: "O5", Title: "Overlord", Subtitle: "A Companion Novel to the Overlord Series", Authors: ol, RuntimeMin: 610}},
		},
		{
			name: "series-note volume vs known position, qualified series name",
			book: asinBookFacts{Title: "Overlord", Authors: ol, RuntimeSec: 10 * 3600, SeriesName: "Overlord", SeriesSeq: 1},
			cands: []metadata.AudibleIdentity{{ASIN: "O7", Title: "Overlord", Subtitle: "Overlord, Vol. 2", Authors: ol, RuntimeMin: 610,
				Series: []metadata.AudibleSeriesRef{{Title: "Overlord (Light Novel)", Sequence: "2"}}}},
		},
		{
			name: "qualified series name still vetoes",
			book: asinBookFacts{Title: "Overlord", Authors: ol, RuntimeSec: 10 * 3600, SeriesName: "Overlord", SeriesSeq: 1},
			cands: []metadata.AudibleIdentity{{ASIN: "O8", Title: "Overlord", Authors: ol, RuntimeMin: 610,
				Series: []metadata.AudibleSeriesRef{{Title: "Overlord (Light Novel)", Sequence: "2"}}}},
		},
		{
			name: "paren volume vs universe series",
			book: asinBookFacts{Title: "Mistborn (Book 2)", Authors: []string{"Brandon Sanderson"}, RuntimeSec: 20 * 3600},
			cands: []metadata.AudibleIdentity{{ASIN: "MB1", Title: "Mistborn", Authors: []string{"Brandon Sanderson"}, RuntimeMin: 1200,
				Series: []metadata.AudibleSeriesRef{{Title: "Mistborn", Sequence: "1"}, {Title: "Cosmere", Sequence: "2"}}}},
		},
		{
			name: "paren (Series 3, Book 2) takes the book number",
			book: asinBookFacts{Title: "Foo (Series 3, Book 2)", Authors: []string{"A Writer"}, RuntimeSec: 10 * 3600},
			cands: []metadata.AudibleIdentity{{ASIN: "F3", Title: "Foo", Authors: []string{"A Writer"}, RuntimeMin: 600,
				Series: []metadata.AudibleSeriesRef{{Title: "Foo", Sequence: "3"}}}},
		},
	}...)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if d := decideASIN(tc.book, tc.cands); d.Outcome == asinOutcomeMatched {
				t.Fatalf("matched %s (evidence %v); want no match", d.ASIN, d.Evidence)
			}
		})
	}
}

// TestDecideASIN_ReviewTighteningKeepsGoodMatches is the control set: the
// tightened gate must still match these.
func TestDecideASIN_ReviewTighteningKeepsGoodMatches(t *testing.T) {
	cases := []struct {
		name string
		book asinBookFacts
		cand metadata.AudibleIdentity
	}{
		{
			name: "exact title, runtime",
			book: asinBookFacts{Title: "Red Rising", Authors: []string{"Pierce Brown"}, RuntimeSec: 972 * 60},
			cand: metadata.AudibleIdentity{ASIN: "RR", Title: "Red Rising", Authors: []string{"Pierce Brown"}, RuntimeMin: 972},
		},
		{
			name: "series-note subtitle naming the book's position is exact",
			book: asinBookFacts{Title: "Red Rising", Authors: []string{"Pierce Brown"}, RuntimeSec: 972 * 60, SeriesName: "Red Rising Saga", SeriesSeq: 1},
			cand: metadata.AudibleIdentity{ASIN: "RR", Title: "Red Rising", Subtitle: "Red Rising Saga, Book 1", Authors: []string{"Pierce Brown"}, RuntimeMin: 972},
		},
		{
			name: "(Book 2) agrees with the product's position",
			book: asinBookFacts{Title: "Golden Son (Book 2)", Authors: []string{"Pierce Brown"}, RuntimeSec: 1100 * 60},
			cand: metadata.AudibleIdentity{ASIN: "GS", Title: "Golden Son", Authors: []string{"Pierce Brown"}, RuntimeMin: 1100,
				Series: []metadata.AudibleSeriesRef{{Title: "Red Rising", Sequence: "2"}}},
		},
		{
			name: "partial title with ISBN",
			book: asinBookFacts{Title: "Leviathan Wakes", Authors: []string{"James S. A. Corey"}, ISBNs: []string{"9781611133158"}},
			cand: metadata.AudibleIdentity{ASIN: "LW", Title: "Leviathan Wakes: The Expanse, Book 1", Authors: []string{"James S. A. Corey"}, ISBN: "9781611133158"},
		},
		{
			name: "partial title with runtime and series",
			book: asinBookFacts{Title: "Leviathan Wakes", Authors: []string{"James S. A. Corey"}, RuntimeSec: 1240 * 60, SeriesName: "The Expanse", SeriesSeq: 1},
			cand: metadata.AudibleIdentity{ASIN: "LW", Title: "Leviathan Wakes: A Novel of the Expanse", Authors: []string{"James S. A. Corey"}, RuntimeMin: 1240,
				Series: []metadata.AudibleSeriesRef{{Title: "The Expanse", Sequence: "1"}}},
		},
		{
			name: "Le Guin, Ursula K. swap",
			book: asinBookFacts{Title: "The Dispossessed", Authors: []string{"Le Guin, Ursula K."}, RuntimeSec: 900 * 60},
			cand: metadata.AudibleIdentity{ASIN: "UD", Title: "The Dispossessed", Authors: []string{"Ursula K. Le Guin"}, RuntimeMin: 900},
		},
		{
			name: "series-note subtitle volume equals known position",
			book: asinBookFacts{Title: "Overlord", Authors: []string{"Kugane Maruyama"}, RuntimeSec: 10 * 3600, SeriesName: "Overlord", SeriesSeq: 2},
			cand: metadata.AudibleIdentity{ASIN: "O2", Title: "Overlord", Subtitle: "Overlord, Vol. 2", Authors: []string{"Kugane Maruyama"}, RuntimeMin: 610},
		},
		{
			name: "year in the parenthetical is not a volume",
			book: asinBookFacts{Title: "Dune (Unabridged, 2019)", Authors: []string{"Frank Herbert"}, RuntimeSec: 1260 * 60},
			cand: metadata.AudibleIdentity{ASIN: "DU", Title: "Dune", Authors: []string{"Frank Herbert"}, RuntimeMin: 1260},
		},
		{
			name: "abridged with runtime corroboration",
			book: asinBookFacts{Title: "Dune", Authors: []string{"Frank Herbert"}, RuntimeSec: 300 * 60},
			cand: metadata.AudibleIdentity{ASIN: "ABR", Title: "Dune", FormatType: "abridged", Authors: []string{"Frank Herbert"}, RuntimeMin: 300},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if d := decideASIN(tc.book, []metadata.AudibleIdentity{tc.cand}); d.Outcome != asinOutcomeMatched {
				t.Fatalf("outcome %s rejects %v; want matched", d.Outcome, d.Rejects)
			}
		})
	}
}

// TestDecideASIN_Round3SeriesNoteVolumes pins the round-3 review's sibling
// volumes: word and roman numbers, split parts, and a note naming another
// series. Each used to match on runtime alone.
func TestDecideASIN_Round3SeriesNoteVolumes(t *testing.T) {
	ol := []string{"Kugane Maruyama"}
	at1 := asinBookFacts{Title: "Overlord", Authors: ol, RuntimeSec: 10 * 3600, SeriesName: "Overlord", SeriesSeq: 1}
	none := asinBookFacts{Title: "Overlord", Authors: ol, RuntimeSec: 10 * 3600}
	cand := func(sub string) []metadata.AudibleIdentity {
		return []metadata.AudibleIdentity{{ASIN: "OX", Title: "Overlord", Subtitle: sub, Authors: ol, RuntimeMin: 610}}
	}
	bad := []struct {
		name string
		book asinBookFacts
		sub  string
	}{
		{"word number, no position", none, "Book Two of the Overlord Series"},
		{"word number vs #1", at1, "Book Two of the Overlord Series"},
		{"roman vs #1", at1, "Overlord, Volume II"},
		{"ordinal word vs #1", at1, "The Second Book of the Overlord Saga"},
		{"volume 1 part 2 vs #1", at1, "Overlord Series, Volume 1, Part 2"},
		{"note names another series", at1, "The First Law, Book 1"},
		{"unreadable volume token", at1, "Overlord, Book Something"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if d := decideASIN(tc.book, cand(tc.sub)); d.Outcome == asinOutcomeMatched {
				t.Fatalf("matched %s (evidence %v); want no match", d.ASIN, d.Evidence)
			}
		})
	}
	at2 := at1
	at2.SeriesSeq = 2
	good := []struct {
		name string
		book asinBookFacts
		sub  string
	}{
		{"word number equals position", at1, "Book One of the Overlord Series"},
		{"roman equals position", at2, "Overlord, Volume II"},
		{"digit equals position", at2, "Overlord, Vol. 2"},
		{"note with a year only", at1, "Overlord Series, Book 1 (2016)"},
		{"no number at all", none, "The Overlord Series"},
	}
	for _, tc := range good {
		t.Run(tc.name, func(t *testing.T) {
			if d := decideASIN(tc.book, cand(tc.sub)); d.Outcome != asinOutcomeMatched {
				t.Fatalf("outcome %s rejects %v; want matched", d.Outcome, d.Rejects)
			}
		})
	}
	// A split part matches only with the ISBN.
	split := at1
	split.ISBNs = []string{"9781975300000"}
	c := cand("Overlord Series, Volume 1, Part 2")
	c[0].ISBN = "9781975300000"
	if d := decideASIN(split, c); d.Outcome != asinOutcomeMatched {
		t.Fatalf("split part with ISBN: outcome %s rejects %v", d.Outcome, d.Rejects)
	}
}

// Sub-series names are not folded together: two Discworld sub-series do not
// agree just because both reduce to "discworld". Format parentheticals are
// still folded.
func TestNormSeriesName_FormatOnly(t *testing.T) {
	if normSeriesName("Discworld (City Watch)") == normSeriesName("Discworld (Rincewind)") {
		t.Fatal("sub-series folded together")
	}
	if normSeriesName("The Expanse Universe") == normSeriesName("The Expanse") {
		t.Fatal("universe folded into the series")
	}
	if normSeriesName("Overlord (Light Novel)") != normSeriesName("Overlord") {
		t.Fatal("format parenthetical not folded")
	}
	if normSeriesName("Dune Chronicles") != normSeriesName("Dune") {
		t.Fatal("chronicles qualifier not folded")
	}
}
