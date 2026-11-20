// file: internal/books/store/pebble_store_author_books_index_test.go
// version: 1.1.1
// guid: d82b0da1-45c3-44dd-974b-c1d9ed6d5f6d
// last-edited: 2026-11-09

package store

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"
)

// Fixtures are Batman comics and characters only.

func newAuthorBooksPebble(t *testing.T) *PebbleStore {
	t.Helper()
	store, cleanup := setupPebbleTestDB(t)
	t.Cleanup(cleanup)
	p, ok := store.(*PebbleStore)
	require.True(t, ok, "expected *PebbleStore")
	return p
}

func abMakeAuthor(t *testing.T, p *PebbleStore, name string) int {
	t.Helper()
	a, err := p.CreateAuthor(name)
	require.NoError(t, err)
	return a.ID
}

// abMakeBook creates a primary-version book. authorID may be 0 for none.
func abMakeBook(t *testing.T, p *PebbleStore, title string, authorID int) *Book {
	t.Helper()
	yes := true
	b := &Book{Title: title, FilePath: "/lib/batman/" + title, IsPrimaryVersion: &yes}
	if authorID != 0 {
		b.AuthorID = &authorID
	}
	created, err := p.CreateBook(b)
	require.NoError(t, err)
	return created
}

func abCredits(bookID string, authorIDs ...int) []BookAuthor {
	rows := make([]BookAuthor, 0, len(authorIDs))
	for i, id := range authorIDs {
		rows = append(rows, BookAuthor{BookID: bookID, AuthorID: id, Role: "author", Position: i})
	}
	return rows
}

// abIndexKeys is every key of the index family as stored.
func abIndexKeys(t *testing.T, p *PebbleStore) []string {
	t.Helper()
	var keys []string
	require.NoError(t, forEachKeyInRange(p.db, []byte(authorBooksPrefix), prefixEnd([]byte(authorBooksPrefix)), func(k, _ []byte) error {
		keys = append(keys, string(k))
		return nil
	}))
	sort.Strings(keys)
	return keys
}

// abLegacyKeys is every retired author_books_legacy: row as stored.
func abLegacyKeys(t *testing.T, p *PebbleStore) []string {
	t.Helper()
	var keys []string
	require.NoError(t, forEachKeyInRange(p.db, []byte(authorBooksLegacyPrefix), prefixEnd([]byte(authorBooksLegacyPrefix)), func(k, _ []byte) error {
		keys = append(keys, string(k))
		return nil
	}))
	sort.Strings(keys)
	return keys
}

// abLegacyKey is the retired family's key for (authorID, bookID), as builds
// before F5-18j wrote it. Tests seed it to stand for rows those builds left.
func abLegacyKey(authorID int, bookID string) []byte {
	return []byte(authorBooksLegacyPrefix + strconv.Itoa(authorID) + ":" + bookID)
}

// abTruthKeys derives the exact key set the rows imply: one credit key per
// author in each book_authors row. Book.AuthorID implies no key.
func abTruthKeys(t *testing.T, p *PebbleStore) []string {
	t.Helper()
	var keys []string
	require.NoError(t, forEachKeyInRange(p.db, []byte("book_authors:"), prefixEnd([]byte("book_authors:")), func(k, v []byte) error {
		ids, ok := decodeCreditAuthorIDs(v)
		require.True(t, ok, "undecodable credit row %q", k)
		for _, id := range ids {
			keys = append(keys, string(authorBooksKey(id, strings.TrimPrefix(string(k), "book_authors:"))))
		}
		return nil
	}))
	sort.Strings(keys)
	return keys
}

// requireIndexMatchesRows is the sync invariant: after any write, the stored
// index is EXACTLY what the credit rows imply, no key missing and none extra,
// and no write staged a retired author_books_legacy: row.
func requireIndexMatchesRows(t *testing.T, p *PebbleStore, step string) {
	t.Helper()
	require.Equal(t, abTruthKeys(t, p), abIndexKeys(t, p), "author_books index out of sync after: %s", step)
	require.Empty(t, abLegacyKeys(t, p), "%s staged a retired author_books_legacy: row", step)
}

func abIDs(books []BookCore) []string {
	ids := make([]string, 0, len(books))
	for _, b := range books {
		ids = append(ids, b.ID)
	}
	sort.Strings(ids)
	return ids
}

// TestAuthorBooksIndex_StaysInSyncAcrossWrites drives every writer of either
// source and checks the exact-match invariant after each step.
func TestAuthorBooksIndex_StaysInSyncAcrossWrites(t *testing.T) {
	p := newAuthorBooksPebble(t)
	bruce := abMakeAuthor(t, p, "Bruce Wayne")
	dick := abMakeAuthor(t, p, "Dick Grayson")
	alfred := abMakeAuthor(t, p, "Alfred Pennyworth")
	selina := abMakeAuthor(t, p, "Selina Kyle")

	// insert
	yearOne := abMakeBook(t, p, "Year One", bruce)
	requireIndexMatchesRows(t, p, "CreateBook with AuthorID")
	require.Empty(t, abIndexKeys(t, p), "a book row's AuthorID stages no index key")
	noAuthor := abMakeBook(t, p, "Hush", 0)
	requireIndexMatchesRows(t, p, "CreateBook without AuthorID")

	// credit rows: add, then drop one, then replace
	require.NoError(t, p.SetBookAuthors(yearOne.ID, abCredits(yearOne.ID, bruce, dick)))
	requireIndexMatchesRows(t, p, "SetBookAuthors adds a co-author")
	require.Contains(t, abIndexKeys(t, p), string(authorBooksKey(dick, yearOne.ID)))
	require.NoError(t, p.SetBookAuthors(yearOne.ID, abCredits(yearOne.ID, dick)))
	requireIndexMatchesRows(t, p, "SetBookAuthors drops the primary from the credit row")
	require.NotContains(t, abIndexKeys(t, p), string(authorBooksKey(bruce, yearOne.ID)),
		"credit key of a dropped author must go, whatever the row's AuthorID says")
	_, err := p.ModifyBookAuthors(noAuthor.ID, func(rows []BookAuthor) ([]BookAuthor, error) {
		return abCredits(noAuthor.ID, alfred), nil
	})
	require.NoError(t, err)
	requireIndexMatchesRows(t, p, "ModifyBookAuthors")

	// author change on the row
	fresh, err := p.GetBookByID(yearOne.ID)
	require.NoError(t, err)
	fresh.AuthorID = &selina
	_, err = p.UpdateBook(yearOne.ID, fresh)
	require.NoError(t, err)
	requireIndexMatchesRows(t, p, "UpdateBook changes AuthorID")
	require.NotContains(t, abIndexKeys(t, p), string(authorBooksKey(selina, yearOne.ID)),
		"an AuthorID change is not a credit")

	// an unrelated UpdateBook keeps the keys
	fresh, err = p.GetBookByID(yearOne.ID)
	require.NoError(t, err)
	fresh.Title = "Year One (Deluxe)"
	_, err = p.UpdateBook(yearOne.ID, fresh)
	require.NoError(t, err)
	requireIndexMatchesRows(t, p, "UpdateBook that leaves authors alone")

	// row + credits in one batch
	_, _, err = p.ModifyBookCredits(noAuthor.ID, func(b *Book, c *BookCreditsEdit) error {
		b.AuthorID = &bruce
		c.Authors = abCredits(noAuthor.ID, bruce, alfred, selina)
		return nil
	})
	require.NoError(t, err)
	requireIndexMatchesRows(t, p, "ModifyBookCredits adds credits and an AuthorID")
	_, _, err = p.ModifyBookCredits(noAuthor.ID, func(b *Book, c *BookCreditsEdit) error {
		b.AuthorID = &alfred
		c.Authors = abCredits(noAuthor.ID, alfred)
		return nil
	})
	require.NoError(t, err)
	requireIndexMatchesRows(t, p, "ModifyBookCredits drops credits and moves the AuthorID")

	// delete author: its books are repointed first, its credit keys swept
	require.NoError(t, p.DeleteAuthor(selina))
	requireIndexMatchesRows(t, p, "DeleteAuthor")
	for _, k := range abIndexKeys(t, p) {
		require.False(t, strings.HasPrefix(k, authorBooksPrefix+strconv.Itoa(selina)+":"), "credit key of deleted author left: %s", k)
	}

	// delete book removes every key it owned
	require.NoError(t, p.DeleteBook(yearOne.ID))
	requireIndexMatchesRows(t, p, "DeleteBook")
	for _, k := range abIndexKeys(t, p) {
		require.False(t, strings.HasSuffix(k, ":"+yearOne.ID), "key of deleted book left: %s", k)
	}
}

// TestAuthorBooksIndex_StaysInSyncAcrossMerge folds one author into another the
// way the merge op does (relink every book, then MergeDeleteAuthor).
func TestAuthorBooksIndex_StaysInSyncAcrossMerge(t *testing.T) {
	p := newAuthorBooksPebble(t)
	keep := abMakeAuthor(t, p, "Bruce Wayne")
	dupe := abMakeAuthor(t, p, "Bruce Wayne Jr")
	other := abMakeAuthor(t, p, "Barbara Gordon")

	killingJoke := abMakeBook(t, p, "The Killing Joke", dupe)
	require.NoError(t, p.SetBookAuthors(killingJoke.ID, abCredits(killingJoke.ID, dupe)))
	knightfall := abMakeBook(t, p, "Knightfall", other)
	require.NoError(t, p.SetBookAuthors(knightfall.ID, abCredits(knightfall.ID, other, dupe)))
	requireIndexMatchesRows(t, p, "seed")

	books, err := p.GetBooksByAuthorIDForRelinkCore(dupe)
	require.NoError(t, err)
	require.Len(t, books, 2)
	for _, bc := range books {
		_, _, err := p.ModifyBookCredits(bc.ID, func(b *Book, c *BookCreditsEdit) error {
			if b.AuthorID != nil && *b.AuthorID == dupe {
				b.AuthorID = &keep
			}
			for i := range c.Authors {
				if c.Authors[i].AuthorID == dupe {
					c.Authors[i].AuthorID = keep
				}
			}
			return nil
		})
		require.NoError(t, err)
	}
	requireIndexMatchesRows(t, p, "relink to the survivor")
	_, err = p.MergeDeleteAuthor(dupe, keep)
	require.NoError(t, err)
	requireIndexMatchesRows(t, p, "MergeDeleteAuthor")
	for _, k := range abIndexKeys(t, p) {
		require.False(t, strings.Contains(k, ":"+strconv.Itoa(dupe)+":"), "key of the merged-away author left: %s", k)
	}

	rep, err := p.ReconcileAuthorBooksIndex(context.Background())
	require.NoError(t, err)
	require.True(t, rep.Trusted)
	p.UseMemDB = false
	defer func() { p.UseMemDB = true }()
	got, err := p.GetBooksByAuthorIDCore(keep)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{killingJoke.ID, knightfall.ID}, abIDs(got))
}

// abBatmanFixture is the conformance fixture on Batman names: a book credited to
// the author who is also its AuthorID, a co-author-only link, a non-primary
// version, a trashed book, and an unrelated one. Every listable book's AuthorID
// is credited, so a reconcile trusts the index; the trashed book's AuthorID is
// not, and must not hold trust (no listing returns it).
type abBatmanFixture struct {
	author, other                                                  int
	credited, coAuthored, nonPrimary, trashed, unrelated, otherCol string
}

func buildABBatmanFixture(t *testing.T, p *PebbleStore) abBatmanFixture {
	t.Helper()
	var fx abBatmanFixture
	fx.author = abMakeAuthor(t, p, "Bruce Wayne")
	other := abMakeAuthor(t, p, "Alfred Pennyworth")
	fx.other = other
	yes, no := true, false

	cr := abMakeBook(t, p, "Batman Year One", fx.author)
	require.NoError(t, p.SetBookAuthors(cr.ID, abCredits(cr.ID, fx.author)))
	fx.credited = cr.ID

	co := abMakeBook(t, p, "The Long Halloween", other)
	require.NoError(t, p.SetBookAuthors(co.ID, abCredits(co.ID, other, fx.author)))
	fx.coAuthored = co.ID

	np := abMakeBook(t, p, "Hush", other)
	np.IsPrimaryVersion = &no
	_, err := p.UpdateBook(np.ID, np)
	require.NoError(t, err)
	require.NoError(t, p.SetBookAuthors(np.ID, abCredits(np.ID, other, fx.author)))
	fx.nonPrimary = np.ID

	tr := abMakeBook(t, p, "Knightfall", fx.author)
	tr.MarkedForDeletion = &yes
	_, err = p.UpdateBook(tr.ID, tr)
	require.NoError(t, err)
	fx.trashed = tr.ID

	un := abMakeBook(t, p, "A Death in the Family", other)
	require.NoError(t, p.SetBookAuthors(un.ID, abCredits(un.ID, other)))
	fx.unrelated = un.ID
	fx.otherCol = fx.unrelated
	return fx
}

// TestGetBooksByAuthorIDCore_IndexedPebbleMatchesMemdb: the Pebble fallback,
// answering from the index, returns exactly the set memdb returns and exactly
// the set the unindexed scan returns.
func TestGetBooksByAuthorIDCore_IndexedPebbleMatchesMemdb(t *testing.T) {
	p := newAuthorBooksPebble(t)
	fx := buildABBatmanFixture(t, p)
	p.WaitForWarmup()
	require.True(t, p.IsMemReady())

	p.UseMemDB = true
	memBooks, err := p.GetBooksByAuthorIDCore(fx.author)
	require.NoError(t, err)

	p.UseMemDB = false
	defer func() { p.UseMemDB = true }()
	trusted, err := p.AuthorBooksIndexTrusted()
	require.NoError(t, err)
	require.False(t, trusted, "precondition: no reconcile in this process, so this is the scan path")
	scanBooks, err := p.GetBooksByAuthorIDCore(fx.author)
	require.NoError(t, err)

	rep, err := p.ReconcileAuthorBooksIndex(context.Background())
	require.NoError(t, err)
	require.True(t, rep.Trusted)
	indexed, _, err := p.booksByAuthorIDFromIndex(fx.author)
	require.NoError(t, err)
	idxBooks, err := p.GetBooksByAuthorIDCore(fx.author)
	require.NoError(t, err)
	require.Len(t, indexed, len(idxBooks))

	want := []string{fx.credited, fx.coAuthored}
	sort.Strings(want)
	require.Equal(t, want, abIDs(memBooks), "memdb")
	require.Equal(t, want, abIDs(scanBooks), "scan fallback")
	require.Equal(t, want, abIDs(idxBooks), "index fallback")
	require.Equal(t, scanBooks, idxBooks, "same rows in the same order")
}

// TestGetBooksByAuthorIDCore_IndexedPathDoesNotScanTheLibrary is the regression
// test for SQ-02. The old fallback unmarshalled every book_authors row and
// failed closed on any undecodable one, so a single corrupt credit row of an
// UNRELATED book broke every author's listing and proved the whole library was
// read. The indexed path touches only the author's own candidates.
func TestGetBooksByAuthorIDCore_IndexedPathDoesNotScanTheLibrary(t *testing.T) {
	p := newAuthorBooksPebble(t)
	fx := buildABBatmanFixture(t, p)
	_, err := p.ReconcileAuthorBooksIndex(context.Background())
	require.NoError(t, err)

	require.NoError(t, p.db.Set(bookAuthorsKey(fx.unrelated), []byte("{not json"), pebble.Sync))

	// The authoritative scan (still used by merges and deletes) reads every
	// credit row, so it fails on the corrupt one. This is what the listing used
	// to do.
	_, err = p.bookIDsInAuthorJunction(fx.author)
	require.Error(t, err)

	p.UseMemDB = false
	defer func() { p.UseMemDB = true }()
	got, err := p.GetBooksByAuthorIDCore(fx.author)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{fx.credited, fx.coAuthored}, abIDs(got))
}

// TestGetBooksByAuthorIDCore_UntrustedIndexKeepsScanning: before this
// process's reconcile the reader must not use a partial index.
func TestGetBooksByAuthorIDCore_UntrustedIndexKeepsScanning(t *testing.T) {
	p := newAuthorBooksPebble(t)
	fx := buildABBatmanFixture(t, p)
	// Wipe the index so an index read would return nothing.
	require.NoError(t, p.db.DeleteRange([]byte(authorBooksPrefix), prefixEnd([]byte(authorBooksPrefix)), pebble.Sync))
	p.UseMemDB = false
	defer func() { p.UseMemDB = true }()
	got, err := p.GetBooksByAuthorIDCore(fx.author)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{fx.credited, fx.coAuthored}, abIDs(got))
}

// TestReconcileAuthorBooksIndex_BuildsFromExistingRows: the backfill builds the
// index from rows that predate it, is idempotent, and repairs a lost key on a
// later boot.
func TestReconcileAuthorBooksIndex_BuildsFromExistingRows(t *testing.T) {
	p := newAuthorBooksPebble(t)
	fx := buildABBatmanFixture(t, p)
	want := abTruthKeys(t, p)
	require.NotEmpty(t, want)

	// Rows that predate the index: no keys.
	require.NoError(t, p.db.DeleteRange([]byte(authorBooksPrefix), prefixEnd([]byte(authorBooksPrefix)), pebble.Sync))
	require.Empty(t, abIndexKeys(t, p))

	rep, err := p.ReconcileAuthorBooksIndex(context.Background())
	require.NoError(t, err)
	require.False(t, rep.WasTrusted)
	require.True(t, rep.Trusted)
	require.Equal(t, len(want), rep.Written)
	require.Equal(t, want, abIndexKeys(t, p))

	// Idempotent: a second run writes nothing.
	rep, err = p.ReconcileAuthorBooksIndex(context.Background())
	require.NoError(t, err)
	require.True(t, rep.WasTrusted)
	require.Zero(t, rep.Written)
	require.Equal(t, want, abIndexKeys(t, p))

	// Rollback self-heal: a key an older binary never wrote is restored.
	require.NoError(t, p.db.Delete(authorBooksKey(fx.author, fx.coAuthored), pebble.Sync))
	rep, err = p.ReconcileAuthorBooksIndex(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, rep.Written)
	require.Equal(t, want, abIndexKeys(t, p))
}

func TestReconcileAuthorBooksIndex_UndecodableCreditRowHoldsTrust(t *testing.T) {
	p := newAuthorBooksPebble(t)
	fx := buildABBatmanFixture(t, p)
	require.NoError(t, p.db.Set(bookAuthorsKey(fx.unrelated), []byte("{not json"), pebble.Sync))

	rep, err := p.ReconcileAuthorBooksIndex(context.Background())
	require.Error(t, err)
	require.Equal(t, 1, rep.UnreadableCredits)
	require.Zero(t, rep.UncoveredBooks, "an undecodable credit row is counted once, by the credit pass")
	require.False(t, rep.Trusted)
	trusted, err := p.AuthorBooksIndexTrusted()
	require.NoError(t, err)
	require.False(t, trusted)
}

// A process that trusted the index stops trusting it when a later reconcile
// finds an undecodable credit row: readers have to go back to the fail-closed
// scan. Repairing the row and reconciling again restores trust.
func TestReconcileAuthorBooksIndex_UnreadableCreditRevokesTrust(t *testing.T) {
	p := newAuthorBooksPebble(t)
	fx := buildABBatmanFixture(t, p)
	rep, err := p.ReconcileAuthorBooksIndex(context.Background())
	require.NoError(t, err)
	require.True(t, rep.Trusted)
	_, trusted, err := p.booksByAuthorIDFromIndex(fx.author)
	require.NoError(t, err)
	require.True(t, trusted)

	require.NoError(t, p.db.Set(bookAuthorsKey(fx.unrelated), []byte("{not json"), pebble.Sync))
	rep, err = p.ReconcileAuthorBooksIndex(context.Background())
	require.Error(t, err)
	require.Equal(t, 1, rep.UnreadableCredits)
	require.True(t, rep.WasTrusted)
	require.False(t, rep.Trusted)
	got, err := p.AuthorBooksIndexTrusted()
	require.NoError(t, err)
	require.False(t, got)
	_, trusted, err = p.booksByAuthorIDFromIndex(fx.author)
	require.NoError(t, err)
	require.False(t, trusted, "the reader must be back on the scan")

	require.NoError(t, p.SetBookAuthors(fx.unrelated, abCredits(fx.unrelated, fx.other)))
	rep, err = p.ReconcileAuthorBooksIndex(context.Background())
	require.NoError(t, err)
	require.True(t, rep.Trusted)
	_, trusted, err = p.booksByAuthorIDFromIndex(fx.author)
	require.NoError(t, err)
	require.True(t, trusted)
}

// TestReconcileAuthorBooksIndex_UncoveredAuthorIDHoldsTrust: the index cannot
// find a listable book linked only through Book.AuthorID, which the scan and
// memdb list, so such a book holds trust (both shapes: no credit row, and a
// credit row naming someone else). Trashed and non-primary uncovered books do
// not: no listing returns them. Crediting the author restores trust and the
// index then lists the book.
func TestReconcileAuthorBooksIndex_UncoveredAuthorIDHoldsTrust(t *testing.T) {
	for _, tc := range []struct {
		name    string
		credits func(fx abBatmanFixture, id string) []BookAuthor
	}{
		{"no credit row", func(abBatmanFixture, string) []BookAuthor { return nil }},
		{"credit row names another author", func(fx abBatmanFixture, id string) []BookAuthor { return abCredits(id, fx.other) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newAuthorBooksPebble(t)
			fx := buildABBatmanFixture(t, p)
			yes, no := true, false

			// Uncovered but not listable: never holds trust.
			np := abMakeBook(t, p, "Batman: Year Two", fx.author)
			np.IsPrimaryVersion = &no
			_, err := p.UpdateBook(np.ID, np)
			require.NoError(t, err)
			tr := abMakeBook(t, p, "Batman: Venom", fx.author)
			tr.MarkedForDeletion = &yes
			_, err = p.UpdateBook(tr.ID, tr)
			require.NoError(t, err)
			rep, err := p.ReconcileAuthorBooksIndex(context.Background())
			require.NoError(t, err)
			require.True(t, rep.Trusted, "trashed and non-primary uncovered books do not hold trust")
			require.Zero(t, rep.UncoveredBooks)

			// Uncovered and listable: holds trust. The write that uncovers the
			// book already revoked it (pebble_store_author_books_trust.go), and
			// the reconcile does not grant it back.
			hush := abMakeBook(t, p, "Batman: Hush Returns", fx.author)
			if c := tc.credits(fx, hush.ID); c != nil {
				require.NoError(t, p.SetBookAuthors(hush.ID, c))
			}
			rep, err = p.ReconcileAuthorBooksIndex(context.Background())
			require.Error(t, err)
			require.ErrorContains(t, err, "credits-backfill")
			require.False(t, rep.WasTrusted, "the uncredited create revoked it")
			require.False(t, rep.Trusted)
			require.Equal(t, 1, rep.UncoveredBooks)
			require.Equal(t, []string{"book:" + hush.ID}, rep.SampleUncovered)
			_, trusted, err := p.booksByAuthorIDFromIndex(fx.author)
			require.NoError(t, err)
			require.False(t, trusted)

			p.UseMemDB = false
			defer func() { p.UseMemDB = true }()
			got, err := p.GetBooksByAuthorIDCore(fx.author)
			require.NoError(t, err)
			require.Contains(t, abIDs(got), hush.ID, "the scan lists the AuthorID-only book")

			require.NoError(t, p.SetBookAuthors(hush.ID, abCredits(hush.ID, fx.author)))
			rep, err = p.ReconcileAuthorBooksIndex(context.Background())
			require.NoError(t, err)
			require.True(t, rep.Trusted)
			indexed, trusted, err := p.booksByAuthorIDFromIndex(fx.author)
			require.NoError(t, err)
			require.True(t, trusted)
			require.Contains(t, abIDs(coresOf(indexed)), hush.ID)
		})
	}
}

func coresOf(books []Book) []BookCore {
	out := make([]BookCore, len(books))
	for i := range books {
		out[i] = books[i].Core()
	}
	return out
}

// TestAuthorBooksIndex_FreshProcessScansUntilReconcile: trust is never carried
// across a restart. Writers outside the store set an AuthorID without a credit,
// so a check made by an earlier boot says nothing about the writes since; a new
// process scans until its own reconcile finishes.
func TestAuthorBooksIndex_FreshProcessScansUntilReconcile(t *testing.T) {
	p := newAuthorBooksPebble(t)
	fx := buildABBatmanFixture(t, p)
	_, err := p.ReconcileAuthorBooksIndex(context.Background())
	require.NoError(t, err)

	// A restart, and since the last boot a writer set an AuthorID it did not
	// credit.
	p.authorBooksVerifiedThisBoot.Store(false)
	drift := abMakeBook(t, p, "Batman: The Cult", fx.author)

	trusted, err := p.AuthorBooksIndexTrusted()
	require.NoError(t, err)
	require.False(t, trusted)
	_, indexUsed, err := p.booksByAuthorIDFromIndex(fx.author)
	require.NoError(t, err)
	require.False(t, indexUsed, "the reader must scan, so the uncredited book cannot be hidden")
	p.UseMemDB = false
	defer func() { p.UseMemDB = true }()
	got, err := p.GetBooksByAuthorIDCore(fx.author)
	require.NoError(t, err)
	require.Contains(t, abIDs(got), drift.ID)

	rep, err := p.ReconcileAuthorBooksIndex(context.Background())
	require.Error(t, err)
	require.False(t, rep.WasTrusted)
	require.Equal(t, 1, rep.UncoveredBooks)
	trusted, err = p.AuthorBooksIndexTrusted()
	require.NoError(t, err)
	require.False(t, trusted)
}

// TestReconcileAuthorBooksIndex_DeletesTheV1Sentinel: a build before F5-18j
// trusted its own v1 sentinel without a reconcile. Once this build has run the
// legacy family is no longer maintained, so the sentinel must go: a rolled-back
// binary then scans until its boot reconcile has rebuilt the family. The
// delete happens even when the run does not trust the index.
func TestReconcileAuthorBooksIndex_DeletesTheV1Sentinel(t *testing.T) {
	for _, clean := range []bool{true, false} {
		t.Run(fmt.Sprintf("clean=%t", clean), func(t *testing.T) {
			p := newAuthorBooksPebble(t)
			fx := buildABBatmanFixture(t, p)
			if !clean {
				abMakeBook(t, p, "Batman: The Black Mirror", fx.author)
			}
			require.NoError(t, p.db.Set([]byte(authorBooksIndexSentinelV1), []byte("rev-old"), pebble.Sync))
			rep, err := p.ReconcileAuthorBooksIndex(context.Background())
			require.Equal(t, clean, err == nil)
			require.Equal(t, clean, rep.Trusted)
			_, closer, err := p.db.Get([]byte(authorBooksIndexSentinelV1))
			if err == nil {
				closer.Close()
			}
			require.ErrorIs(t, err, pebble.ErrNotFound, "the v1 sentinel must be deleted")
		})
	}
}

// TestReconcileAuthorBooksIndex_WritesNoLegacyRow: the reconcile used to
// rebuild the Book.AuthorID family on every boot. It reads the book rows now
// only to check coverage, and leaves rows an older build wrote alone (the
// retirement op deletes them).
func TestReconcileAuthorBooksIndex_WritesNoLegacyRow(t *testing.T) {
	p := newAuthorBooksPebble(t)
	fx := buildABBatmanFixture(t, p)
	old := abLegacyKey(fx.author, fx.trashed)
	require.NoError(t, p.db.Set(old, []byte{}, pebble.Sync))
	_, err := p.ReconcileAuthorBooksIndex(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{string(old)}, abLegacyKeys(t, p))
}

// TestAuthorBooksBounds_AuthorOneNeverLeaksIntoAuthorTen pins the scan range.
func TestAuthorBooksBounds_AuthorOneNeverLeaksIntoAuthorTen(t *testing.T) {
	lower, upper := authorBooksBounds(authorBooksPrefix, 1)
	require.Equal(t, "author_books:1:", string(lower))
	require.Equal(t, "author_books:1;", string(upper))

	p := newAuthorBooksPebble(t)
	for _, k := range [][]byte{
		authorBooksKey(1, "BOOK-A"), authorBooksKey(1, "BOOK:WITH:COLONS"),
		authorBooksKey(10, "BOOK-B"), authorBooksKey(100, "BOOK-C"), authorBooksKey(11, "BOOK-D"),
	} {
		require.NoError(t, p.db.Set(k, []byte{}, pebble.Sync))
	}
	var got []string
	require.NoError(t, forEachKeyInRange(p.db, lower, upper, func(k, _ []byte) error {
		got = append(got, string(k[len(lower):]))
		return nil
	}))
	require.Equal(t, []string{"BOOK-A", "BOOK:WITH:COLONS"}, got)

	// And the family never reaches the retired legacy rows (still stored until
	// the retirement op runs) or the plain author: rows.
	ll, lu := authorBooksBounds(authorBooksLegacyPrefix, 1)
	require.NoError(t, p.db.Set(abLegacyKey(1, "BOOK-E"), []byte{}, pebble.Sync))
	got = nil
	require.NoError(t, forEachKeyInRange(p.db, lower, upper, func(k, _ []byte) error {
		got = append(got, string(k))
		return nil
	}))
	require.NotContains(t, strings.Join(got, ","), "legacy")
	got = nil
	require.NoError(t, forEachKeyInRange(p.db, ll, lu, func(k, _ []byte) error {
		got = append(got, string(k[len(ll):]))
		return nil
	}))
	require.Equal(t, []string{"BOOK-E"}, got)
}

// TestGetBooksByAuthorIDCore_AuthorOneDoesNotSeeAuthorTen is the end-to-end
// version of the bound test: real author ids 1 and 10, one book each.
func TestGetBooksByAuthorIDCore_AuthorOneDoesNotSeeAuthorTen(t *testing.T) {
	p := newAuthorBooksPebble(t)
	ids := make([]int, 0, 11)
	for i := 0; i < 11; i++ {
		ids = append(ids, abMakeAuthor(t, p, fmt.Sprintf("Robin %02d", i)))
	}
	var one, ten int
	for _, id := range ids {
		switch id {
		case 1:
			one = id
		case 10:
			ten = id
		}
	}
	require.NotZero(t, one, "author id 1 must exist")
	require.NotZero(t, ten, "author id 10 must exist")
	b1 := abMakeBook(t, p, "Batman Incorporated", one)
	require.NoError(t, p.SetBookAuthors(b1.ID, abCredits(b1.ID, one)))
	b10 := abMakeBook(t, p, "Batman Eternal", ten)
	require.NoError(t, p.SetBookAuthors(b10.ID, abCredits(b10.ID, ten)))

	rep, err := p.ReconcileAuthorBooksIndex(context.Background())
	require.NoError(t, err)
	require.True(t, rep.Trusted, "the index path is the one under test")
	p.UseMemDB = false
	defer func() { p.UseMemDB = true }()
	got, err := p.GetBooksByAuthorIDCore(one)
	require.NoError(t, err)
	require.Equal(t, []string{b1.ID}, abIDs(got))
	got, err = p.GetBooksByAuthorIDCore(ten)
	require.NoError(t, err)
	require.Equal(t, []string{b10.ID}, abIDs(got))
}
