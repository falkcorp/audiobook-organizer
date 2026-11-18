// file: internal/plugins/maintenance/series_denumber_review3_test.go
// version: 1.0.1
// guid: 3c8e1f72-5a09-4d46-b7e3-9d12a60c4f85
// last-edited: 2026-11-18

package maintenance

import (
	"sort"
	"strconv"
	"testing"
)

// The wrong moves of the PR #4698 round-2 review (B1, B2(a) to B2(c), M1), run
// through the whole op on the stateful fixture exactly as the review traced
// them: planner, demotion, decision and apply. Each input must leave the library
// as it was. The controls at the end still move, so the tier is not empty.

// libState is every book's series and position, so a test can assert that a run
// changed nothing even when the books carry positions of their own.
func libState(l *denumberLib) map[string]string {
	out := make(map[string]string, len(l.books))
	for id := range l.books {
		out[id] = strconv.Itoa(l.seriesOf(id)) + "@" + l.seqOf(id)
	}
	return out
}

// assertLibUnchanged fails when a run created, deleted, moved, repositioned or
// journaled anything.
func assertLibUnchanged(t *testing.T, l *denumberLib, before map[string]string) {
	t.Helper()
	if len(l.created) != 0 {
		t.Errorf("created base series %+v, want none", l.created)
	}
	if len(l.deleted) != 0 {
		t.Errorf("deleted series %v, want none", l.deleted)
	}
	after := libState(l)
	ids := make([]string, 0, len(before))
	for id := range before {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if after[id] != before[id] {
			t.Errorf("book %s went from series@position %s to %s, want it left where it was", id, before[id], after[id])
		}
	}
	if len(l.changes) != 0 {
		t.Errorf("%d journal rows written for a run that must change nothing", len(l.changes))
	}
}

// debrisTitles are three different Batman titles: the books behind a run of
// numbered series are distinct books, so the title is not what holds a shape
// the grammar already refuses.
var debrisTitles = []string{"A Death in the Family", "A Lonely Place of Dying", "Under the Red Hood"}

// addNumberedRun adds one numbered series per number, each holding one book with
// a distinct title and no position.
func addNumberedRun(l *denumberLib, firstSeriesID int, author int, nameFor func(n int) string, nums ...int) {
	for i, n := range nums {
		sid := firstSeriesID + i
		l.addSeries(sid, nameFor(n), author)
		l.addBookT(bookIDFor(sid, 0), sid, -1, "", debrisTitles[i%len(debrisTitles)])
	}
}

func runLib(t *testing.T, l *denumberLib) map[string]string {
	t.Helper()
	before := libState(l)
	l.mustRun(shapeApplyParams(l, nil))
	return before
}

// B1, own-position leg. "Nightwing Vol. 3" is a comic run: four issues at their
// own positions. The one whose position equals the 3 in the name must not be
// torn out of the run into a new series.
func TestSeriesDenumber_Round3_B1_AMultiBookSeriesIsNotSplitByOwnPosition(t *testing.T) {
	l := newDenumberLib(t)
	l.addSeries(11, "Nightwing Vol. 3", denumberAuthor)
	for i := 1; i <= 4; i++ {
		l.addBookT("n"+strconv.Itoa(i), 11, i, "", "Nightwing Issue "+strconv.Itoa(i))
	}
	assertLibUnchanged(t, l, runLib(t, l))
}

// B1, run leg. "Gotham 2" holds one book at position 1 and one with no position.
// The run Gotham 1, 2, 3 (siblings that hold books) must not carry the book with
// no position into a new "Gotham" series at 2 while its neighbour stays.
func TestSeriesDenumber_Round3_B1_AMultiBookSeriesIsNotSplitByTheRun(t *testing.T) {
	l := newDenumberLib(t)
	l.addSeries(21, "Gotham 1", denumberAuthor)
	l.addBookT("g1", 21, 99, "", "Gotham Gaslight")
	l.addSeries(22, "Gotham 2", denumberAuthor)
	l.addBookT("b1", 22, 1, "", "Gotham by Gaslight")
	l.addBookT("b2", 22, -1, "", "Gotham Central")
	l.addSeries(23, "Gotham 3", denumberAuthor)
	l.addBookT("g3", 23, 99, "", "Gotham Knights")
	assertLibUnchanged(t, l, runLib(t, l))
}

// B2(a): a word number inside the base. Three parts of one book, one book each.
func TestSeriesDenumber_Round3_B2a_AWordNumberInTheBaseIsHeld(t *testing.T) {
	l := newDenumberLib(t)
	addNumberedRun(l, 31, denumberAuthor, func(n int) string { return "Hush Book One " + zero2(n) }, 1, 2, 3)
	assertLibUnchanged(t, l, runLib(t, l))
}

func zero2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

// B2(b): part and format words missing from both word lists. Each is three files
// of one book numbered 01 to 03.
func TestSeriesDenumber_Round3_B2b_PartWordsMissingFromTheListsAreHeld(t *testing.T) {
	for i, word := range []string{"Ch", "Ep", "Episodes", "Discs", "CDs", "Sides", "Reel", "Hour", "Hours", "Disks"} {
		t.Run(word, func(t *testing.T) {
			l := newDenumberLib(t)
			addNumberedRun(l, 100+10*i, denumberAuthor, func(n int) string { return "Hush " + word + " " + zero2(n) }, 1, 2, 3)
			assertLibUnchanged(t, l, runLib(t, l))
		})
	}
}

// B2(c), the decision half. A book that sits in a numbered series at a position
// equal to the number in the name is not evidence: the importer writes the
// position from a "Part N" title (metadata.DetectVolumeNumber) into a numbered
// row that exists, and series-normalize leaves the position it parsed from the
// row's own name after a partial merge. Both writers produce this exact state
// (see TestImportFile_PartTitleWritesItsNumberIntoTheNumberedSeriesRow and
// TestExecuteSeriesNormalize_PartialMergeLeavesTheNameParsedPosition), and the
// name and the position are then one tagger's reading, not two.
func TestSeriesDenumber_Round3_B2c_AnOwnPositionCopiedFromTheNameIsNotEvidence(t *testing.T) {
	t.Run("a lone numbered row, position written from a Part title", func(t *testing.T) {
		l := newDenumberLib(t)
		l.addSeries(41, "Bane 02", denumberAuthor)
		l.addBookT("bane-2", 41, 2, "", "Bane Part 2")
		assertLibUnchanged(t, l, runLib(t, l))
	})
	t.Run("after a partial merge: the keeper was renamed to the base and one book was stranded", func(t *testing.T) {
		l := newDenumberLib(t)
		// The keeper is the contaminated "Bane 03" row series-normalize renamed to
		// "Bane"; the stranded book sits in "Bane 05" at the 5 parsed from its name.
		l.addSeries(51, "Bane", denumberAuthor)
		l.addBookT("bane-3", 51, 3, "", "Bane Part 3")
		l.addSeries(52, "Bane 05", denumberAuthor)
		l.addBookT("bane-5", 52, 5, "", "Bane Part 5")
		assertLibUnchanged(t, l, runLib(t, l))
	})
}

// B2(d), the evidence the run itself must pass. Parts of one shattered book are
// consecutive, same author, same base, one book each: a perfect run by
// construction. They are told apart by their books, which are one book: the same
// title, a title that is the base, a shared ASIN.
func TestSeriesDenumber_Round3_B2d_ARunOfPartsOfOneBookIsHeld(t *testing.T) {
	cases := []struct {
		name   string
		titles [3]string
		asins  [3]string
	}{
		{"one title", [3]string{"Bane", "Bane", "Bane"}, [3]string{}},
		{"the title is the base", [3]string{"Bane", "Knightfall", "Knightquest"}, [3]string{}},
		{"the part number in the title", [3]string{"Bane Part 1", "Bane Part 2", "Bane Part 3"}, [3]string{}},
		{"the base and a number in the title", [3]string{"Bane 01", "Bane 02", "Bane 03"}, [3]string{}},
		{"one ASIN", [3]string{"Knightfall", "Knightquest", "Knights End"}, [3]string{"B00BANE001", "B00BANE001", "B00BANE001"}},
		{"two of three share an ASIN", [3]string{"Knightfall", "Knightquest", "Knights End"}, [3]string{"B00BANE001", "B00BANE002", "B00BANE001"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := newDenumberLib(t)
			for i := 0; i < 3; i++ {
				sid := 60 + i
				l.addSeries(sid, "Bane "+zero2(i+1), denumberAuthor)
				l.addBookT(bookIDFor(sid, 0), sid, -1, "", tc.titles[i])
				if tc.asins[i] != "" {
					a := tc.asins[i]
					l.books[bookIDFor(sid, 0)].ASIN = &a
				}
			}
			assertLibUnchanged(t, l, runLib(t, l))
		})
	}
}

// M1: a base that is a part of the series' own author's name. "Wayne 01" to
// "Wayne 03" under the author Bruce Wayne is the author's name, not a series.
func TestSeriesDenumber_Round3_M1_APartOfTheAuthorsNameIsNotASeries(t *testing.T) {
	cases := []struct {
		name   string
		author string
		base   string
	}{
		{"surname alone", "Bruce Wayne", "Wayne"},
		{"given name alone", "Bruce Wayne", "Bruce"},
		{"the name with a middle initial removed", "Bruce T. Wayne", "Bruce Wayne"},
		{"surname alone, inverted credit", "Wayne, Bruce", "Wayne"},
		{"given name alone, inverted credit", "Wayne, Bruce", "Bruce"},
		{"surname of a composite credit", "Bruce Wayne & Dick Grayson", "Grayson"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := newDenumberLib(t)
			l.authors[0].Name = tc.author
			addNumberedRun(l, 70, denumberAuthor, func(n int) string { return tc.base + " " + zero2(n) }, 1, 2, 3)
			assertLibUnchanged(t, l, runLib(t, l))
		})
	}
}

// Control: three consecutive numbered series, each one book with its own title,
// still fold into the base.
func TestSeriesDenumber_Round3_Control_ADistinctSingleBookRunStillMoves(t *testing.T) {
	l := newDenumberLib(t)
	addNumberedRun(l, 80, denumberAuthor, func(n int) string { return "Court of Owls " + zero2(n) }, 2, 3, 4)
	l.mustRun(shapeApplyParams(l, nil))
	base := l.createdID("Court of Owls")
	if base == 0 {
		t.Fatalf("no base created; events=%v", l.events)
	}
	for i, want := range []string{"2", "3", "4"} {
		id := bookIDFor(80+i, 0)
		if l.seriesOf(id) != base || l.seqOf(id) != want {
			t.Errorf("%s: series %d position %s, want %d position %s", id, l.seriesOf(id), l.seqOf(id), base, want)
		}
	}
}
