// file: internal/plugins/maintenance/junk_author_fixer_round2_test.go
// version: 1.5.0
// guid: 993dd8c9-20d0-4bd4-951b-11471d5dff76
// last-edited: 2026-09-30

package maintenance

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// The junk-author re-plan of 2026-09-29 (op 01M3QZT0KV9H0K9B5MATDRM9N5)
// still proposed ~160 bad targets after #3631: a parser placeholder ("parse
// author", 49 rows), work titles stored as author rows ("Cthulhu Armageddon"
// 37, "Wraith Knight" 20, ...), a minted "Raymond L. Weil-Star Cross[1-5]"
// and "Cathfach (A (Not So) Simple Fetch Quest)". The work rows passed
// because hasOtherWork asks only whether the row has other books, and a junk
// row does. The fixtures below replay those shapes and the real authors whose
// names are also series names, which must keep passing.

// relinkRow plans a book in a version group with one sibling credited to
// target and returns the junk book's row.
func relinkRow(t *testing.T, f *junkFixture, plan *repairs.PlanResult, junk, bookID string) *repairs.Row {
	t.Helper()
	r := f.row(plan, junk, bookID)
	require.NotNil(t, r, "row for %q", junk)
	return r
}

func TestJunkAuthorFixer_WorkCreditedToAnotherAuthorIsRefused(t *testing.T) {
	f := newJunkFixture(t)
	// Series "Cthulhu Armageddon" is C. T. Phipps's.
	f.mkSeries("Cthulhu Armageddon", "C. T. Phipps")
	for i, title := range []string{"Cthulhu Armageddon", "The Tower of Zhaal", "The Tree of Azathoth"} {
		f.book(junkBookSpec{title: title, path: fmt.Sprintf("/lib/ctp/%d", i), author: "C. T. Phipps", series: "Cthulhu Armageddon"})
	}
	f.book(junkBookSpec{title: "Esoterrorism", path: "/lib/ctp/eso", author: "C. T. Phipps"})
	// The junk row of that name has books of its own that are not titled
	// with it (a folder dump), so hasOtherWork alone passes it.
	f.book(junkBookSpec{title: "Esoterrorism From the Secret File 2", path: "/lib/ca/1", author: "Cthulhu Armageddon"})
	f.book(junkBookSpec{title: "Wraith Knight Three Worlds, Book", path: "/lib/ca/2", author: "Cthulhu Armageddon", vg: "vg-ca"})
	id := f.book(junkBookSpec{title: "read by narrator", path: "/lib/j/ca", author: "read by narrator", vg: "vg-ca"})

	r := relinkRow(t, f, f.plan(), "read by narrator", id)
	require.NotEqual(t, "Cthulhu Armageddon", r.Proposed["author"], r.Reason)
	require.Equal(t, junkAuthorSkipAmbiguous, r.Skipped, "held, not unlinked: %v %s", r.Proposed, r.Reason)
	require.Contains(t, r.SkipReason, `credited to "C. T. Phipps"`)
}

// Real authors whose names are also series or title names keep passing:
// the series is credited only to them, or the other credit is a reader, a
// swapped folder parse, or a junk variant of the name.
func TestJunkAuthorFixer_AuthorNamedSeriesStillPasses(t *testing.T) {
	f := newJunkFixture(t)
	var cases []struct{ target, book string }
	add := func(target, vg string) {
		id := f.book(junkBookSpec{title: "read by narrator", path: "/lib/j/" + vg, author: "read by narrator", vg: vg})
		cases = append(cases, struct{ target, book string }{target, id})
	}

	// Series "Brent Weeks", credited only to Brent Weeks.
	f.mkSeries("Brent Weeks", "Brent Weeks")
	f.mkSeries("Night Angel", "Brent Weeks")
	f.book(junkBookSpec{title: "The Way of Shadows", path: "/lib/bw/1", author: "Brent Weeks", series: "Night Angel", vg: "vg-bw"})
	f.book(junkBookSpec{title: "Night Angel Nemesis", path: "/lib/bw/2", author: "Brent Weeks", series: "Brent Weeks"})
	add("Brent Weeks", "vg-bw")

	// Series "Cressida Cowell" also filed under her reader, David Tennant,
	// who narrates at least half as many books as he is credited with.
	f.mkSeries("Cressida Cowell", "Cressida Cowell")
	f.mkSeries("How to Train Your Dragon", "Cressida Cowell")
	for i := range 2 {
		f.book(junkBookSpec{title: fmt.Sprintf("How to Speak Dragonese %d", i), path: fmt.Sprintf("/lib/dt/%d", i),
			author: "David Tennant", series: "Cressida Cowell"})
		f.book(junkBookSpec{title: fmt.Sprintf("How to Be a Pirate %d", i), path: fmt.Sprintf("/lib/cc/p%d", i),
			author: "Cressida Cowell", narrator: "David Tennant"})
	}
	f.book(junkBookSpec{title: "How to Break a Dragon's Heart", path: "/lib/cc/1", author: "Cressida Cowell",
		series: "How to Train Your Dragon", vg: "vg-cc"})
	add("Cressida Cowell", "vg-cc")

	// A book titled "Joe Abercrombie" credited to "Before They Are Ha": a
	// folder parsed the wrong way round (that row has no other work).
	f.book(junkBookSpec{title: "Joe Abercrombie", path: "/lib/ja/x", author: "Before They Are Ha"})
	f.book(junkBookSpec{title: "Before They Are Hanged", path: "/lib/ja/1", author: "Joe Abercrombie", vg: "vg-ja"})
	add("Joe Abercrombie", "vg-ja")

	// A swapped pair: a "Mistborn" row credited with one book in a series
	// "Brandon Sanderson", while Brandon Sanderson has the Mistborn books.
	f.mkSeries("Brandon Sanderson", "Brandon Sanderson")
	f.mkSeries("Mistborn", "Brandon Sanderson")
	f.book(junkBookSpec{title: "Mistborn 02 - The Well of Ascension", path: "/lib/bs/x", author: "Mistborn", series: "Brandon Sanderson"})
	f.book(junkBookSpec{title: "The Final Empire", path: "/lib/bs/1", author: "Brandon Sanderson", series: "Mistborn", vg: "vg-bs"})
	f.book(junkBookSpec{title: "The Hero of Ages", path: "/lib/bs/2", author: "Brandon Sanderson", series: "Mistborn"})
	f.book(junkBookSpec{title: "The Way of Kings", path: "/lib/bs/3", author: "Brandon Sanderson"})
	add("Brandon Sanderson", "vg-bs")

	plan := f.plan()
	for _, c := range cases {
		r := relinkRow(t, f, plan, "read by narrator", c.book)
		require.Empty(t, r.Skipped, "%s: %s", c.target, r.SkipReason)
		require.Equal(t, c.target, r.Proposed["author"], r.Reason)
		require.Equal(t, junkAuthorDecRelink, r.Proposed["decision"], r.Reason)
	}
}

// Apply's backstop asks the same question against the index as it is at
// apply: a series of that name credited to another author since the plan
// refuses the relink before anything is written.
func TestJunkAuthorFixer_ApplyBackstopRefusesAWorkCreditedSincePlan(t *testing.T) {
	f := newJunkFixture(t)
	f.book(junkBookSpec{title: "Three Worlds", path: "/lib/wk/2", author: "Wraith Knight", vg: "vg-wk"})
	id := f.book(junkBookSpec{title: "read by narrator", path: "/lib/j/wk", author: "read by narrator", vg: "vg-wk"})
	plan := f.plan()
	row := relinkRow(t, f, plan, "read by narrator", id)
	require.Equal(t, "Wraith Knight", row.Proposed["author"], row.Reason)
	fresh, err := f.fixer.Replan(context.Background(), nil, *row, &fakeReporter{})
	require.NoError(t, err)
	before := f.credits(id)

	f.mkSeries("Wraith Knight", "C. T. Phipps")
	f.book(junkBookSpec{title: "The Wraith Knight", path: "/lib/ctp/wk1", author: "C. T. Phipps", series: "Wraith Knight"})
	f.book(junkBookSpec{title: "The Wraith Lord", path: "/lib/ctp/wk2", author: "C. T. Phipps", series: "Wraith Knight"})
	f.book(junkBookSpec{title: "Cthulhu Armageddon", path: "/lib/ctp/ca", author: "C. T. Phipps"})
	f.fixer.idxMu.Lock()
	f.fixer.idx = nil
	f.fixer.idxMu.Unlock()

	w := repairs.NewWriter(f.s, f.s, f.fixer.ID(), "bulk_update", "repairs-").WithJournal(f.s, f.s, junkTestOpID).WithCredits(f.s)
	err = f.fixer.Apply(context.Background(), w, fresh)
	require.Error(t, err)
	require.True(t, errors.Is(err, repairs.ErrChangedSincePlan), err)
	require.Contains(t, err.Error(), `credited to "C. T. Phipps"`)
	require.Equal(t, before, f.credits(id), "nothing written")
}

// "parse author" (a parser's placeholder that became an author row) is never
// a target.
func TestJunkAuthorFixer_ParserPlaceholderIsNeverATarget(t *testing.T) {
	f := newJunkFixture(t)
	f.mkSeries("Night of the Hunter", "parse author")
	f.book(junkBookSpec{title: "read by narrator", path: "/lib/pa/1", author: "parse author", series: "Night of the Hunter"})
	id := f.book(junkBookSpec{title: "1-02 Track 02", path: "/lib/j/pa", author: "Unknown Author", series: "Night of the Hunter"})
	for _, r := range f.plan().Rows {
		require.NotEqual(t, "parse author", r.Proposed["author"], "%s: %s", r.RowID, r.Reason)
	}
	_ = id
}

// "Cathfach (A (Not So) Simple Fetch Quest)": a person plus a series of
// theirs in parentheses. The head is the answer (it has books); the whole is
// never a target.
func TestJunkAuthorFixer_PersonWorkCompositeResolvesToTheHead(t *testing.T) {
	const composite = "Cathfach (A (Not So) Simple Fetch Quest)"
	f := newJunkFixture(t)
	f.mkSeries("A (Not So) Simple Fetch Quest", "Cathfach")
	f.book(junkBookSpec{title: "A (Not So) Simple Fetch Quest, Part 1 - Monsters", path: "/lib/cf/1", author: "Cathfach",
		series: "A (Not So) Simple Fetch Quest"})
	f.book(junkBookSpec{title: "Monsters", path: "/lib/cfx/1", author: composite})
	id := f.book(junkBookSpec{title: "Disease", path: "/lib/j/cf", author: "Fetch Quest)", series: "A (Not So) Simple Fetch Quest",
		tags: map[string]string{"artist": composite}})
	r := relinkRow(t, f, f.plan(), "Fetch Quest)", id)
	require.Empty(t, r.Skipped, r.SkipReason)
	require.Equal(t, "Cathfach", r.Proposed["author"], r.Reason)

	idx := f.index()
	code, why := idx.workTarget(composite, f.authors[composite], "Disease", nil)
	require.Equal(t, junkRefuseComposite, code, why)
}

func (f *junkFixture) index() *junkAuthorIndex {
	f.t.Helper()
	idx, err := f.fixer.buildIndex(f.s)
	require.NoError(f.t, err)
	return idx
}

// A mint never carries brackets or an "Author-Title" hyphen join; hyphenated
// given names and surnames still mint.
func TestJunkAuthorIndex_MintableRefusesJoinedAndBracketedNames(t *testing.T) {
	idx := indexFrom(nil, nil, nil, nil, nil)
	for name, want := range map[string]bool{
		"Raymond L. Weil-Star Cross[1-5]": false,
		"Raymond L. Weil-Star Cross":      false,
		"Raymond L. Weil [1-5]":           false,
		"Raymond L. Weil[1-5]":            false,
		"Raymond Weil [Star Cross]":       false,
		"Raymond Weil {Star Cross}":       false,
		"Raymond L. Weil":                 true,
		"Jean-Paul Sartre":                true,
		"Hannah Bonam-Young":              true,
	} {
		require.Equal(t, want, idx.mintable(name), name)
	}
}

// A reader filed as an author is no evidence: David Tennant, credited with a
// book in a series "Cressida Cowell", reads her books. Without the reader
// test his one credit plus her own series outweighs her (no other work).
func TestJunkAuthorIndex_ReaderCreditIsNoWorkEvidence(t *testing.T) {
	tennant, cowell, s := 1, 2, 10
	narr := "David Tennant"
	books := []database.BookCore{
		{ID: "b1", Title: "How to Speak Dragonese", AuthorID: &tennant, SeriesID: &s},
		{ID: "b2", Title: "How to Be a Pirate", AuthorID: &cowell, SeriesID: &s, Narrator: &narr},
		{ID: "b3", Title: "How to Cheat a Dragon's Curse", AuthorID: &cowell, SeriesID: &s, Narrator: &narr},
	}
	idx := indexFrom([]database.Author{{ID: tennant, Name: "David Tennant"}, {ID: cowell, Name: "Cressida Cowell"}},
		books, []database.Series{{ID: s, Name: "Cressida Cowell"}}, nil, nil)
	require.True(t, idx.isReader(tennant))
	require.Empty(t, idx.workCreditedElsewhere("Cressida Cowell", cowell))
}

// An author reading their own books is not a reader: Neil Gaiman's
// self-read "American Gods" still makes an "American Gods" row a work.
func TestJunkAuthorFixer_SelfNarratingAuthorIsStillEvidence(t *testing.T) {
	f := newJunkFixture(t)
	for _, ti := range []string{"American Gods", "Anansi Boys", "Neverwhere", "Coraline"} {
		f.book(junkBookSpec{title: ti, path: "/lib/ng/" + ti, author: "Neil Gaiman", narrator: "Neil Gaiman"})
	}
	f.book(junkBookSpec{title: "Stardust", path: "/lib/ng/sd", author: "Neil Gaiman"})
	f.book(junkBookSpec{title: "American Gods 10th Anniversary", path: "/lib/ag/1", author: "American Gods", vg: "vg-ag"})
	f.book(junkBookSpec{title: "Norse Mythology", path: "/lib/ag/2", author: "American Gods"})
	id := f.book(junkBookSpec{title: "American Gods 10th Anniversary (copy)", path: "/lib/j/ag", author: "read by narrator", vg: "vg-ag"})
	r := relinkRow(t, f, f.plan(), "read by narrator", id)
	require.NotEqual(t, "American Gods", r.Proposed["author"], r.Reason)
	require.Contains(t, r.SkipReason, `credited to "Neil Gaiman"`)
}

// One stray credit never outweighs a body of work: a biography titled
// "Agatha Christie" does not make Agatha Christie a work.
func TestJunkAuthorFixer_BiographyTitleDoesNotRefuseItsSubject(t *testing.T) {
	f := newJunkFixture(t)
	for _, ti := range []string{"Murder on the Orient Express", "Death on the Nile", "The ABC Murders"} {
		f.book(junkBookSpec{title: ti, path: "/lib/ac/" + ti, author: "Agatha Christie"})
	}
	f.book(junkBookSpec{title: "Agatha Christie", path: "/lib/lt/1", author: "Laura Thompson"})
	f.book(junkBookSpec{title: "The Six Wives of Henry VIII", path: "/lib/lt/2", author: "Laura Thompson"})
	id := f.book(junkBookSpec{title: "Evil Under the Sun", path: "/lib/j/eus", author: "read by narrator",
		tags: map[string]string{"artist": "Agatha Christie"}})
	r := relinkRow(t, f, f.plan(), "read by narrator", id)
	require.Empty(t, r.Skipped, r.SkipReason)
	require.Equal(t, "Agatha Christie", r.Proposed["author"], r.Reason)
	require.Equal(t, junkAuthorSrcTags, r.Proposed["source"])
}

// A swapped pair where neither side has work outside it: held, never the
// work row ("Cthulhu Armageddon" credited with three books in a series "C.
// T. Phipps", C. T. Phipps with two in series "Cthulhu Armageddon").
func TestJunkAuthorFixer_SwappedPairWithNoOutsideWorkIsHeld(t *testing.T) {
	f := newJunkFixture(t)
	f.mkSeries("Cthulhu Armageddon", "C. T. Phipps")
	f.mkSeries("C. T. Phipps", "Cthulhu Armageddon")
	for i := range 2 {
		f.book(junkBookSpec{title: fmt.Sprintf("CA Book %d", i), path: fmt.Sprintf("/lib/ctp/%d", i), author: "C. T. Phipps", series: "Cthulhu Armageddon"})
	}
	for i := range 3 {
		f.book(junkBookSpec{title: fmt.Sprintf("Swapped %d", i), path: fmt.Sprintf("/lib/sw/%d", i), author: "Cthulhu Armageddon",
			series: "C. T. Phipps", vg: fmt.Sprintf("vg-%d", i)})
	}
	id := f.book(junkBookSpec{title: "Swapped 0 (copy)", path: "/lib/j/sw", author: "read by narrator", vg: "vg-0"})
	r := relinkRow(t, f, f.plan(), "read by narrator", id)
	require.NotEqual(t, "Cthulhu Armageddon", r.Proposed["author"], r.Reason)
	require.Equal(t, junkAuthorSkipAmbiguous, r.Skipped, "held: %v %s", r.Proposed, r.Reason)
	require.Contains(t, r.SkipReason, `credited to "C. T. Phipps"`)
}

// A better source's answer refused as a work credited elsewhere holds the
// row: a sibling naming someone else does not decide it instead.
func TestJunkAuthorFixer_RefusedWorkCreditHoldsAgainstLowerSources(t *testing.T) {
	f := newJunkFixture(t)
	f.mkSeries("Wraith Knight", "C. T. Phipps")
	for _, ti := range []string{"The Wraith Knight", "The Wraith Lord"} {
		f.book(junkBookSpec{title: ti, path: "/lib/ctp/" + ti, author: "C. T. Phipps", series: "Wraith Knight"})
	}
	f.book(junkBookSpec{title: "Three Worlds", path: "/lib/wk/1", author: "Wraith Knight"})
	f.book(junkBookSpec{title: "Some Other Book", path: "/lib/mix/sib.m4b", author: "Ann Other"})
	id := f.book(junkBookSpec{title: "read by narrator", path: "/lib/mix/j.m4b", author: "read by narrator",
		tags: map[string]string{"artist": "Wraith Knight"}})
	r := relinkRow(t, f, f.plan(), "read by narrator", id)
	require.NotEqual(t, "Wraith Knight", r.Proposed["author"], r.Reason)
	require.NotEqual(t, "Ann Other", r.Proposed["author"], r.Reason)
	require.Equal(t, junkAuthorSkipAmbiguous, r.Skipped, "held: %v %s", r.Proposed, r.Reason)
	require.Contains(t, r.SkipReason, "refused as a work credited to another author")
}

// Not someone else: a strong junk row, a row spelled the same, a row
// carrying the name whole. Someone else: a real author with the series.
func TestJunkAuthorIndex_WorkCreditedElsewhereExclusions(t *testing.T) {
	gs, rbn, dup, jr, ctp, stray, s := 1, 2, 3, 4, 5, 6, 10
	// Glynn Stewart's only book is in the series of his name (no independent
	// work), so each excluded credit alone would outweigh him.
	idx := indexFrom(
		[]database.Author{{ID: gs, Name: "Glynn Stewart"}, {ID: rbn, Name: "read by narrator"},
			{ID: dup, Name: "glynn stewart"}, {ID: jr, Name: "Glynn Stewart Jr"}, {ID: ctp, Name: "C. T. Phipps"},
			{ID: stray, Name: "Stray Row"}},
		[]database.BookCore{
			{ID: "b1", Title: "Starship's Mage", AuthorID: &rbn, SeriesID: &s},
			{ID: "b2", Title: "Mercury Rising", AuthorID: &gs, SeriesID: &s},
			// title-only, and the row has no other work: a folder parse.
			{ID: "b6", Title: "Glynn Stewart", AuthorID: &stray},
			{ID: "b3", Title: "Sword of Mars", AuthorID: &dup, SeriesID: &s},
			{ID: "b4", Title: "Duchy of Terra", AuthorID: &jr, SeriesID: &s},
			{ID: "b5", Title: "Other Work", AuthorID: &jr},
		},
		[]database.Series{{ID: s, Name: "Glynn Stewart"}}, nil, nil)
	require.Empty(t, idx.workCreditedElsewhere("Glynn Stewart", gs))

	// Proportional: one C. T. Phipps book filed in a series "Glynn Stewart"
	// does not outweigh Glynn Stewart's own work; two works do outweigh one.
	books := []database.BookCore{
		{ID: "b1", Title: "Cthulhu Armageddon", AuthorID: &ctp, SeriesID: &s},
		{ID: "b2", Title: "Mercury Rising", AuthorID: &gs},
		{ID: "b3", Title: "The Tower of Zhaal", AuthorID: &ctp},
	}
	authors := []database.Author{{ID: gs, Name: "Glynn Stewart"}, {ID: ctp, Name: "C. T. Phipps"}}
	series := []database.Series{{ID: s, Name: "Glynn Stewart"}}
	require.Empty(t, indexFrom(authors, books, series, nil, nil).workCreditedElsewhere("Glynn Stewart", gs))
	books = append(books, database.BookCore{ID: "b4", Title: "The Tree of Azathoth", AuthorID: &ctp, SeriesID: &s})
	require.Equal(t, "C. T. Phipps", indexFrom(authors, books, series, nil, nil).workCreditedElsewhere("Glynn Stewart", gs))
}

// A junk row's "body of work" does not count file labels ("Disc 01") or the
// other author's own works ("Wraith Knight Three Worlds" is C. T. Phipps's
// "Wraith Knight"): neither outweighs one real credit.
func TestJunkAuthorIndex_IndependentWorksSkipLabelsAndTheOthersWorks(t *testing.T) {
	lk, tp, s := 1, 2, 10
	books := []database.BookCore{{ID: "t1", Title: "Lady Knight", AuthorID: &tp, SeriesID: &s}, {ID: "t2", Title: "Alanna", AuthorID: &tp}}
	for i := range 3 {
		books = append(books, database.BookCore{ID: fmt.Sprintf("d%d", i), Title: fmt.Sprintf("Disc 0%d", i+1), AuthorID: &lk})
	}
	idx := indexFrom([]database.Author{{ID: lk, Name: "Lady Knight"}, {ID: tp, Name: "Tamora Pierce"}},
		books, []database.Series{{ID: s, Name: "Lady Knight"}}, nil, nil)
	require.Equal(t, "Tamora Pierce", idx.workCreditedElsewhere("Lady Knight", lk))

	ca, ctp, wk := 3, 4, 11
	books = []database.BookCore{
		{ID: "c1", Title: "The Tower of Zhaal", AuthorID: &ctp, SeriesID: &s},
		{ID: "c2", Title: "The Wraith Knight", AuthorID: &ctp, SeriesID: &wk},
	}
	for i := range 3 {
		books = append(books, database.BookCore{ID: fmt.Sprintf("w%d", i), Title: fmt.Sprintf("Wraith Knight Three Worlds, Book %d", i), AuthorID: &ca})
	}
	idx = indexFrom([]database.Author{{ID: ca, Name: "Cthulhu Armageddon"}, {ID: ctp, Name: "C. T. Phipps"}},
		books, []database.Series{{ID: s, Name: "Cthulhu Armageddon"}, {ID: wk, Name: "Wraith Knight"}}, nil, nil)
	require.Equal(t, "C. T. Phipps", idx.workCreditedElsewhere("Cthulhu Armageddon", ca))
}

// A series named exactly for a row and holding the row's books is either the
// author-named swap artifact (the prod "Brent Weeks" shape) or a work row
// filed in its own series ("Wraith Knight"). File tags cannot tell them apart
// (the scanner makes a junk row FROM its artist tag); only a stored provider
// match naming the row corroborates, and a tag naming a different real author
// vetoes.
func weeksFixture(t *testing.T, tags map[string]string, provider bool, others ...string) (*junkFixture, string) {
	f := newJunkFixture(t)
	f.mkSeries("Brent Weeks", "Brent Weeks")
	for _, ti := range []string{"The Way of Shadows", "Shadow's Edge", "Beyond the Shadows", "The Black Prism"} {
		bid := f.book(junkBookSpec{title: ti, path: "/lib/bw/" + ti, author: "Brent Weeks", series: "Brent Weeks", tags: tags})
		if provider {
			f.setProvider(bid, ti, "Brent Weeks")
		}
	}
	for i, o := range others {
		f.book(junkBookSpec{title: "Anthology " + o, path: fmt.Sprintf("/lib/o/%d", i), author: o, series: "Brent Weeks"})
		f.book(junkBookSpec{title: "Own Book " + o, path: fmt.Sprintf("/lib/o/x%d", i), author: o})
	}
	id := f.book(junkBookSpec{title: "The Blinding Knife", path: "/lib/j/bk", author: "read by narrator",
		tags: map[string]string{"artist": "Brent Weeks"}})
	return f, id
}

// Brent Weeks with every book in series "Brent Weeks" and an agreeing
// provider match naming him relinks, with one other author misfiled in the
// series (W1) or two (E).
func TestJunkAuthorFixer_ProviderCorroboratedAuthorNamedSeriesStaysTheAuthors(t *testing.T) {
	for _, others := range [][]string{{"Peter V. Brett"}, {"Peter V. Brett", "Shawn Speakman"}} {
		t.Run(fmt.Sprintf("%d others", len(others)), func(t *testing.T) {
			f, id := weeksFixture(t, map[string]string{"artist": "Brent Weeks"}, true, others...)
			require.True(t, f.index().seriesCorroborated[f.author("Brent Weeks")])
			r := relinkRow(t, f, f.plan(), "read by narrator", id)
			require.Empty(t, r.Skipped, r.SkipReason)
			require.Equal(t, "Brent Weeks", r.Proposed["author"], r.Reason)
		})
	}
}

// The deliberate trade-off: without a provider match the same library is
// structurally a work row filed in its own series, and a tag naming the row
// is circular, so the row is held -- tagged (W1) or not (W2).
func TestJunkAuthorFixer_TagsAloneNeverCorroborateAnAuthorNamedSeries(t *testing.T) {
	for _, tags := range []map[string]string{{"artist": "Brent Weeks"}, nil} {
		t.Run(fmt.Sprintf("tags=%v", tags), func(t *testing.T) {
			f, id := weeksFixture(t, tags, false, "Peter V. Brett")
			require.False(t, f.index().seriesCorroborated[f.author("Brent Weeks")])
			r := relinkRow(t, f, f.plan(), "read by narrator", id)
			require.NotEqual(t, "Brent Weeks", r.Proposed["author"], r.Reason)
			require.Equal(t, junkAuthorSkipAmbiguous, r.Skipped, "held: %v %s", r.Proposed, r.Reason)
		})
	}
}

// A work row filed in its own series is held in every shape: untagged (A,
// C), tagged with the series name beside the real author (C2, even with a
// provider match naming the row: the real author's tag vetoes), tagged with
// the series name alone (C3: the row was made from that tag), or with a
// provider match naming the real author, alone or joined to the row's name.
func TestJunkAuthorFixer_WorkRowInItsOwnSeriesIsHeld(t *testing.T) {
	for _, tc := range []struct {
		name     string
		titles   []string
		phippsIn int
		tags     map[string]string
		provider string   // fetched author on each work-row book ("" = none)
		outside  []string // the row's titles outside the series
	}{
		{"A: three titles, Phipps has one in the series", []string{"Wraith Lord", "Wraith Queen", "Wraith Emperor"}, 1, nil, "", nil},
		{"C: two titles, Phipps has none in the series", []string{"Wraith Lord", "Wraith Queen"}, 0, nil, "", nil},
		{"C2: album_artist is the series, artist is Phipps", []string{"Wraith Lord", "Wraith Queen"}, 0,
			map[string]string{"album_artist": "Wraith Knight", "artist": "C. T. Phipps"}, "", nil},
		{"C2 with a provider match naming the row", []string{"Wraith Lord", "Wraith Queen"}, 0,
			map[string]string{"album_artist": "Wraith Knight", "artist": "C. T. Phipps"}, "Wraith Knight", nil},
		{"artist is Phipps, provider names the row", []string{"Wraith Lord", "Wraith Queen"}, 0,
			map[string]string{"artist": "C. T. Phipps"}, "Wraith Knight", nil},
		{"C3: artist is the series (the row was made from it)", []string{"Wraith Lord", "Wraith Queen"}, 0,
			map[string]string{"artist": "Wraith Knight"}, "", nil},
		{"provider names Phipps", []string{"Wraith Lord", "Wraith Queen"}, 0, nil, "C. T. Phipps", nil},
		{"provider names Phipps joined to the row's name", []string{"Wraith Lord", "Wraith Queen"}, 0, nil, "C. T. Phipps & Wraith Knight", nil},
		// One title outside the series is other work (hasOtherWork), so only
		// the uncorroborated series books keep the row from outweighing
		// Phipps's two: they must not count as its own.
		{"three titles plus one outside, Phipps has two in the series", []string{"Wraith Lord", "Wraith Queen", "Wraith Emperor"}, 2, nil, "", []string{"Night Watch"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newJunkFixture(t)
			f.mkSeries("Wraith Knight", "C. T. Phipps")
			for i := range tc.phippsIn {
				f.book(junkBookSpec{title: fmt.Sprintf("Phipps WK %d", i), path: fmt.Sprintf("/lib/ctp/wk%d", i), author: "C. T. Phipps", series: "Wraith Knight"})
			}
			f.book(junkBookSpec{title: "The Rules of Supervillainy", path: "/lib/ctp/rs", author: "C. T. Phipps"})
			for i, ti := range tc.titles {
				bid := f.book(junkBookSpec{title: ti, path: fmt.Sprintf("/lib/w/%d", i), author: "Wraith Knight", series: "Wraith Knight",
					vg: fmt.Sprintf("vgw-%d", i), tags: tc.tags})
				if tc.provider != "" {
					f.setProvider(bid, ti, tc.provider)
				}
			}
			for i, ti := range tc.outside {
				f.book(junkBookSpec{title: ti, path: fmt.Sprintf("/lib/w/out%d", i), author: "Wraith Knight"})
			}
			id := f.book(junkBookSpec{title: tc.titles[0] + " (copy)", path: "/lib/j/w", author: "read by narrator", vg: "vgw-0"})
			require.False(t, f.index().seriesCorroborated[f.author("Wraith Knight")])
			r := relinkRow(t, f, f.plan(), "read by narrator", id)
			// Held: skipped, or the junk credit left for a hand decision.
			// Never relinked to the work row.
			require.NotEqual(t, "Wraith Knight", r.Proposed["author"], r.Reason)
			require.NotEqual(t, "relink", r.Proposed["decision"], "held: %v %s", r.Proposed, r.Reason)
		})
	}
}

// An anthology series whose files name its editor: a row of its name is the
// anthology's title, held.
func TestJunkAuthorFixer_AnthologySeriesRowIsHeld(t *testing.T) {
	f := newJunkFixture(t)
	f.mkSeries("Dangerous Visions", "Dangerous Visions")
	for _, ti := range []string{"Introduction One", "Introduction Two", "Afterword Three"} {
		f.book(junkBookSpec{title: ti, path: "/lib/dv/" + ti, author: "Dangerous Visions", series: "Dangerous Visions",
			tags: map[string]string{"artist": "Harlan Ellison"}})
	}
	f.book(junkBookSpec{title: "Aye and Gomorrah", path: "/lib/o/1", author: "Samuel R. Delany", series: "Dangerous Visions"})
	f.book(junkBookSpec{title: "Dhalgren", path: "/lib/o/2", author: "Samuel R. Delany"})
	f.book(junkBookSpec{title: "Riders of the Purple Wage", path: "/lib/o/3", author: "Philip Jose Farmer", series: "Dangerous Visions"})
	f.book(junkBookSpec{title: "To Your Scattered Bodies Go", path: "/lib/o/4", author: "Philip Jose Farmer"})
	id := f.book(junkBookSpec{title: "The Jigsaw Man", path: "/lib/j/jm", author: "read by narrator",
		tags: map[string]string{"artist": "Dangerous Visions"}})
	r := relinkRow(t, f, f.plan(), "read by narrator", id)
	require.NotEqual(t, "Dangerous Visions", r.Proposed["author"], r.Reason)
	require.NotEqual(t, "relink", r.Proposed["decision"], "held: %v %s", r.Proposed, r.Reason)
}

// corroborationReader is a junkAuthorCorroborationReader over fixed data.
type corroborationReader struct {
	files  map[string][]database.BookFile
	states map[string][]database.MetadataFieldState
	err    error
}

func (c corroborationReader) GetBookFiles(id string) ([]database.BookFile, error) {
	return c.files[id], c.err
}

func (c corroborationReader) GetMetadataFieldStates(id string) ([]database.MetadataFieldState, error) {
	return c.states[id], nil
}

func providerStates(title, author string) []database.MetadataFieldState {
	enc := func(v string) *string { return new(`"` + v + `"`) }
	return []database.MetadataFieldState{{Field: "title", FetchedValue: enc(title)}, {Field: "author_name", FetchedValue: enc(author)}}
}

// Corroboration reads only candidate rows (a book in the series of the row's
// own name); only an agreeing provider match naming the row counts; a tag
// naming another real author vetoes; a joint credit counts only when no other
// name in it is a real author row; a read error fails closed.
func TestJunkAuthorIndex_CorroborateNamedSeries(t *testing.T) {
	books := []database.BookCore{
		{ID: "bw1", Title: "The Way of Shadows", AuthorID: new(1), SeriesID: new(7)},
		{ID: "wk1", Title: "Wraith Lord", AuthorID: new(2), SeriesID: new(8)},
		{ID: "ctp", Title: "The Rules of Supervillainy", AuthorID: new(3)},
		{ID: "gs1", Title: "Mercury Rising", AuthorID: new(4)},
	}
	idx := indexFrom([]database.Author{{ID: 1, Name: "Brent Weeks"}, {ID: 2, Name: "Wraith Knight"}, {ID: 3, Name: "C. T. Phipps"}, {ID: 4, Name: "Glynn Stewart"}},
		books, []database.Series{{ID: 7, Name: "Brent Weeks"}, {ID: 8, Name: "Wraith Knight"}}, nil, nil)
	tags := func(m map[string]map[string]string) map[string][]database.BookFile {
		out := map[string][]database.BookFile{}
		for id, t := range m {
			out[id] = []database.BookFile{{RawTags: t}}
		}
		return out
	}
	for _, tc := range []struct {
		name string
		r    corroborationReader
		want map[int]bool
	}{
		{"tags naming the rows never corroborate", corroborationReader{files: tags(map[string]map[string]string{
			"bw1": {"artist": "Brent Weeks"}, "wk1": {"artist": "Wraith Knight"}, "gs1": {"artist": "Glynn Stewart"},
		})}, map[int]bool{}},
		{"an agreeing provider match does", corroborationReader{states: map[string][]database.MetadataFieldState{
			"bw1": providerStates("The Way of Shadows", "Brent Weeks"),
			"gs1": providerStates("Mercury Rising", "Glynn Stewart"),
		}}, map[int]bool{1: true}},
		{"a provider match for another title does not", corroborationReader{states: map[string][]database.MetadataFieldState{
			"bw1": providerStates("A Different Book Entirely", "Brent Weeks"),
		}}, map[int]bool{}},
		{"a joint credit with no other real author does", corroborationReader{states: map[string][]database.MetadataFieldState{
			"bw1": providerStates("The Way of Shadows", "Brent Weeks & Someone Unknown"),
		}}, map[int]bool{1: true}},
		{"a joint credit naming a real author does not", corroborationReader{states: map[string][]database.MetadataFieldState{
			"wk1": providerStates("Wraith Lord", "C. T. Phipps & Wraith Knight"),
		}}, map[int]bool{}},
		{"a tag naming another real author vetoes the provider", corroborationReader{
			files: tags(map[string]map[string]string{"wk1": {"album_artist": "Wraith Knight", "artist": "C. T. Phipps"}}),
			states: map[string][]database.MetadataFieldState{
				"wk1": providerStates("Wraith Lord", "Wraith Knight"),
			}}, map[int]bool{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, idx.corroborateNamedSeries(tc.r))
			require.Equal(t, tc.want, idx.seriesCorroborated)
		})
	}
	require.Error(t, idx.corroborateNamedSeries(corroborationReader{err: errors.New("disk gone")}))
}

// A provider match naming Brent Weeks corroborates his series whatever else
// the files carry beside him: an album artist naming a narrator's author row
// (W4), a co-credit after his name in the artist tag (W5), or a provider
// credit that lists him first with the narrator (W6).
func TestJunkAuthorFixer_ProviderCorroborationSurvivesCoCredits(t *testing.T) {
	titles := []string{"The Way of Shadows", "Shadow's Edge", "Beyond the Shadows", "The Black Prism"}
	for _, tc := range []struct {
		name     string
		tags     map[string]string
		provider string
	}{
		{"W4: album_artist names a narrator's author row", map[string]string{"album_artist": "Simon Vance", "artist": "Brent Weeks"}, "Brent Weeks"},
		{"W5: artist credits him first, then the narrator", map[string]string{"artist": "Brent Weeks, Simon Vance"}, "Brent Weeks"},
		{"W6: provider credits him first, then the narrator", nil, "Brent Weeks & Simon Vance"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newJunkFixture(t)
			f.mkSeries("Brent Weeks", "Brent Weeks")
			f.book(junkBookSpec{title: "Voice Work", path: "/lib/pd/1", author: "Simon Vance"})
			for _, ti := range titles {
				bid := f.book(junkBookSpec{title: ti, path: "/lib/bw/" + ti, author: "Brent Weeks", series: "Brent Weeks", tags: tc.tags})
				f.setProvider(bid, ti, tc.provider)
			}
			f.book(junkBookSpec{title: "Anthology", path: "/lib/o/1", author: "Peter V. Brett", series: "Brent Weeks"})
			f.book(junkBookSpec{title: "The Warded Man", path: "/lib/o/2", author: "Peter V. Brett"})
			id := f.book(junkBookSpec{title: "The Blinding Knife", path: "/lib/j/bk", author: "read by narrator",
				tags: map[string]string{"artist": "Brent Weeks"}})
			require.True(t, f.index().seriesCorroborated[f.author("Brent Weeks")])
			r := relinkRow(t, f, f.plan(), "read by narrator", id)
			require.Empty(t, r.Skipped, r.SkipReason)
			require.Equal(t, "Brent Weeks", r.Proposed["author"], r.Reason)
		})
	}
}

// The veto needs the same other author on most sampled files' artist tags;
// a reader neither vetoes nor leads a joint credit.
func TestJunkAuthorIndex_CorroborationVetoIsByMajorityAndIgnoresReaders(t *testing.T) {
	books := []database.BookCore{
		{ID: "bw1", Title: "The Way of Shadows", AuthorID: new(1), SeriesID: new(7)},
		{ID: "bw2", Title: "Shadow's Edge", AuthorID: new(1), SeriesID: new(7)},
		{ID: "ctp", Title: "The Rules of Supervillainy", AuthorID: new(3)},
		{ID: "sv", Title: "Voice Work", AuthorID: new(5)},
		{ID: "gs1", Title: "Mercury Rising", AuthorID: new(4), Narrator: new("Simon Vance")},
		{ID: "gs2", Title: "Starship's Mage", AuthorID: new(4), Narrator: new("Simon Vance")},
	}
	idx := indexFrom([]database.Author{{ID: 1, Name: "Brent Weeks"}, {ID: 3, Name: "C. T. Phipps"}, {ID: 4, Name: "Glynn Stewart"}, {ID: 5, Name: "Simon Vance"}},
		books, []database.Series{{ID: 7, Name: "Brent Weeks"}}, nil, nil)
	require.True(t, idx.isReader(5), "fixture: Simon Vance reads more than he writes")
	provider := map[string][]database.MetadataFieldState{
		"bw1": providerStates("The Way of Shadows", "Brent Weeks"),
		"bw2": providerStates("Shadow's Edge", "Brent Weeks"),
	}
	artist := func(a, b string) map[string][]database.BookFile {
		return map[string][]database.BookFile{
			"bw1": {{RawTags: map[string]string{"artist": a}}},
			"bw2": {{RawTags: map[string]string{"artist": b}}},
		}
	}
	for _, tc := range []struct {
		name string
		r    corroborationReader
		want bool
	}{
		{"one of two files names Phipps: no majority", corroborationReader{files: artist("C. T. Phipps", "Brent Weeks"), states: provider}, true},
		{"both files name Phipps: vetoed", corroborationReader{files: artist("C. T. Phipps", "C. T. Phipps"), states: provider}, false},
		{"album artist naming Phipps never vetoes", corroborationReader{files: map[string][]database.BookFile{
			"bw1": {{RawTags: map[string]string{"album_artist": "C. T. Phipps"}}},
			"bw2": {{RawTags: map[string]string{"album_artist": "C. T. Phipps"}}},
		}, states: provider}, true},
		{"a reader on every file never vetoes", corroborationReader{files: artist("Simon Vance", "Simon Vance"), states: provider}, true},
		{"a reader listed first does not lead the provider credit", corroborationReader{states: map[string][]database.MetadataFieldState{
			"bw1": providerStates("The Way of Shadows", "Simon Vance & Brent Weeks"),
		}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, idx.corroborateNamedSeries(tc.r))
			require.Equal(t, tc.want, idx.seriesCorroborated[1])
		})
	}
}

// One shared word with another author's title does not make a title theirs:
// Laura Thompson's "Murder" takes none of Agatha Christie's "Murder ..."
// titles, so the one "Agatha Christie" biography still does not outweigh her.
func TestJunkAuthorFixer_OneSharedWordIsNotTheOtherAuthorsWork(t *testing.T) {
	f := newJunkFixture(t)
	for _, ti := range []string{"Murder on the Orient Express", "Murder at the Vicarage", "The Murder of Roger Ackroyd"} {
		f.book(junkBookSpec{title: ti, path: "/lib/ac/" + ti, author: "Agatha Christie"})
	}
	f.book(junkBookSpec{title: "Agatha Christie", path: "/lib/lt/1", author: "Laura Thompson"})
	f.book(junkBookSpec{title: "Murder", path: "/lib/lt/2", author: "Laura Thompson"})
	id := f.book(junkBookSpec{title: "Evil Under the Sun", path: "/lib/j/eus", author: "read by narrator",
		tags: map[string]string{"artist": "Agatha Christie"}})
	r := relinkRow(t, f, f.plan(), "read by narrator", id)
	require.Empty(t, r.Skipped, r.SkipReason)
	require.Equal(t, "Agatha Christie", r.Proposed["author"], r.Reason)
}

// A row named for another author's series, with none of its own books in it,
// is a work named after that series even when it holds more series-less
// titles than the series has works: C. T. Phipps's two-book "Cthulhu
// Armageddon" against a "Cthulhu Armageddon" row with three titles. The book
// titled after its own series counts once as a title AND once in the series.
func TestJunkAuthorFixer_RowNamedForAnotherAuthorsSeriesIsHeld(t *testing.T) {
	f := newJunkFixture(t)
	f.mkSeries("Cthulhu Armageddon", "C. T. Phipps")
	for _, ti := range []string{"Cthulhu Armageddon", "The Tower of Zhaal"} {
		f.book(junkBookSpec{title: ti, path: "/lib/ctp/" + ti, author: "C. T. Phipps", series: "Cthulhu Armageddon"})
	}
	for i, ti := range []string{"Wrath of the Old Ones", "Fallen Gods Hunger", "Sea of Madness"} {
		f.book(junkBookSpec{title: ti, path: "/lib/w/" + ti, author: "Cthulhu Armageddon", vg: fmt.Sprintf("vgw-%d", i)})
	}
	id := f.book(junkBookSpec{title: "Wrath of the Old Ones (copy)", path: "/lib/j/w", author: "read by narrator", vg: "vgw-0"})

	idx := f.index()
	ctp := f.author("C. T. Phipps")
	require.Equal(t, 2, idx.seriesWorkCredits["cthulhu armageddon"][ctp], "title-named-for-its-series book counts in the series too")

	r := relinkRow(t, f, f.plan(), "read by narrator", id)
	require.NotEqual(t, "Cthulhu Armageddon", r.Proposed["author"], r.Reason)
	require.Equal(t, junkAuthorSkipAmbiguous, r.Skipped, "held: %v %s", r.Proposed, r.Reason)
	require.Contains(t, r.SkipReason, `credited to "C. T. Phipps"`)
}

// One misfiled book is not a series: C. T. Phipps's single book in a series
// "Glynn Stewart" (none of Glynn Stewart's own books in it) does not hold
// Glynn Stewart.
func TestJunkAuthorFixer_OneMisfiledBookIsNotAnotherAuthorsSeries(t *testing.T) {
	f := newJunkFixture(t)
	f.mkSeries("Glynn Stewart", "Glynn Stewart")
	f.book(junkBookSpec{title: "Mercury Rising", path: "/lib/gs/1", author: "Glynn Stewart"})
	f.book(junkBookSpec{title: "Cthulhu Armageddon", path: "/lib/ctp/1", author: "C. T. Phipps", series: "Glynn Stewart"})
	f.book(junkBookSpec{title: "The Rules of Supervillainy", path: "/lib/ctp/2", author: "C. T. Phipps"})
	id := f.book(junkBookSpec{title: "Starship's Mage", path: "/lib/j/sm", author: "read by narrator",
		tags: map[string]string{"artist": "Glynn Stewart"}})
	r := relinkRow(t, f, f.plan(), "read by narrator", id)
	require.Empty(t, r.Skipped, r.SkipReason)
	require.Equal(t, "Glynn Stewart", r.Proposed["author"], r.Reason)
}
