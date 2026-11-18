// file: internal/plugins/metafetch/asin_backfill_retired_test.go
// version: 1.0.1
// guid: c41f6a82-5d93-4e07-8b1a-6f2d9e0a3b57
// last-edited: 2026-11-17

package metafetch

import (
	"context"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// A book trashed or merged away while the backfill was searching Audible for
// it must not get an ASIN or an ISBN-13. The run read the book before the
// search; the write re-reads it under the book's lock, and that re-read has to
// say "retired" too.

const (
	retiredBackfillTitle  = "Batman Year One"
	retiredBackfillAuthor = "Selina Kyle"
	retiredBackfillASIN   = "B0BATRETI1"
	retiredBackfillISBN   = "9780306406157"
)

type retireShape struct {
	name   string
	retire func(b *database.Book)
	// counter reads the counter this shape is a skip of.
	counter func(r asinBackfillResult) int
}

var retireShapes = []retireShape{
	{"soft_deleted", func(b *database.Book) { yes := true; b.MarkedForDeletion = &yes }, func(r asinBackfillResult) int { return int(r.SkippedDeleted) }},
	{"merged_into", func(b *database.Book) { into := "survivor-book-id"; b.MergedIntoBookID = &into }, func(r asinBackfillResult) int { return int(r.SkippedMerged) }},
}

// retiringAudible retires the book the first time the run asks Audible about
// anything: the book is live when the run reads it and retired by the time the
// run comes back to write.
type retiringAudible struct {
	*fakeAudible
	store  *database.PebbleStore
	bookID string
	shape  retireShape
	done   bool
}

func (r *retiringAudible) retireOnce() {
	if r.done {
		return
	}
	r.done = true
	b, err := r.store.GetBookByID(r.bookID)
	if err != nil || b == nil {
		panic("retiringAudible: book not readable")
	}
	r.shape.retire(b)
	if _, err := r.store.UpdateBook(r.bookID, b); err != nil {
		panic("retiringAudible: " + err.Error())
	}
}

func (r *retiringAudible) SearchIdentities(ctx context.Context, q metadata.AudibleIdentityQuery) ([]metadata.AudibleIdentity, error) {
	got, err := r.fakeAudible.SearchIdentities(ctx, q)
	r.retireOnce()
	return got, err
}

func (r *retiringAudible) LookupIdentityByASIN(ctx context.Context, asin string) (*metadata.AudibleIdentity, error) {
	got, err := r.fakeAudible.LookupIdentityByASIN(ctx, asin)
	r.retireOnce()
	return got, err
}

func retiredBackfillProduct() metadata.AudibleIdentity {
	return metadata.AudibleIdentity{ASIN: retiredBackfillASIN, Title: retiredBackfillTitle, Authors: []string{retiredBackfillAuthor},
		ISBN: retiredBackfillISBN, RuntimeMin: 600}
}

func retiredBackfillFake() *fakeAudible {
	p := retiredBackfillProduct()
	return &fakeAudible{
		byTitle: map[string][]metadata.AudibleIdentity{"batman year one": {p}},
		byKW:    map[string][]metadata.AudibleIdentity{retiredBackfillISBN: {p}},
		byASIN:  map[string]*metadata.AudibleIdentity{retiredBackfillASIN: &p},
	}
}

// newRetiringBackfill is a plugin whose Audible client retires bookID (set
// after the book exists) during the first request.
func newRetiringBackfill(t *testing.T, shape retireShape) (*Plugin, *database.PebbleStore, *retiringAudible) {
	t.Helper()
	fa := retiredBackfillFake()
	ra := &retiringAudible{fakeAudible: fa, shape: shape}
	p, s := newASINTestPlugin(t, fa)
	ra.store = s
	p.newAudible = func() audibleIdentitySearcher { return ra }
	return p, s, ra
}

// Control: the same fixture on a live book writes the ASIN, so the refusals
// below are the retirement check's.
func TestASINBackfill_ControlLiveBookGetsASINAndISBN(t *testing.T) {
	p, s := newASINTestPlugin(t, retiredBackfillFake())
	aid := mkAuthor(t, s, retiredBackfillAuthor)
	b := mkBook(t, s, &database.Book{Title: retiredBackfillTitle, AuthorID: &aid, ISBN13: new(retiredBackfillISBN), FilePath: "/x/by1.m4b"})
	res := runASIN(t, p, `{"dry_run":false}`).res(t)
	if got := asinOf(t, s, b.ID); got != retiredBackfillASIN {
		t.Fatalf("control: asin = %q, result %+v", got, res)
	}
}

func TestASINBackfill_BookRetiredDuringSearchGetsNoASIN(t *testing.T) {
	for _, shape := range retireShapes {
		t.Run(shape.name, func(t *testing.T) {
			p, s, ra := newRetiringBackfill(t, shape)
			aid := mkAuthor(t, s, retiredBackfillAuthor)
			b := mkBook(t, s, &database.Book{Title: retiredBackfillTitle, AuthorID: &aid, ISBN13: new(retiredBackfillISBN), FilePath: "/x/by1.m4b"})
			ra.bookID = b.ID

			res := runASIN(t, p, `{"dry_run":false}`).res(t)
			if !ra.done {
				t.Fatalf("fixture: the book was never retired during the run; result %+v", res)
			}
			if got := asinOf(t, s, b.ID); got != "" {
				t.Fatalf("wrote ASIN %q onto a book retired during the search; result %+v", got, res)
			}
			if i13, _ := isbnOf(t, s, b.ID); i13 != retiredBackfillISBN {
				t.Fatalf("ISBN-13 changed to %q", i13)
			}
			if res.MatchedWritten != 0 || res.ISBNWritten != 0 {
				t.Fatalf("counted a write: %+v", res)
			}
			if res.Errored != 0 {
				t.Fatalf("a retired skip was counted as a failure: %+v", res)
			}
			if shape.counter(res) != 1 {
				t.Fatalf("not counted as a retired skip (%s): %+v", shape.name, res)
			}
		})
	}
}

func TestASINBackfill_BookRetiredDuringISBNLookupGetsNoISBN(t *testing.T) {
	for _, shape := range retireShapes {
		t.Run(shape.name, func(t *testing.T) {
			p, s, ra := newRetiringBackfill(t, shape)
			aid := mkAuthor(t, s, retiredBackfillAuthor)
			b := mkBook(t, s, &database.Book{Title: retiredBackfillTitle, AuthorID: &aid, ASIN: new(retiredBackfillASIN), FilePath: "/x/by1.m4b"})
			ra.bookID = b.ID

			res := runASIN(t, p, `{"dry_run":false}`).res(t)
			if !ra.done {
				t.Fatalf("fixture: the book was never retired during the run; result %+v", res)
			}
			if i13, _ := isbnOf(t, s, b.ID); i13 != "" {
				t.Fatalf("wrote ISBN-13 %q onto a book retired during the lookup; result %+v", i13, res)
			}
			if res.ISBNWritten != 0 || res.Errored != 0 {
				t.Fatalf("result %+v", res)
			}
			if shape.counter(res) != 1 {
				t.Fatalf("not counted as a retired skip (%s): %+v", shape.name, res)
			}
		})
	}
}

// The start-of-run check is the one retired rule (database.Book.IsRetired), the
// same one the under-lock rechecks use, so a run cannot read a book as live and
// then find it retired by its own definition. The only input the old open-coded
// copy answered differently was a whitespace-only merged-into id (it trimmed it
// to "live"; IsRetired does not trim): such a row is retired here too. No writer
// produces one, so this pins agreement rather than a case anyone depends on.
func TestASINBackfill_StartOfRunUsesTheOneRetiredRule(t *testing.T) {
	fa := retiredBackfillFake()
	p, s := newASINTestPlugin(t, fa)
	aid := mkAuthor(t, s, retiredBackfillAuthor)
	b := mkBook(t, s, &database.Book{Title: retiredBackfillTitle, AuthorID: &aid, ISBN13: new(retiredBackfillISBN),
		FilePath: "/x/by1.m4b", MergedIntoBookID: new("   ")})
	if !b.IsRetired() {
		t.Fatalf("fixture: IsRetired = false for a merged-into id of spaces; the one rule changed")
	}

	res := runASIN(t, p, `{"dry_run":false,"book_ids":["`+b.ID+`"]}`).res(t)
	if res.SkippedMerged != 1 || fa.calls.Load() != 0 || asinOf(t, s, b.ID) != "" {
		t.Fatalf("result = %+v calls = %d", res, fa.calls.Load())
	}
}
