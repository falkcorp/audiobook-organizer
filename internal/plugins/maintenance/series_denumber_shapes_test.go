// file: internal/plugins/maintenance/series_denumber_shapes_test.go
// version: 1.0.0
// guid: 60a27f27-6670-4e9f-9f7c-b5f9deccf612
// last-edited: 2026-11-17

package maintenance

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// The wrong-move shapes of the PR #4684 round-1 review (B1 to B5), run through
// the whole op on the stateful fixture: planner, demotion, decision and apply.
// Every input is a name the review traced; each must leave the library exactly
// as it was. The controls at the end must still move, so the tier does not
// collapse to nothing.

// seriesSpec is one numbered series of a shape test: its name, the author row
// it belongs to, and how many one-book versions it holds (0 for an empty row).
type seriesSpec struct {
	id     int
	name   string
	author int
	books  int
	// seq is the stored position of every book of the series, -1 for none.
	seq int
}

// shapeApplyParams is an apply with the pre-apply report the op requires.
func shapeApplyParams(t *testing.T, extra map[string]any) string {
	t.Helper()
	p := map[string]any{"apply": true, "reportPath": filepath.Join(t.TempDir(), "report.tsv")}
	for k, v := range extra {
		p[k] = v
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// runShape builds the library, applies, and returns it with the books' original
// series, so a test can assert nothing moved.
func runShape(t *testing.T, authors []string, specs []seriesSpec) (*denumberLib, map[string]int) {
	t.Helper()
	l := newDenumberLib(t)
	for i, name := range authors {
		l.authors = append(l.authors, database.Author{ID: denumberAuthor + 2 + i, Name: name})
	}
	orig := map[string]int{}
	for _, s := range specs {
		l.addSeries(s.id, s.name, s.author)
		for b := 0; b < s.books; b++ {
			id := bookIDFor(s.id, b)
			l.addBook(id, s.id, s.seq, "")
			orig[id] = s.id
		}
	}
	l.mustRun(shapeApplyParams(t, nil))
	return l, orig
}

func bookIDFor(seriesID, n int) string {
	return "book-" + strconv.Itoa(seriesID) + "-" + strconv.Itoa(n)
}

// assertUntouched fails when anything was created, deleted, moved or journaled.
func assertUntouched(t *testing.T, l *denumberLib, orig map[string]int) {
	t.Helper()
	if len(l.created) != 0 {
		t.Errorf("created base series %+v, want none", l.created)
	}
	if len(l.deleted) != 0 {
		t.Errorf("deleted series %v, want none", l.deleted)
	}
	for id, was := range orig {
		if got := l.seriesOf(id); got != was {
			t.Errorf("book %s moved from series %d to %d (position %s), want it left where it was", id, was, got, l.seqOf(id))
		}
		if got := l.seqOf(id); got != "none" {
			t.Errorf("book %s was given position %s, want none", id, got)
		}
	}
	if len(l.changes) != 0 {
		t.Errorf("%d journal rows written for a run that must change nothing", len(l.changes))
	}
}

// B1: a "Part N" in any punctuation, or one followed by a format word, numbers
// the audio files of one book. Decision 2 of the PR said every one is held.
func TestSeriesDenumber_ReviewB1_PartShapesAreHeld(t *testing.T) {
	for _, name := range []string{
		"Hush Part: 2",
		"Hush Part #2",
		"Hush Part 1 - Unabridged",
		"Hush Pt. 2",
		"Hush Part 2",
		"Hush Part.2",
	} {
		t.Run(name, func(t *testing.T) {
			l, orig := runShape(t, nil, []seriesSpec{{id: 11, name: name, author: denumberAuthor, books: 1, seq: -1}})
			assertUntouched(t, l, orig)
		})
	}
}

// B2: a base that still carries a number, a connector or an ampersand.
func TestSeriesDenumber_ReviewB2_BaseCarryingASecondNumberIsHeld(t *testing.T) {
	for _, name := range []string{
		"Gotham Knights 01 of 03",
		"Gotham Knights Part 02 of 05",
		"Gotham Knights 01 & 02",
		"Gotham Knights 01 to 03",
		"Gotham Knights 1-03",
		"Gotham Knights 01 and 02",
	} {
		t.Run(name, func(t *testing.T) {
			l, orig := runShape(t, nil, []seriesSpec{{id: 11, name: name, author: denumberAuthor, books: 1, seq: -1}})
			assertUntouched(t, l, orig)
		})
	}
}

// B3: ".5" is a fractional position (a novella between two books), not 5.
func TestSeriesDenumber_ReviewB3_FractionalPositionIsHeld(t *testing.T) {
	for _, name := range []string{
		"Gotham Knights Book .5",
		"Gotham Knights #.5",
		"Gotham Knights Book 0.5",
		"Gotham Knights 2.5",
		"Gotham Knights, Book 2.5",
	} {
		t.Run(name, func(t *testing.T) {
			l, orig := runShape(t, nil, []seriesSpec{{id: 11, name: name, author: denumberAuthor, books: 1, seq: -1}})
			assertUntouched(t, l, orig)
		})
	}
}

// B4: a sibling corroborates only when it holds books and the numbers form a
// run. Two unrelated numbers under one base, or an empty row, are not evidence.
func TestSeriesDenumber_ReviewB4_SiblingCorroborationNeedsBooksAndARun(t *testing.T) {
	t.Run("two year-shaped numbers", func(t *testing.T) {
		l, orig := runShape(t, nil, []seriesSpec{
			{id: 11, name: "Batman 66", author: denumberAuthor, books: 1, seq: -1},
			{id: 12, name: "Batman 89", author: denumberAuthor, books: 1, seq: -1},
		})
		assertUntouched(t, l, orig)
	})
	t.Run("an empty sibling is not evidence", func(t *testing.T) {
		l, orig := runShape(t, nil, []seriesSpec{
			{id: 11, name: "Batman 66", author: denumberAuthor, books: 1, seq: -1},
			{id: 12, name: "Batman 02", author: denumberAuthor, books: 0, seq: -1},
		})
		assertUntouched(t, l, orig)
	})
	t.Run("a sibling that is itself held is not evidence", func(t *testing.T) {
		l, orig := runShape(t, nil, []seriesSpec{
			{id: 11, name: "Batman 66", author: denumberAuthor, books: 1, seq: -1},
			{id: 12, name: "Batman Part 1", author: denumberAuthor, books: 1, seq: -1},
		})
		assertUntouched(t, l, orig)
	})
}

// B5: a base that is an author's name in another word order, inside a composite
// credit, or spelled with a decomposed accent.
func TestSeriesDenumber_ReviewB5_AuthorNamedBaseIsHeld(t *testing.T) {
	t.Run("inverted Last, First", func(t *testing.T) {
		l, orig := runShape(t, nil, []seriesSpec{{id: 11, name: "Wayne, Bruce 03", author: denumberAuthor, books: 1, seq: -1}})
		assertUntouched(t, l, orig)
	})
	t.Run("part of a composite credit", func(t *testing.T) {
		l, orig := runShape(t, []string{"Dick Grayson & Barbara Gordon"}, []seriesSpec{
			{id: 11, name: "Barbara Gordon 03", author: denumberAuthor, books: 1, seq: -1},
		})
		assertUntouched(t, l, orig)
	})
	t.Run("decomposed accent against a composed one", func(t *testing.T) {
		l, orig := runShape(t, []string{"Renée Montoya"}, []seriesSpec{
			{id: 11, name: "Renée Montoya 03", author: denumberAuthor, books: 1, seq: -1},
		})
		assertUntouched(t, l, orig)
	})
}

// Controls: the shapes the op exists for still move, so the tier is not empty.
func TestSeriesDenumber_ReviewControls_CorroboratedRunStillMoves(t *testing.T) {
	t.Run("three consecutive numbered series with books", func(t *testing.T) {
		l := newDenumberLib(t)
		for i, n := range []string{"Court of Owls 02", "Court of Owls 03", "Court of Owls 04"} {
			l.addSeries(20+i, n, denumberAuthor)
			l.addBook(bookIDFor(20+i, 0), 20+i, -1, "")
		}
		l.mustRun(shapeApplyParams(t, nil))
		base := l.createdID("Court of Owls")
		if base == 0 {
			t.Fatalf("no base created; events=%v", l.events)
		}
		for i, want := range []string{"2", "3", "4"} {
			id := bookIDFor(20+i, 0)
			if l.seriesOf(id) != base || l.seqOf(id) != want {
				t.Errorf("%s: series %d position %s, want %d position %s", id, l.seriesOf(id), l.seqOf(id), base, want)
			}
		}
	})
	t.Run("a book whose own position matches the name", func(t *testing.T) {
		l := newDenumberLib(t)
		l.addSeries(31, "Court of Owls 05", denumberAuthor)
		l.addBook("owls-5", 31, 5, "")
		l.mustRun(shapeApplyParams(t, nil))
		base := l.createdID("Court of Owls")
		if base == 0 || l.seriesOf("owls-5") != base || l.seqOf("owls-5") != "5" {
			t.Fatalf("book in series %d at %s, want the created base %d at 5", l.seriesOf("owls-5"), l.seqOf("owls-5"), base)
		}
	})
}
