// file: internal/applygate/titlecompare_test.go
// version: 1.0.0
// guid: 2f7a9d14-6c3e-4b58-8d01-e5a4b7c92f60
// last-edited: 2026-11-17

package applygate

import (
	"fmt"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// ctClassName names a class for failure messages.
func ctClassName(c titleClass) string {
	switch c {
	case classConflictNumber:
		return "conflict(number)"
	case classConflictWords:
		return "conflict(words)"
	case classUnrelated:
		return "unrelated"
	case classNeutral:
		return "neutral"
	case classAgree:
		return "agree"
	}
	return fmt.Sprint(int(c))
}

// ctRank orders classes by how permissive they are: any conflict is the least.
func ctRank(c titleClass) int {
	switch c {
	case classAgree:
		return 3
	case classNeutral:
		return 2
	case classUnrelated:
		return 1
	}
	return 0
}

func ctCtx(series []string, credits ...string) titleCtx {
	names := map[string]bool{}
	for _, s := range series {
		names[normText(s)] = true
	}
	return newTitleCtx(names, credits...)
}

// TestCompareTitles_Table pins the class of each pair, in both directions.
func TestCompareTitles_Table(t *testing.T) {
	gc, lotdk := []string{"Gotham Central"}, []string{"Legends of the Dark Knight"}
	cases := []struct {
		name    string
		a, b    string
		series  []string
		credits []string
		want    titleClass
	}{
		// agree: the same identifying title in another spelling.
		{"identical", "Dark Victory", "Dark Victory", nil, nil, classAgree},
		{"case and punctuation", "DARK VICTORY!", "Dark Victory", nil, nil, classAgree},
		{"diacritics", "Détective Comics", "Detective Comics", nil, nil, classAgree},
		{"leading article", "The Long Halloween", "Long Halloween", nil, nil, classAgree},
		{"ampersand and and", "Batman & Robin", "Batman and Robin", nil, nil, classAgree},
		{"unabridged parenthetical", "Batman: Year One (Unabridged)", "Batman: Year One", nil, nil, classAgree},
		{"edition suffix", "The Killing Joke: Deluxe Edition", "The Killing Joke", nil, nil, classAgree},
		{"edition suffix, mirror", "The Killing Joke", "The Killing Joke: Deluxe Edition", nil, nil, classAgree},
		{"trailing subtitle", "The Killing Joke: A Gotham Story", "The Killing Joke", nil, nil, classAgree},
		{"series prefix declared", "Gotham Central: Half a Life", "Half a Life", gc, nil, classAgree},
		{"series suffix declared", "Half a Life - Gotham Central", "Half a Life", gc, nil, classAgree},
		{"series and volume in a parenthetical", "Half a Life (Gotham Central, Book 2)", "Half a Life", gc, nil, classAgree},
		{"reordered segments", "A Death in the Family: Batman", "Batman: A Death in the Family", nil, nil, classAgree},
		{"digit and number word", "Batman: Year One", "Batman: Year 1", nil, nil, classAgree},
		{"digit and roman", "Hush: Part II", "Hush: Part 2", nil, nil, classAgree},
		{"number word and digit behind a marker", "Knightfall: Book Two", "Knightfall: Book 2", nil, nil, classAgree},
		{"decimal volume", "Dark Victory 1.5", "Dark Victory 1.5", nil, nil, classAgree},
		{"a title that is only a number", "1939", "1939", nil, nil, classAgree},
		{"author in the title", "Bruce Wayne - Dark Victory", "Dark Victory", nil, []string{"Bruce Wayne"}, classAgree},
		{"leading list number", "08 - The Long Halloween", "The Long Halloween", nil, nil, classAgree},
		{"multi-part suffix", "Dark Victory - 001", "Dark Victory", nil, nil, classAgree},
		{"the total of a part counter is not identity", "Hush: Part 1 of 2", "Hush: Part 1 of 3", nil, nil, classAgree},
		{"list ordinals differ, same title: where the files sit, not which book", "08 - The Long Halloween", "09 - The Long Halloween", nil, nil, classAgree},
		{"multi-part suffixes differ, same title", "Dark Victory - 001", "Dark Victory - 002", nil, nil, classAgree},
		{"part marker on one side only", "Dark Victory: Part 2", "Dark Victory", nil, nil, classAgree},
		{"narrator in the file name", "Hush - Alfred Pennyworth", "Hush: A Gotham Story", nil, []string{"Alfred Pennyworth"}, classAgree},
		{"series that is the whole title", "Gotham Central", "Gotham Central", gc, nil, classAgree},

		// neutral: related, but one side says something the other cannot confirm.
		{"volume number on one side", "Knightfall 2", "Knightfall", nil, nil, classNeutral},
		{"volume marker on one side", "Dark Victory: Book 2", "Dark Victory", nil, nil, classNeutral},
		{"a trailing subtitle that carries a volume number on one side", "Dark Victory: Gotham Year 2", "Dark Victory", nil, nil, classNeutral},
		{"prefix nobody accounts for", "Batman: Dark Victory", "Dark Victory", nil, nil, classNeutral},
		{"only the series name is shared", "Batman", "Batman: Dark Victory", []string{"Batman"}, nil, classNeutral},
		{"a longer title with the same start", "Dark Victory", "Dark Victory Returns", nil, nil, classNeutral},
		{"part and book are different notions", "Hush: Part 1", "Hush Book 1", nil, nil, classNeutral},
		{"number word on one side", "Batman: Year One", "Batman: Year", nil, nil, classNeutral},
		{"a collected range and a single book", "Knightfall, Books 1-3", "Knightfall: Book 1", nil, nil, classNeutral},

		// conflict(number): the same words with a number both sides give differently.
		{"part 1 against part 2", "Hush: Part 1", "Hush: Part 2", nil, nil, classConflictNumber},
		{"part 2 against part 1", "Hush: Part 2", "Hush: Part 1", nil, nil, classConflictNumber},
		{"part I against part II", "Hush: Part I", "Hush: Part II", nil, nil, classConflictNumber},
		{"part II against part I", "Hush: Part II", "Hush: Part I", nil, nil, classConflictNumber},
		{"year 2 against year one", "Batman: Year 2", "Batman: Year One", nil, nil, classConflictNumber},
		{"part 1 of 2 against part 2 of 2", "Hush: Part 1 of 2", "Hush: Part 2 of 2", nil, nil, classConflictNumber},
		{"year one against year two, a number in the name", "Two-Face: Year One", "Two-Face: Year Two", nil, nil, classConflictNumber},
		{"part and volume against another part", "Hush: Part 1, Vol 2", "Hush: Part 2", nil, nil, classConflictNumber},
		{"numbered issue", "Detective Comics 27", "Detective Comics 28", nil, nil, classConflictNumber},
		{"a number inside a longer title", "Batman: Gotham by Gaslight 2", "Batman: Gotham by Gaslight 3", nil, nil, classConflictNumber},
		{"year", "Best of Gotham 2009", "Best of Gotham 2010", nil, nil, classConflictNumber},
		{"single-letter numeral at the end", "Gotham Gazette V", "Gotham Gazette VI", nil, nil, classConflictNumber},
		{"numbers only", "1939", "1940", nil, nil, classConflictNumber},
		{"decimal against whole", "Dark Victory 1.5", "Dark Victory 1", nil, nil, classConflictNumber},
		{"hash numbers", "Batman #66", "Batman #67", nil, nil, classConflictNumber},

		// conflict(words): a shared segment, and a remainder on each side.
		{"two storylines of one franchise", "Batman: Dark Victory", "Batman: The Long Halloween", nil, nil, classConflictWords},
		{"two storylines, mirror", "Batman: The Long Halloween", "Batman: Dark Victory", nil, nil, classConflictWords},
		{"franchise declared as the series", "Batman: Dark Victory", "Batman: The Long Halloween", []string{"Batman"}, nil, classConflictWords},
		{"subtitle names the other storyline", "Batman: Dark Victory", "Batman: The Long Halloween: The Prelude to Dark Victory", nil, nil, classConflictWords},
		{"dash separated", "Gotham Central - Half a Life", "Gotham Central - Unresolved Targets", nil, nil, classConflictWords},
		{"series declared, different books", "Legends of the Dark Knight: Year One", "Legends of the Dark Knight: Knightfall", lotdk, nil, classConflictWords},

		// unrelated.
		{"different books", "Knightfall", "The Long Halloween", nil, nil, classUnrelated},
		{"one shared word", "Dark Victory", "The Dark Knight Returns", nil, nil, classUnrelated},
		{"blank", "", "Dark Victory", nil, nil, classUnrelated},
		{"only stop words", "Unabridged", "Dark Victory", nil, nil, classUnrelated},
	}
	if len(cases) < 40 {
		t.Fatalf("table has %d rows, want at least 40", len(cases))
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			x := ctCtx(c.series, c.credits...)
			got := compareTitles(c.a, c.b, x)
			if got.class != c.want {
				t.Errorf("compareTitles(%q, %q) = %s (%s), want %s", c.a, c.b, ctClassName(got.class), got.note, ctClassName(c.want))
			}
			back := compareTitles(c.b, c.a, x)
			if back.class != got.class {
				t.Errorf("not symmetric: (%q, %q) = %s but swapped = %s", c.a, c.b, ctClassName(got.class), ctClassName(back.class))
			}
		})
	}
}

// ctPool is a set of titles that exercise every shape the comparison knows.
var ctPool = []string{
	"Dark Victory", "Batman: Dark Victory", "Batman: The Long Halloween", "The Long Halloween",
	"Batman: The Long Halloween: The Prelude to Dark Victory", "Hush", "Hush: Part 1", "Hush: Part 2",
	"Hush: Part I", "Hush: Part II", "Hush Part 1", "Hush - Alfred Pennyworth", "Batman: Year One",
	"Batman: Year 2", "Batman: Year Two", "Two-Face: Year One", "Half a Life", "Gotham Central: Half a Life",
	"Half a Life (Gotham Central, Book 2)", "Knightfall", "Knightfall: Book 3", "Knightfall, Books 1-3",
	"08 - The Long Halloween", "Dark Victory - 001", "Dark Victory (Unabridged)", "The Killing Joke",
	"The Killing Joke: Deluxe Edition", "Batman & Robin", "Batman and Robin", "Detective Comics 27",
	"Detective Comics 28", "Gotham Gazette V", "Gotham Gazette VI", "Batman #66", "Batman #67",
	"Bruce Wayne - Dark Victory", "Dark Victory: A Batman Novel", "Dark Victory 1.5", "1939", "1940",
	"Gotham Central - Half a Life", "Gotham Central - Unresolved Targets", "A Death in the Family: Batman",
	"Batman: A Death in the Family", "", "Unabridged",
}

var ctContexts = []titleCtx{
	ctCtx(nil),
	ctCtx([]string{"Batman"}),
	ctCtx([]string{"Gotham Central", "Legends of the Dark Knight"}, "Bruce Wayne", "Alfred Pennyworth"),
}

// TestCompareTitles_Symmetric: swapping the two texts never changes the class,
// for every ordered pair of the pool under each context.
func TestCompareTitles_Symmetric(t *testing.T) {
	for xi, x := range ctContexts {
		for _, a := range ctPool {
			for _, b := range ctPool {
				ab, ba := compareTitles(a, b, x), compareTitles(b, a, x)
				if ab.class != ba.class {
					t.Errorf("ctx %d: (%q, %q) = %s but swapped = %s", xi, a, b, ctClassName(ab.class), ctClassName(ba.class))
				}
			}
		}
	}
}

// TestCompareTitles_AppendingANumberNeverRaisesTheClass is the metamorphic
// property: appending a part, book, year or bare number (7, which is in no pool
// title) to either side of a pair can only keep or lower how permissive the
// class is. Run from every base class, conflicts included.
func TestCompareTitles_AppendingANumberNeverRaisesTheClass(t *testing.T) {
	suffixes := []string{" Part 7", ": Part 7", " - Part 7", " (Part 7)", ": Book 7", " Book 7", " Vol. 7", " 7", " - 7", " VII", ": Part VII", " Part 7 of 9"}
	checked := 0
	for xi, x := range ctContexts {
		for _, a := range ctPool {
			for _, b := range ctPool {
				base := compareTitles(a, b, x)
				for _, sfx := range suffixes {
					for side, pair := range [][2]string{{a + sfx, b}, {a, b + sfx}} {
						got := compareTitles(pair[0], pair[1], x)
						checked++
						if ctRank(got.class) > ctRank(base.class) {
							t.Errorf("ctx %d side %d: (%q, %q) is %s but appending %q gives (%q, %q) = %s",
								xi, side, a, b, ctClassName(base.class), sfx, pair[0], pair[1], ctClassName(got.class))
						}
					}
				}
			}
		}
	}
	if checked < 10000 {
		t.Fatalf("only %d pairs checked", checked)
	}
}

// ctTitleDecides runs the whole gate with the title check as one of exactly
// two agreements: the files' runtime is 7% off (neutral, not agree), the author
// agrees, so a pair applies only if the title check agrees, and a neutral
// title is insufficient_evidence.
func ctTitleDecides(book database.Book, cand metafetch.MetadataCandidate) Verdict {
	book.Duration = new(36000)
	book.Author = &database.Author{Name: "Bruce Wayne"}
	cand.Author = "Bruce Wayne"
	cand.DurationSec = 38520
	cand.Score = 0.95
	return Evaluate(&book, snap(&book), database.ComputeBookRuntime(&book, nil), &cand, nil)
}

// TestEvaluate_TitleCheckShapesStillApply: the shapes the segment variants
// exist for keep applying, with the title as one of only two agreements, so a
// check that silently fell to neutral would be caught.
func TestEvaluate_TitleCheckShapesStillApply(t *testing.T) {
	gc := "Gotham Central"
	cases := []struct {
		name string
		book database.Book
		cand metafetch.MetadataCandidate
	}{
		{"identical titles", database.Book{Title: "Dark Victory"}, metafetch.MetadataCandidate{Title: "Dark Victory"}},
		{"title against title and subtitle, the candidate's subtitle is extra",
			database.Book{Title: "The Killing Joke"}, metafetch.MetadataCandidate{Title: "The Killing Joke: Deluxe Edition"}},
		{"title against title with a subtitle field",
			database.Book{Title: "The Killing Joke"}, metafetch.MetadataCandidate{Title: "The Killing Joke", Subtitle: "A Gotham Story"}},
		{"series prefix, the candidate declares that series",
			database.Book{Title: "Gotham Central: Half a Life"}, metafetch.MetadataCandidate{Title: "Half a Life", Series: gc, SeriesPosition: ""}},
		{"series in the subtitle field, the candidate declares it",
			database.Book{Title: "Half a Life: Gotham Central"}, metafetch.MetadataCandidate{Title: "Half a Life", Subtitle: gc, Series: gc}},
		{"series and volume in a parenthetical on the candidate", database.Book{Title: "Half a Life"},
			metafetch.MetadataCandidate{Title: "Half a Life (Gotham Central, Book 2)", Series: gc}},
		{"unabridged suffix", database.Book{Title: "Dark Victory (Unabridged)"}, metafetch.MetadataCandidate{Title: "Dark Victory"}},
		{"case punctuation and diacritics", database.Book{Title: "DÉTECTIVE COMICS!"}, metafetch.MetadataCandidate{Title: "Detective Comics"}},
		{"article", database.Book{Title: "The Long Halloween"}, metafetch.MetadataCandidate{Title: "Long Halloween"}},
		{"ampersand", database.Book{Title: "Batman & Robin"}, metafetch.MetadataCandidate{Title: "Batman and Robin"}},
		{"series and subtitle swapped", database.Book{Title: "A Death in the Family: Batman"},
			metafetch.MetadataCandidate{Title: "Batman", Subtitle: "A Death in the Family", Series: "Batman"}},
		{"title on the file name, not the stored title", database.Book{Title: "Track 01", FilePath: "/lib/Bruce Wayne/Dark Victory/Dark Victory.m4b"},
			metafetch.MetadataCandidate{Title: "Dark Victory"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := ctTitleDecides(c.book, c.cand)
			if !v.Allowed {
				t.Fatalf("refused: %s (%s); title check %+v", v.Reason, v.Detail, titleCheck(v.Evidence))
			}
			if got := titleCheck(v.Evidence).Outcome; got != OutcomeAgree {
				t.Fatalf("title check %q, want agree (a neutral title would not apply with two agreements missing)", got)
			}
		})
	}
}

// TestEvaluate_TitleCheckConflictsRefuse: with runtime inside 5% and the author
// agreeing (ctGate), and again with the title as one of two agreements
// (ctTitleDecides), every conflicting pair is refused as title_disagrees.
func TestEvaluate_TitleCheckConflictsRefuse(t *testing.T) {
	cases := []struct {
		name string
		book database.Book
		cand metafetch.MetadataCandidate
	}{
		{"part 1 of 2 against part 2 of 2", database.Book{Title: "Hush: Part 1 of 2"}, metafetch.MetadataCandidate{Title: "Hush: Part 2 of 2"}},
		{"year one against year two", database.Book{Title: "Two-Face: Year One"}, metafetch.MetadataCandidate{Title: "Two-Face: Year Two"}},
		{"numbered issues (the sequence leg refuses first; the title check blocks too)", database.Book{Title: "Detective Comics 27"}, metafetch.MetadataCandidate{Title: "Detective Comics 28"}},
		{"candidate splits title and subtitle, stored is another storyline",
			database.Book{Title: "Batman: Dark Victory"}, metafetch.MetadataCandidate{Title: "Batman", Subtitle: "The Long Halloween"}},
		{"franchise declared as the series",
			database.Book{Title: "Batman: Dark Victory"}, metafetch.MetadataCandidate{Title: "Batman: The Long Halloween", Series: "Batman"}},
		{"the stored title's file name carries the other part",
			database.Book{Title: "Hush", FilePath: "/lib/Bruce Wayne/Hush/Hush Part 1.m4b"}, metafetch.MetadataCandidate{Title: "Hush: Part 2"}},
		{"nothing agrees and the file and folder name name another storyline",
			database.Book{Title: "Track 01", FilePath: "/lib/Bruce Wayne/Batman - The Long Halloween/Batman - The Long Halloween.m4b"},
			metafetch.MetadataCandidate{Title: "Batman: Dark Victory"}},
		{"stored title is a neutral match, the file and folder name name another storyline",
			database.Book{Title: "Dark Victory", FilePath: "/lib/Bruce Wayne/Batman - The Long Halloween/Batman - The Long Halloween.m4b"},
			metafetch.MetadataCandidate{Title: "Batman: Dark Victory"}},
		{"stored title and file name disagree with each other",
			database.Book{Title: "Hush: Part 1", FilePath: "/lib/Bruce Wayne/Hush/Hush Part 2.m4b"}, metafetch.MetadataCandidate{Title: "Hush: Part 1"}},
	}
	for _, c := range cases {
		for hname, run := range map[string]func(database.Book, metafetch.MetadataCandidate) Verdict{
			"runtime agrees": func(b database.Book, m metafetch.MetadataCandidate) Verdict { return ctGate(b, "", m) },
			"title decides":  ctTitleDecides,
		} {
			t.Run(c.name+"/"+hname, func(t *testing.T) {
				v := run(c.book, c.cand)
				if v.Allowed || (v.Reason != ReasonTitleDisagrees && v.Reason != ReasonSequenceMismatch) {
					t.Fatalf("allowed=%v reason=%q (%s), want refused as %q; title %+v", v.Allowed, v.Reason, v.Detail, ReasonTitleDisagrees, titleCheck(v.Evidence))
				}
				if got := titleCheck(v.Evidence); got.Outcome != OutcomeBlock || got.Reason != ReasonTitleDisagrees {
					t.Fatalf("title check %+v, want a title_disagrees block", got)
				}
			})
		}
	}
}

// TestEvaluate_TitleCheckNeutralNeedsTwoOtherAgreements: a related title that
// only one side can vouch for is neutral. It confirms nothing, so with the title
// as one of two agreements the pair is insufficient_evidence, and with the
// runtime also agreeing it still applies (it is not a conflict).
func TestEvaluate_TitleCheckNeutralNeedsTwoOtherAgreements(t *testing.T) {
	cases := []struct {
		name string
		book database.Book
		cand metafetch.MetadataCandidate
	}{
		{"prefix nobody accounts for", database.Book{Title: "Batman: Dark Victory"}, metafetch.MetadataCandidate{Title: "Dark Victory"}},
		{"a longer title with the same start", database.Book{Title: "Dark Victory"}, metafetch.MetadataCandidate{Title: "Dark Victory Returns"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := ctTitleDecides(c.book, c.cand)
			if v.Allowed || v.Reason != ReasonInsufficientEvidence {
				t.Fatalf("allowed=%v reason=%q (%s), want insufficient_evidence; title %+v", v.Allowed, v.Reason, v.Detail, titleCheck(v.Evidence))
			}
			if got := titleCheck(v.Evidence).Outcome; got != OutcomeNeutral {
				t.Fatalf("title check %q, want neutral", got)
			}
			if v := ctGate(c.book, "", c.cand); !v.Allowed {
				t.Fatalf("with an agreeing runtime a neutral title refused: %s (%s)", v.Reason, v.Detail)
			}
		})
	}
}
