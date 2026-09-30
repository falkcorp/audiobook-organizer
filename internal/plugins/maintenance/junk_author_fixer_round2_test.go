// file: internal/plugins/maintenance/junk_author_fixer_round2_test.go
// version: 1.0.0
// guid: 993dd8c9-20d0-4bd4-951b-11471d5dff76
// last-edited: 2026-09-29

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
	f.book(junkBookSpec{title: "Esoterrorism From the Secret File 2", path: "/lib/wk/1", author: "Wraith Knight"})
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

// The credit maps skip a book whose primary author is also its Narrator,
// even when the reader is credited with more books than they narrate.
func TestJunkAuthorIndex_ReaderCreditIsNoWorkEvidence(t *testing.T) {
	tennant, cowell, s := 1, 2, 10
	narr := "David Tennant"
	idx := indexFrom(
		[]database.Author{{ID: tennant, Name: "David Tennant"}, {ID: cowell, Name: "Cressida Cowell"}},
		[]database.BookCore{
			{ID: "b1", Title: "How to Speak Dragonese", AuthorID: &tennant, SeriesID: &s, Narrator: &narr},
			{ID: "b2", Title: "Mis-credited One", AuthorID: &tennant},
			{ID: "b3", Title: "Mis-credited Two", AuthorID: &tennant},
			{ID: "b4", Title: "How to Be a Pirate", AuthorID: &cowell},
		},
		[]database.Series{{ID: s, Name: "Cressida Cowell"}}, nil, nil)
	require.Empty(t, idx.workCreditedElsewhere("Cressida Cowell", cowell))
}

// Not someone else: a strong junk row, a row spelled the same, a row
// carrying the name whole. Someone else: a real author with the series.
func TestJunkAuthorIndex_WorkCreditedElsewhereExclusions(t *testing.T) {
	gs, rbn, dup, jr, ctp, s := 1, 2, 3, 4, 5, 10
	idx := indexFrom(
		[]database.Author{{ID: gs, Name: "Glynn Stewart"}, {ID: rbn, Name: "read by narrator"},
			{ID: dup, Name: "glynn stewart"}, {ID: jr, Name: "Glynn Stewart Jr"}, {ID: ctp, Name: "C. T. Phipps"}},
		[]database.BookCore{
			{ID: "b1", Title: "Starship's Mage", AuthorID: &rbn, SeriesID: &s},
			{ID: "b2", Title: "Mercury Rising", AuthorID: &gs},
			{ID: "b3", Title: "Sword of Mars", AuthorID: &dup, SeriesID: &s},
			{ID: "b4", Title: "Duchy of Terra", AuthorID: &jr, SeriesID: &s},
			{ID: "b5", Title: "Other Work", AuthorID: &jr},
		},
		[]database.Series{{ID: s, Name: "Glynn Stewart"}}, nil, nil)
	require.Empty(t, idx.workCreditedElsewhere("Glynn Stewart", gs))

	idx = indexFrom(
		[]database.Author{{ID: gs, Name: "Glynn Stewart"}, {ID: ctp, Name: "C. T. Phipps"}},
		[]database.BookCore{
			{ID: "b1", Title: "Cthulhu Armageddon", AuthorID: &ctp, SeriesID: &s},
			{ID: "b2", Title: "Mercury Rising", AuthorID: &gs},
		},
		[]database.Series{{ID: s, Name: "Glynn Stewart"}}, nil, nil)
	require.Equal(t, "C. T. Phipps", idx.workCreditedElsewhere("Glynn Stewart", gs))
}
