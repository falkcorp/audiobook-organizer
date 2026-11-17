// file: internal/server/series_normalize_partial_merge_test.go
// version: 1.0.1
// guid: b17e5c40-92d3-4a68-8f01-6c4d7a3e2b95
// last-edited: 2026-11-17

package server

import (
	"context"
	"errors"
	"sort"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// PR #4698 round 2, B2(c), the series-normalize writer. A merge repoints a numbered
// series' books one at a time; when one repoint fails, the pass records the number
// it parsed from the numbered row's NAME as the position of EVERY book of that row
// (positionByBook is built before the merge) and writes it in the third pass,
// whether or not the merge succeeded. The book left behind therefore sits in
// "Bane 05" at position 5: its position is the number in the name, copied from
// the name, not a statement the book made. (A merge here only ever folds one
// contaminated row into another that normalizes to the same name, so the keeper
// is "Bane 03" renamed to "Bane", never a plain "Bane" row that already existed.)
//
// series-denumber must not read that as two independent statements agreeing. This
// pins the state the writer leaves; the decision half is
// TestSeriesDenumber_Round3_B2c_AnOwnPositionCopiedFromTheNameIsNotEvidence.
//
// The injected failure hits only the SeriesID repoint. The later sequence write
// goes through the same ModifyBook, so a blanket failure would also stop the
// position write and leave no such state.
func TestExecuteSeriesNormalize_PartialMergeLeavesTheNameParsedPosition(t *testing.T) {
	authorID := 1
	f := newNormalizeFixture(t,
		[]database.Series{
			{ID: 51, Name: "Bane 03", AuthorID: &authorID},
			{ID: 52, Name: "Bane 05", AuthorID: &authorID},
		},
		map[string]*database.Book{
			"book-keeper":   {ID: "book-keeper", Title: "Knightfall", SeriesID: new(51)},
			"book-moved":    {ID: "book-moved", Title: "Knightquest", SeriesID: new(52)},
			"book-stranded": {ID: "book-stranded", Title: "Knights End", SeriesID: new(52)},
		})
	// The fixture lists a series' books in map order, and the pass stops repointing a
	// row at its first failure, so which book is stranded would change from run to
	// run. Listed by id, "book-moved" is repointed before "book-stranded" fails.
	for _, p := range []*func(int) ([]database.BookCore, error){
		&f.store.GetBooksBySeriesIDCoreFunc, &f.store.GetBooksBySeriesIDAllVersionsFunc,
	} {
		inner := *p
		*p = func(id int) ([]database.BookCore, error) {
			out, err := inner(id)
			sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
			return out, err
		}
	}
	f.store.ModifyBookFunc = func(id string, fn func(*database.Book) error) (*database.Book, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		stored, ok := f.books[id]
		if !ok {
			return nil, nil
		}
		cp := *stored
		if err := fn(&cp); err != nil {
			if errors.Is(err, database.ErrSkipBookWrite) {
				return &cp, nil
			}
			return nil, err
		}
		repoint := stored.SeriesID != nil && cp.SeriesID != nil && *stored.SeriesID != *cp.SeriesID
		if id == "book-stranded" && repoint {
			return nil, errors.New("injected: the repoint of this one book fails")
		}
		f.books[id] = &cp
		return &cp, nil
	}

	// The pass reports the partial merge as an error; the state is what matters.
	if _, err := executeSeriesNormalizeCore(context.Background(), f.store, ""); err == nil {
		t.Fatal("want the pass to report the failed repoint, got nil")
	}

	moved, stranded := f.books["book-moved"], f.books["book-stranded"]
	if moved.SeriesID == nil || *moved.SeriesID != 51 {
		t.Fatalf("book-moved series = %v, want the keeper 51", derefInt(moved.SeriesID))
	}
	if got := f.renamedTo(51); got != "Bane" {
		t.Fatalf("keeper renamed to %q, want Bane", got)
	}
	if stranded.SeriesID == nil || *stranded.SeriesID != 52 {
		t.Fatalf("book-stranded series = %v, want it left in the numbered row 52", derefInt(stranded.SeriesID))
	}
	if got := f.seq(t, "book-stranded"); got == nil || *got != 5 {
		t.Fatalf("book-stranded position = %v, want 5: the number parsed from the name of the row it is stranded in", derefInt(got))
	}
}

// derefInt prints a *int as its value, or "nil", so a failure message names the
// series or position instead of an address.
func derefInt(p *int) any {
	if p == nil {
		return "nil"
	}
	return *p
}
