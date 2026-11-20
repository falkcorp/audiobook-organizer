// file: internal/books/store/author_books_legacy_retired_test.go
// version: 0.1.0
// guid: 5c1e7a9d-3b24-4f86-9d0a-6e2b8c4f17a3
// last-edited: 2026-11-19

package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"
)

// Fixtures are Batman comics and characters only.

// abAuthorFamilyKeys is every stored key the store-key diff files under the
// authors resource: author:, author_* (both index families and the rest) and
// book_authors:. It is the evidence F5-18j's narrowed rows rest on.
func abAuthorFamilyKeys(t *testing.T, p *PebbleStore) []string {
	t.Helper()
	var keys []string
	for _, prefix := range []string{"author:", "author_", "book_authors:"} {
		require.NoError(t, forEachKeyInRange(p.db, []byte(prefix), prefixEnd([]byte(prefix)), func(k, _ []byte) error {
			keys = append(keys, string(k))
			return nil
		}))
	}
	sort.Strings(keys)
	return keys
}

// TestBookWrites_TouchNoAuthorsFamilyKey is the store-side proof behind
// dropping `authors` from the CreateBook, UpdateBook, ModifyBook and
// RecomputeBookAggregates rows (internal/maintenance/resources): with an
// AuthorID set, changed and cleared, none of them changes a key in any family
// the store-key diff maps to `authors`. Until F5-18j every one of them staged an
// author_books_legacy: row.
func TestBookWrites_TouchNoAuthorsFamilyKey(t *testing.T) {
	p := newAuthorBooksPebble(t)
	bruce := abMakeAuthor(t, p, "Bruce Wayne")
	selina := abMakeAuthor(t, p, "Selina Kyle")

	step := func(name string, write func()) {
		t.Helper()
		before := abAuthorFamilyKeys(t, p)
		write()
		require.Equal(t, before, abAuthorFamilyKeys(t, p), "%s changed an authors-family key", name)
	}

	var book *Book
	step("CreateBook with an AuthorID", func() { book = abMakeBook(t, p, "The Dark Knight Returns", bruce) })
	step("CreateBook without an AuthorID", func() { abMakeBook(t, p, "Arkham Asylum", 0) })

	step("UpdateBook changes the AuthorID", func() {
		fresh, err := p.GetBookByID(book.ID)
		require.NoError(t, err)
		fresh.AuthorID = &selina
		_, err = p.UpdateBook(book.ID, fresh)
		require.NoError(t, err)
	})
	step("UpdateBook leaves the AuthorID", func() {
		fresh, err := p.GetBookByID(book.ID)
		require.NoError(t, err)
		fresh.Title = "The Dark Knight Returns (Absolute)"
		_, err = p.UpdateBook(book.ID, fresh)
		require.NoError(t, err)
	})
	step("ModifyBook changes the AuthorID", func() {
		_, err := p.ModifyBook(book.ID, func(b *Book) error { b.AuthorID = &bruce; return nil })
		require.NoError(t, err)
	})
	step("ModifyBook clears the AuthorID", func() {
		_, err := p.ModifyBook(book.ID, func(b *Book) error { b.AuthorID = nil; return nil })
		require.NoError(t, err)
	})
	step("ModifyBook sets the AuthorID again", func() {
		_, err := p.ModifyBook(book.ID, func(b *Book) error { b.AuthorID = &selina; return nil })
		require.NoError(t, err)
	})
	step("RecomputeBookAggregates", func() { require.NoError(t, p.RecomputeBookAggregates(book.ID)) })
}

// abAllKeysExceptLegacy is every stored key outside the retired family, with
// its value: what a retirement must leave exactly as it was.
func abAllKeysExceptLegacy(t *testing.T, p *PebbleStore) map[string]string {
	t.Helper()
	out := map[string]string{}
	require.NoError(t, forEachKeyInRange(p.db, []byte{0}, []byte{0xff, 0xff, 0xff}, func(k, v []byte) error {
		if !strings.HasPrefix(string(k), authorBooksLegacyPrefix) {
			out[string(k)] = string(v)
		}
		return nil
	}))
	return out
}

// abSeedLegacy writes retired rows the way builds before F5-18j left them.
func abSeedLegacy(t *testing.T, p *PebbleStore, keys ...[]byte) {
	t.Helper()
	for _, k := range keys {
		require.NoError(t, p.db.Set(k, []byte{}, pebble.Sync))
	}
}

// abRetireFixture is a store where every book's AuthorID is credited, plus the
// legacy rows an older build left: one live, one for a book since deleted, one
// for an author the book has since moved away from.
type abRetireFixture struct {
	bruce, selina int
	books         []string
	legacy        [][]byte
}

func buildABRetireFixture(t *testing.T, p *PebbleStore) abRetireFixture {
	t.Helper()
	fx := abRetireFixture{bruce: abMakeAuthor(t, p, "Bruce Wayne"), selina: abMakeAuthor(t, p, "Selina Kyle")}
	yes, no := true, false
	yearOne := abMakeBook(t, p, "Batman: Year One", fx.bruce)
	require.NoError(t, p.SetBookAuthors(yearOne.ID, abCredits(yearOne.ID, fx.bruce)))
	// A trashed and a non-primary book, both credited: every state is checked.
	tr := abMakeBook(t, p, "Batman: Knightfall", fx.selina)
	require.NoError(t, p.SetBookAuthors(tr.ID, abCredits(tr.ID, fx.bruce, fx.selina)))
	tr.MarkedForDeletion = &yes
	_, err := p.UpdateBook(tr.ID, tr)
	require.NoError(t, err)
	np := abMakeBook(t, p, "Batman: Hush", fx.bruce)
	require.NoError(t, p.SetBookAuthors(np.ID, abCredits(np.ID, fx.bruce)))
	np.IsPrimaryVersion = &no
	_, err = p.UpdateBook(np.ID, np)
	require.NoError(t, err)
	abMakeBook(t, p, "Batman: Arkham Asylum", 0) // no AuthorID: nothing to cover
	fx.books = []string{yearOne.ID, tr.ID, np.ID}
	fx.legacy = [][]byte{
		abLegacyKey(fx.bruce, yearOne.ID),
		abLegacyKey(fx.selina, "01GONEBOOKGONEBOOKGONEBOOK"), // stale: the book is gone
		abLegacyKey(fx.selina, yearOne.ID),                   // stale: the AuthorID moved on
	}
	abSeedLegacy(t, p, fx.legacy...)
	return fx
}

// TestRetireAuthorBooksLegacy_PreviewWritesNothing: the default run counts and
// writes nothing at all.
func TestRetireAuthorBooksLegacy_PreviewWritesNothing(t *testing.T) {
	p := newAuthorBooksPebble(t)
	fx := buildABRetireFixture(t, p)
	before := abAllKeysExceptLegacy(t, p)

	rep, err := p.CheckAuthorBooksLegacyRetire(context.Background(), nil)
	require.NoError(t, err)
	require.False(t, rep.Apply)
	require.False(t, rep.Refused, "stale rows never block")
	require.Equal(t, 4, rep.BookRows)
	require.Equal(t, 3, rep.WithAuthorID)
	require.Equal(t, 3, rep.Credited)
	require.Zero(t, rep.Blocking)
	require.Equal(t, 3, rep.LegacyRows)
	require.Zero(t, rep.Deleted)
	require.Len(t, abLegacyKeys(t, p), len(fx.legacy))
	require.Equal(t, before, abAllKeysExceptLegacy(t, p))
}

// TestRetireAuthorBooksLegacy_ApplyDeletesEveryLegacyRowAndNothingElse, then a
// second run finds nothing to do (idempotent).
func TestRetireAuthorBooksLegacy_ApplyDeletesEveryLegacyRowAndNothingElse(t *testing.T) {
	p := newAuthorBooksPebble(t)
	buildABRetireFixture(t, p)
	before := abAllKeysExceptLegacy(t, p)

	var phases []string
	rep, err := p.RetireAuthorBooksLegacy(context.Background(), func(phase string, _ int) { phases = append(phases, phase) })
	require.NoError(t, err)
	require.True(t, rep.Apply)
	require.False(t, rep.Refused)
	require.Equal(t, 3, rep.LegacyRows)
	require.Equal(t, 3, rep.Deleted)
	require.Empty(t, abLegacyKeys(t, p))
	require.Equal(t, before, abAllKeysExceptLegacy(t, p), "only author_books_legacy: rows may change")
	require.Contains(t, phases, "deleting legacy rows")

	rep, err = p.RetireAuthorBooksLegacy(context.Background(), nil)
	require.NoError(t, err)
	require.Zero(t, rep.LegacyRows)
	require.Zero(t, rep.Deleted)
	require.Equal(t, before, abAllKeysExceptLegacy(t, p))
}

// TestRetireAuthorBooksLegacy_RefusesWhileAnAuthorIDIsUncredited: every class
// of book whose AuthorID the credit list does not hold blocks the delete, in
// every book state, and is reported instead; nothing is deleted. Crediting the
// authors lets the next run retire.
func TestRetireAuthorBooksLegacy_RefusesWhileAnAuthorIDIsUncredited(t *testing.T) {
	p := newAuthorBooksPebble(t)
	fx := buildABRetireFixture(t, p)
	yes := true
	dick := abMakeAuthor(t, p, "Dick Grayson")

	noCredit := abMakeBook(t, p, "Batman: The Killing Joke", fx.bruce)
	notIn := abMakeBook(t, p, "Batman: The Long Halloween", fx.selina)
	require.NoError(t, p.SetBookAuthors(notIn.ID, abCredits(notIn.ID, fx.bruce)))
	trashedNoCredit := abMakeBook(t, p, "Batman: A Death in the Family", dick)
	trashedNoCredit.MarkedForDeletion = &yes
	_, err := p.UpdateBook(trashedNoCredit.ID, trashedNoCredit)
	require.NoError(t, err)
	undecodable := abMakeBook(t, p, "Batman: Dark Victory", fx.bruce)
	require.NoError(t, p.db.Set(bookAuthorsKey(undecodable.ID), []byte("{not json"), pebble.Sync))
	ghost := 987654 // no author row: a dangling AuthorID
	dangling := abMakeBook(t, p, "Batman: The Cult", ghost)
	abSeedLegacy(t, p, abLegacyKey(fx.bruce, noCredit.ID))
	before := abAllKeysExceptLegacy(t, p)
	legacyBefore := abLegacyKeys(t, p)

	for _, apply := range []bool{false, true} {
		run := p.CheckAuthorBooksLegacyRetire
		if apply {
			run = p.RetireAuthorBooksLegacy
		}
		rep, err := run(context.Background(), nil)
		require.Equal(t, apply, rep.Apply)
		if apply {
			require.ErrorIs(t, err, ErrAuthorBooksLegacyRetireRefused)
		} else {
			require.NoError(t, err, "a preview reports, it does not fail")
		}
		require.True(t, rep.Refused)
		require.Equal(t, 3, rep.NoCreditRow, "killing joke, the trashed book, the dangling author")
		require.Equal(t, 1, rep.NotInCredits)
		require.Equal(t, 1, rep.UnreadableCredits)
		require.Equal(t, 5, rep.Blocking)
		require.Equal(t, 4, rep.BlockingListable, "all but the trashed book")
		require.Equal(t, 1, rep.BlockingDanglingAuthor)
		require.Zero(t, rep.Deleted)
		require.Len(t, rep.SampleBlocking, 5)
		joined := strings.Join(rep.SampleBlocking, "\n")
		for _, want := range []string{
			fmt.Sprintf("no_credit_row book:%s author:%d", noCredit.ID, fx.bruce),
			fmt.Sprintf("not_in_credits book:%s author:%d", notIn.ID, fx.selina),
			fmt.Sprintf("no_credit_row book:%s author:%d", trashedNoCredit.ID, dick),
			fmt.Sprintf("unreadable_credits book:%s author:%d", undecodable.ID, fx.bruce),
			fmt.Sprintf("no_credit_row book:%s author:%d", dangling.ID, ghost),
		} {
			require.Contains(t, joined, want)
		}
		require.Equal(t, legacyBefore, abLegacyKeys(t, p), "a refused or preview run deletes nothing")
		require.Equal(t, before, abAllKeysExceptLegacy(t, p))
	}

	for _, b := range []struct {
		id      string
		credits []int
	}{
		{noCredit.ID, []int{fx.bruce}}, {notIn.ID, []int{fx.bruce, fx.selina}},
		{trashedNoCredit.ID, []int{dick}}, {undecodable.ID, []int{fx.bruce}}, {dangling.ID, []int{ghost}},
	} {
		require.NoError(t, p.SetBookAuthors(b.id, abCredits(b.id, b.credits...)))
	}
	rep, err := p.RetireAuthorBooksLegacy(context.Background(), nil)
	require.NoError(t, err)
	require.False(t, rep.Refused)
	require.Equal(t, len(legacyBefore), rep.Deleted)
	require.Empty(t, abLegacyKeys(t, p))
}

// TestRetireAuthorBooksLegacy_OneUncreditedBookRefuses: a single book whose
// AuthorID only a legacy row holds is enough to refuse; the refusal has no
// tolerance threshold.
func TestRetireAuthorBooksLegacy_OneUncreditedBookRefuses(t *testing.T) {
	p := newAuthorBooksPebble(t)
	fx := buildABRetireFixture(t, p)
	lone := abMakeBook(t, p, "Batman: The Man Who Laughs", fx.bruce)
	abSeedLegacy(t, p, abLegacyKey(fx.bruce, lone.ID))
	legacyBefore := abLegacyKeys(t, p)

	preview, err := p.CheckAuthorBooksLegacyRetire(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, 1, preview.Blocking)
	require.True(t, preview.Refused, "a preview says a live run would refuse")

	rep, err := p.RetireAuthorBooksLegacy(context.Background(), nil)
	require.ErrorIs(t, err, ErrAuthorBooksLegacyRetireRefused)
	require.True(t, rep.Refused)
	require.Equal(t, 1, rep.Blocking)
	require.Equal(t, []string{fmt.Sprintf("no_credit_row book:%s author:%d", lone.ID, fx.bruce)}, rep.SampleBlocking)
	require.Zero(t, rep.Deleted)
	require.Equal(t, legacyBefore, abLegacyKeys(t, p))
}

// TestRetireAuthorBooksLegacy_UndecodableBookRowNeverBlocks: no path can list a
// book whose row does not decode, so its legacy row protects nothing.
func TestRetireAuthorBooksLegacy_UndecodableBookRowNeverBlocks(t *testing.T) {
	p := newAuthorBooksPebble(t)
	fx := buildABRetireFixture(t, p)
	require.NoError(t, p.db.Set([]byte(bookRowPrefix+fx.books[0]), []byte("{not json"), pebble.Sync))
	rep, err := p.RetireAuthorBooksLegacy(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, 1, rep.UnreadableBooks)
	require.False(t, rep.Refused)
	require.Empty(t, abLegacyKeys(t, p))
}

// TestRetireAuthorBooksLegacy_ResumesAfterACancelledDelete: a run cancelled
// between delete commits stops with the count it deleted, deletes nothing past
// the commit, and the next run deletes the rest.
func TestRetireAuthorBooksLegacy_ResumesAfterACancelledDelete(t *testing.T) {
	p := newAuthorBooksPebble(t)
	buildABRetireFixture(t, p)
	extra := make([][]byte, 0, 2*authorBooksBatchRows)
	for i := range 2 * authorBooksBatchRows {
		extra = append(extra, abLegacyKey(1, fmt.Sprintf("01STALEBOOK%015d", i)))
	}
	abSeedLegacy(t, p, extra...)
	total := len(abLegacyKeys(t, p))
	require.Equal(t, 2*authorBooksBatchRows+3, total)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rep, err := p.RetireAuthorBooksLegacy(ctx, func(phase string, n int) {
		if phase == "deleting legacy rows" && n >= authorBooksBatchRows {
			cancel()
		}
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, context.Canceled))
	require.Equal(t, authorBooksBatchRows, rep.Deleted)
	require.Len(t, abLegacyKeys(t, p), total-authorBooksBatchRows)

	rep, err = p.RetireAuthorBooksLegacy(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, total-authorBooksBatchRows, rep.LegacyRows)
	require.Equal(t, total-authorBooksBatchRows, rep.Deleted)
	require.Empty(t, abLegacyKeys(t, p))
}
