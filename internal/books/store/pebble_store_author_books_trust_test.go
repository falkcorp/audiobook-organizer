// file: internal/books/store/pebble_store_author_books_trust_test.go
// version: 0.1.0
// guid: 9a4d2f71-0c3b-4e86-b5d9-6e72a1c8f043
// last-edited: 2026-11-20

package store

import (
	"context"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"
)

// abTrustedStore is a store whose reconcile has just trusted the index: Bruce
// Wayne is credited on Batman: Year One, Selina Kyle exists with no books.
func abTrustedStore(t *testing.T) (p *PebbleStore, bruce, selina int, yearOne *Book) {
	t.Helper()
	p = newAuthorBooksPebble(t)
	bruce, selina = abMakeAuthor(t, p, "Bruce Wayne"), abMakeAuthor(t, p, "Selina Kyle")
	yearOne = abMakeBook(t, p, "Batman: Year One", bruce)
	require.NoError(t, p.SetBookAuthors(yearOne.ID, abCredits(yearOne.ID, bruce)))
	abRequireTrusted(t, p)
	return p, bruce, selina, yearOne
}

// abRequireTrusted reconciles and requires a clean, trusted result.
func abRequireTrusted(t *testing.T, p *PebbleStore) {
	t.Helper()
	rep, err := p.ReconcileAuthorBooksIndex(context.Background())
	require.NoError(t, err)
	require.True(t, rep.Trusted)
}

func abTrusted(t *testing.T, p *PebbleStore) bool {
	t.Helper()
	trusted, err := p.AuthorBooksIndexTrusted()
	require.NoError(t, err)
	return trusted
}

// abRequireListingMatchesScan requires the UseMemDB=false listing (the index
// when trusted, else the scan) to equal both the full scan
// (getBooksByAuthorIDFull) and what memdb lists, which links Book.AuthorID as
// well as the credit row. Callers run it before asserting on the trust flag,
// so a missed revoke fails here on the listing itself.
func abRequireListingMatchesScan(t *testing.T, p *PebbleStore, authorID int, want ...string) {
	t.Helper()
	mem, err := p.GetBooksByAuthorIDCore(authorID)
	require.NoError(t, err)
	require.ElementsMatch(t, want, abIDs(mem), "precondition: memdb lists the book through its AuthorID")
	p.UseMemDB = false
	defer func() { p.UseMemDB = true }()
	got, err := p.GetBooksByAuthorIDCore(authorID)
	require.NoError(t, err)
	require.ElementsMatch(t, abIDs(mem), abIDs(got), "the UseMemDB=false listing drops a book memdb lists")
	scan, err := p.getBooksByAuthorIDFull(authorID)
	require.NoError(t, err)
	scanIDs := make([]string, 0, len(scan))
	for _, b := range scan {
		scanIDs = append(scanIDs, b.ID)
	}
	require.ElementsMatch(t, scanIDs, abIDs(got), "the UseMemDB=false listing differs from the scan")
}

// TestAuthorBooksTrust_BookWriteThatUncoversRevokes: after a clean reconcile,
// every book-row write that leaves a listable book with an AuthorID its credit
// row lacks revokes the trust, and the Pebble listing then lists the book.
func TestAuthorBooksTrust_BookWriteThatUncoversRevokes(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name  string
		write func(t *testing.T, p *PebbleStore, selina int, yearOne *Book) string
	}{
		{"UpdateBook moves the AuthorID off the credits", func(t *testing.T, p *PebbleStore, selina int, yearOne *Book) string {
			b, err := p.GetBookByID(yearOne.ID)
			require.NoError(t, err)
			b.AuthorID = &selina
			_, err = p.UpdateBook(b.ID, b)
			require.NoError(t, err)
			return b.ID
		}},
		{"ModifyBook moves the AuthorID off the credits", func(t *testing.T, p *PebbleStore, selina int, yearOne *Book) string {
			_, err := p.ModifyBook(yearOne.ID, func(b *Book) error { b.AuthorID = &selina; return nil })
			require.NoError(t, err)
			return yearOne.ID
		}},
		{"ModifyBookCredits moves the AuthorID off the list it writes", func(t *testing.T, p *PebbleStore, selina int, yearOne *Book) string {
			_, _, err := p.ModifyBookCredits(yearOne.ID, func(b *Book, _ *BookCreditsEdit) error { b.AuthorID = &selina; return nil })
			require.NoError(t, err)
			return yearOne.ID
		}},
		{"CreateBook with an uncredited AuthorID", func(t *testing.T, p *PebbleStore, selina int, _ *Book) string {
			return abMakeBook(t, p, "Batman: The Long Halloween", selina).ID
		}},
		{"a trashed uncredited book restored", func(t *testing.T, p *PebbleStore, selina int, _ *Book) string {
			b := abMakeBook(t, p, "Batman: Dark Victory", selina)
			b.MarkedForDeletion = &yes
			_, err := p.UpdateBook(b.ID, b)
			require.NoError(t, err)
			abRequireTrusted(t, p) // trashed: not listable, so trusted
			b.MarkedForDeletion = &no
			_, err = p.UpdateBook(b.ID, b)
			require.NoError(t, err)
			return b.ID
		}},
		{"a non-primary uncredited book made primary", func(t *testing.T, p *PebbleStore, selina int, _ *Book) string {
			b := abMakeBook(t, p, "Batman: Haunted Knight", selina)
			b.IsPrimaryVersion = &no
			_, err := p.UpdateBook(b.ID, b)
			require.NoError(t, err)
			abRequireTrusted(t, p)
			b.IsPrimaryVersion = &yes
			_, err = p.UpdateBook(b.ID, b)
			require.NoError(t, err)
			return b.ID
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _, selina, yearOne := abTrustedStore(t)
			id := tc.write(t, p, selina, yearOne)
			abRequireListingMatchesScan(t, p, selina, id)
			require.False(t, abTrusted(t, p))
		})
	}
}

// TestAuthorBooksTrust_BookWriteThatKeepsCoverageKeepsTrust: writes that cannot
// uncover a book cost no revocation, so the index keeps serving.
func TestAuthorBooksTrust_BookWriteThatKeepsCoverageKeepsTrust(t *testing.T) {
	yes := true
	p, bruce, selina, yearOne := abTrustedStore(t)

	b, err := p.GetBookByID(yearOne.ID)
	require.NoError(t, err)
	b.Title = "Batman: Year One (Deluxe)"
	_, err = p.UpdateBook(b.ID, b)
	require.NoError(t, err)
	require.True(t, abTrusted(t, p), "a write that leaves the AuthorID alone")

	abMakeBook(t, p, "Batman: Arkham Asylum", 0)
	require.True(t, abTrusted(t, p), "a create without an AuthorID")

	tr := &Book{Title: "Batman: Knightfall", FilePath: "/lib/batman/knightfall", AuthorID: &selina, MarkedForDeletion: &yes}
	_, err = p.CreateBook(tr)
	require.NoError(t, err)
	require.True(t, abTrusted(t, p), "a trashed create is not listable")

	_, _, err = p.ModifyBookCredits(yearOne.ID, func(bk *Book, c *BookCreditsEdit) error {
		bk.AuthorID = &selina
		c.Authors = abCredits(bk.ID, bruce, selina)
		return nil
	})
	require.NoError(t, err)
	require.True(t, abTrusted(t, p), "the AuthorID moves onto an author the same batch credits")
}

// TestAuthorBooksTrust_CreditRowDroppingAnAuthorRevokes: every credit writer
// that removes an author id revokes; one that only adds keeps the trust.
func TestAuthorBooksTrust_CreditRowDroppingAnAuthorRevokes(t *testing.T) {
	for _, tc := range []struct {
		name string
		drop func(t *testing.T, p *PebbleStore, yearOne *Book, bruce, selina int)
	}{
		{"SetBookAuthors", func(t *testing.T, p *PebbleStore, yearOne *Book, _, selina int) {
			require.NoError(t, p.SetBookAuthors(yearOne.ID, abCredits(yearOne.ID, selina)))
		}},
		{"ModifyBookAuthors", func(t *testing.T, p *PebbleStore, yearOne *Book, _, _ int) {
			_, err := p.ModifyBookAuthors(yearOne.ID, func([]BookAuthor) ([]BookAuthor, error) { return nil, nil })
			require.NoError(t, err)
		}},
		{"ModifyBookCredits", func(t *testing.T, p *PebbleStore, yearOne *Book, _, selina int) {
			_, _, err := p.ModifyBookCredits(yearOne.ID, func(_ *Book, c *BookCreditsEdit) error {
				c.Authors = abCredits(yearOne.ID, selina)
				return nil
			})
			require.NoError(t, err)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, bruce, selina, yearOne := abTrustedStore(t)
			require.NoError(t, p.SetBookAuthors(yearOne.ID, abCredits(yearOne.ID, bruce, selina)))
			require.True(t, abTrusted(t, p), "adding an author keeps the trust")

			tc.drop(t, p, yearOne, bruce, selina)
			abRequireListingMatchesScan(t, p, bruce, yearOne.ID)
			require.False(t, abTrusted(t, p))
		})
	}
}

// TestAuthorBooksTrust_WriteDuringReconcileWithholdsTrust: a revoking write
// that lands while the reconcile runs, after its scans read the rows, leaves
// the trust unset even though the scans found nothing wrong.
func TestAuthorBooksTrust_WriteDuringReconcileWithholdsTrust(t *testing.T) {
	p, _, selina, yearOne := abTrustedStore(t)

	var hookRan bool
	authorBooksBeforeTrustHook = func() {
		hookRan = true
		_, err := p.ModifyBook(yearOne.ID, func(b *Book) error { b.AuthorID = &selina; return nil })
		require.NoError(t, err)
	}
	defer func() { authorBooksBeforeTrustHook = nil }()

	rep, err := p.ReconcileAuthorBooksIndex(context.Background())
	require.NoError(t, err)
	require.True(t, hookRan)
	require.Zero(t, rep.UncoveredBooks, "the scans ran before the write")
	require.False(t, rep.Trusted)
	require.True(t, rep.RevokedDuringRun)
	abRequireListingMatchesScan(t, p, selina, yearOne.ID)
	require.False(t, abTrusted(t, p))

	// A write during the run that uncovers nothing does not withhold it.
	authorBooksBeforeTrustHook = func() {
		_, err := p.ModifyBook(yearOne.ID, func(b *Book) error { b.Title = "Batman: Year One (Absolute)"; return nil })
		require.NoError(t, err)
	}
	require.NoError(t, p.SetBookAuthors(yearOne.ID, abCredits(yearOne.ID, selina)))
	rep, err = p.ReconcileAuthorBooksIndex(context.Background())
	require.NoError(t, err)
	require.True(t, rep.Trusted)
	require.False(t, rep.RevokedDuringRun)
}

// TestAuthorBooksTrust_UnpublishedWarmupWithdrawsTrustForGood: once the memdb
// warmup ends without publishing, reads stay on Pebble for the process, so the
// trust is withdrawn and no later reconcile grants it again.
func TestAuthorBooksTrust_UnpublishedWarmupWithdrawsTrustForGood(t *testing.T) {
	hold := make(chan struct{})
	restore := SetWarmupStartHookForTest(func() { <-hold })
	defer restore()
	p, err := NewPebbleStoreInMemory("test_pebble_" + ulid.Make().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })

	bruce := abMakeAuthor(t, p, "Bruce Wayne")
	b := abMakeBook(t, p, "Batman: The Killing Joke", bruce)
	require.NoError(t, p.SetBookAuthors(b.ID, abCredits(b.ID, bruce)))
	abRequireTrusted(t, p) // memdb not serving yet: exactly what the index covers

	require.NotNil(t, p.warmupCancel)
	p.warmupCancel() // the warmup now fails on its cancelled context
	close(hold)
	p.WaitForWarmup()
	require.Nil(t, p.memPtr.Load(), "precondition: memdb never published")
	require.False(t, abTrusted(t, p))

	rep, err := p.ReconcileAuthorBooksIndex(context.Background())
	require.NoError(t, err)
	require.Zero(t, rep.UncoveredBooks)
	require.False(t, rep.Trusted)
	require.True(t, rep.RevokedDuringRun)
	require.False(t, abTrusted(t, p))
}

// TestRunOpenRepairs_DropsTheV1AuthorBooksSentinel: the pre-F5-18j sentinel is
// gone before the store serves a write, not only once the background reconcile
// gets to it.
func TestRunOpenRepairs_DropsTheV1AuthorBooksSentinel(t *testing.T) {
	p := newAuthorBooksPebble(t)
	require.NoError(t, p.db.Set([]byte(authorBooksIndexSentinelV1), []byte("old-build"), pebble.Sync))
	require.NoError(t, p.runOpenRepairs())
	_, closer, err := p.db.Get([]byte(authorBooksIndexSentinelV1))
	if err == nil {
		closer.Close()
	}
	require.ErrorIs(t, err, pebble.ErrNotFound)
}
