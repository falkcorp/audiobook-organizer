// file: internal/database/author_bookref_test.go
// version: 1.1.0
// guid: 53e2c4ec-167f-4096-990e-5e348ba07236
// last-edited: 2026-08-23

package database

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// These tests exist because a DISPLAY counter was used as an EXISTENCE test.
// GetAllAuthorBookCounts / GetBooksByAuthorIDCore skip trashed and non-primary
// books — correct for a badge or a listing, catastrophic for "is it safe to
// delete this author row". Every assertion below is about the difference
// between the two questions, and every fixture is built so the two counters
// DISAGREE: a fixture where they agree passes with or without the fix.

// seedAuthorRefStore builds a warm store the way production runs it.
func seedAuthorRefStore(t *testing.T, dir string) *PebbleStore {
	t.Helper()
	store, err := NewPebbleStore(dir)
	require.NoError(t, err)
	store.WaitForWarmup()
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// mkAuthorRefBook creates a book through the normal write path so both the
// memdb and Pebble see it exactly as production does. legacyAuthor may be 0 to
// leave Book.AuthorID nil.
func mkAuthorRefBook(t *testing.T, s *PebbleStore, title string, legacyAuthor int, primary, trashed bool) *Book {
	t.Helper()
	b := &Book{
		Title:             title,
		FilePath:          "/authorref/" + title,
		IsPrimaryVersion:  boolp(primary),
		MarkedForDeletion: boolp(trashed),
	}
	if legacyAuthor != 0 {
		b.AuthorID = intp(legacyAuthor)
	}
	created, err := s.CreateBook(b)
	require.NoError(t, err)
	return created
}

// TestGetAllAuthorBookRefCounts_CountsTrashedAndNonPrimary is THE bug. An
// author whose only book is in the trash, or is a non-primary duplicate
// version, is still REFERENCED — the book keeps the author_id and renders with
// a dangling author once the row is gone.
func TestGetAllAuthorBookRefCounts_CountsTrashedAndNonPrimary(t *testing.T) {
	store := seedAuthorRefStore(t, t.TempDir())

	const onlyTrashed = 8100 // every book in the trash
	const onlyNonPrim = 8101 // every book a secondary version
	const healthy = 8102     // an ordinary author
	mkAuthorRefBook(t, store, "trashed-a", onlyTrashed, true, true)
	mkAuthorRefBook(t, store, "trashed-b", onlyTrashed, true, true)
	mkAuthorRefBook(t, store, "secondary", onlyNonPrim, false, false)
	mkAuthorRefBook(t, store, "normal", healthy, true, false)

	refs, err := store.GetAllAuthorBookRefCounts()
	require.NoError(t, err)

	require.Equal(t, 2, refs[onlyTrashed],
		"an author whose books are all trashed is still REFERENCED — deleting the row strands them")
	require.Equal(t, 1, refs[onlyNonPrim],
		"a non-primary version still holds the author_id")
	require.Equal(t, 1, refs[healthy])

	// PRECONDITION, not decoration: prove the OLD instrument disagrees. Without
	// this the fixture could be one where both counters agree, and the test
	// would pass whether or not the guard is wired up.
	display, err := store.GetAllAuthorBookCounts()
	require.NoError(t, err)
	require.Zero(t, display[onlyTrashed],
		"precondition: the display counter must report 0 here, which is exactly why it must not drive deletion")
	require.Zero(t, display[onlyNonPrim], "precondition: display counter hides non-primary")
	require.Equal(t, 1, display[healthy],
		"precondition: the two counters agree on the healthy author, so the divergence above is about state, not arithmetic")
}

// TestGetAllAuthorBookRefCounts_CountsJunctionOnlyCoAuthor covers the second
// way an author is attached: authors 2..n of a credit list live ONLY in the
// book_authors junction table, and the per-author listing the delete handlers
// used to consult drops them once the book is trashed or non-primary.
func TestGetAllAuthorBookRefCounts_CountsJunctionOnlyCoAuthor(t *testing.T) {
	store := seedAuthorRefStore(t, t.TempDir())

	const primaryAuthor = 8110
	const coAuthor = 8111 // exists only as a junction row, on a TRASHED book

	b := mkAuthorRefBook(t, store, "trashed-coauthored", primaryAuthor, true, true)
	require.NoError(t, store.SetBookAuthors(b.ID, []BookAuthor{
		{BookID: b.ID, AuthorID: primaryAuthor, Role: "author", Position: 0},
		{BookID: b.ID, AuthorID: coAuthor, Role: "author", Position: 1},
	}))

	refs, err := store.GetAllAuthorBookRefCounts()
	require.NoError(t, err)
	require.Equal(t, 1, refs[coAuthor],
		"a junction-only co-author on a trashed book is still referenced")
	require.Equal(t, 1, refs[primaryAuthor])

	display, err := store.GetAllAuthorBookCounts()
	require.NoError(t, err)
	require.Zero(t, display[coAuthor], "precondition: the display counter reports 0 for this co-author")
	require.Zero(t, display[primaryAuthor], "precondition: the display counter reports 0 here too")
}

// TestGetAllAuthorBookRefCounts_JunctionWithoutLegacyAuthorStillCounts is the
// fail-open the display counter's per-BOOK dedup would reintroduce. The book
// carries junction rows that do NOT mention its own Book.AuthorID; skipping the
// whole book (which is what GetAllAuthorBookCounts does) loses the legacy
// author's reference entirely and makes a referenced author deletable.
func TestGetAllAuthorBookRefCounts_JunctionWithoutLegacyAuthorStillCounts(t *testing.T) {
	store := seedAuthorRefStore(t, t.TempDir())

	const legacyOnly = 8120 // named ONLY by Book.AuthorID
	const junctionOnly = 8121

	b := mkAuthorRefBook(t, store, "legacy-not-in-junction", legacyOnly, true, false)
	require.NoError(t, store.SetBookAuthors(b.ID, []BookAuthor{
		{BookID: b.ID, AuthorID: junctionOnly, Role: "author", Position: 1},
	}))

	refs, err := store.GetAllAuthorBookRefCounts()
	require.NoError(t, err)
	require.Equal(t, 1, refs[legacyOnly],
		"Book.AuthorID is a reference even when the junction list omits it; a per-book skip would lose it")
	require.Equal(t, 1, refs[junctionOnly])

	// PRECONDITION: the display counter really does lose it, so this test is
	// asserting a divergence rather than restating agreement.
	display, err := store.GetAllAuthorBookCounts()
	require.NoError(t, err)
	require.Zero(t, display[legacyOnly],
		"precondition: the per-book dedup in GetAllAuthorBookCounts drops the legacy author entirely")
}

// TestGetAllAuthorBookRefCounts_NoDoubleCounting — a book present in BOTH the
// junction and the legacy field for the SAME author counts once, so the guard
// refuses on real references rather than on arithmetic.
func TestGetAllAuthorBookRefCounts_NoDoubleCounting(t *testing.T) {
	store := seedAuthorRefStore(t, t.TempDir())

	const both = 8130
	b := mkAuthorRefBook(t, store, "both-links", both, true, false)
	require.NoError(t, store.SetBookAuthors(b.ID, []BookAuthor{
		{BookID: b.ID, AuthorID: both, Role: "author", Position: 0},
	}))

	refs, err := store.GetAllAuthorBookRefCounts()
	require.NoError(t, err)
	require.Equal(t, 1, refs[both],
		"one book referencing an author two ways is ONE reference, not two")
}

// TestGetAllAuthorBookRefCounts_UnreferencedAuthorsAreAbsent — the
// safe-to-delete signal is absence from the map, so absence must mean "nothing
// points here", not "no book passed a filter". This is the positive control at
// the database layer: without it, a counter that returned every author id would
// pass every assertion above.
func TestGetAllAuthorBookRefCounts_UnreferencedAuthorsAreAbsent(t *testing.T) {
	store := seedAuthorRefStore(t, t.TempDir())
	mkAuthorRefBook(t, store, "somewhere", 8140, true, false)

	refs, err := store.GetAllAuthorBookRefCounts()
	require.NoError(t, err)
	require.Equal(t, 1, refs[8140])
	_, present := refs[8141]
	require.False(t, present, "an author nothing references must be absent from the map")
}

// TestAsAuthorBookRefStore_ResolvesPebbleStore guards the capability lookup.
// Production wraps the store in the Bleve indexedStore decorator, and a bare
// type assertion against a wrapped store is indistinguishable from an
// unsupported backend — which is how several ops silently no-opped in prod.
func TestAsAuthorBookRefStore_ResolvesPebbleStore(t *testing.T) {
	store := seedAuthorRefStore(t, t.TempDir())
	// Seed a real reference the FILTERED counter cannot see, so the count
	// comparison below is about data rather than about two empty maps.
	mkAuthorRefBook(t, store, "Trashed But Still Referenced", 7, true, true)
	require.NotNil(t, AsAuthorBookRefStore(store))
	require.Nil(t, AsAuthorBookRefStore(nil))
	require.Nil(t, AsAuthorBookRefStore(struct{}{}))

	// 🔴 THE DECORATOR CASE — the one this test's name is about, and the only
	// one that fails if the lookup stops walking the chain. Asserting on a BARE
	// *PebbleStore above proves nothing about production: the bare store
	// satisfies the interface directly, so it passes even with a plain type
	// assertion. Verified by mutation: replacing AsCapability with
	// `s.(AuthorBookRefStore)` left the three assertions above GREEN.
	//
	// decoratorStore (store_capability_test.go) embeds the Store INTERFACE, so
	// only Store's method set is promoted — exactly the shape of
	// internal/server.indexedStore, the Bleve wrapper the live store always
	// carries. AuthorBookRefStore is deliberately not part of Store.
	wrapped := &decoratorStore{Store: store}
	require.NotNil(t, AsAuthorBookRefStore(wrapped),
		"capability must resolve THROUGH the Bleve-style decorator; a bare type "+
			"assertion returns nil here, which is exactly where the guard matters")

	// Counts must survive the indirection, not merely resolve to something.
	viaWrapped, err := AsAuthorBookRefStore(wrapped).GetAllAuthorBookRefCounts()
	require.NoError(t, err)
	direct, err := AsAuthorBookRefStore(store).GetAllAuthorBookRefCounts()
	require.NoError(t, err)
	require.Equal(t, direct, viaWrapped)
	require.NotEmpty(t, direct, "fixture must actually hold references, or the comparison is vacuous")

	// A decorator that has NOT opted into unwrapping stays opaque: reaching
	// around it would bypass whatever behaviour it exists to add, so the guard
	// must fail closed rather than guess.
	require.Nil(t, AsAuthorBookRefStore(&decoratorNoUnwrap{Store: store}))
}

// ---------------------------------------------------------------------------
// An UNREADABLE row must make the scan REFUSE, not undercount.
//
// Both of these fixtures write garbage directly into the keyspace with p.db.Set
// — nothing that goes through CreateBook can produce a malformed row, so the
// rest of the suite cannot reach this path at all. Undercounting is fail-OPEN
// for a delete guard: the count comes back short, the author looks unreferenced,
// and the delete strands exactly the row that could not be read. Mirrors the
// same fix made to getAllSeriesBookRefCountsPebble.
// ---------------------------------------------------------------------------

func TestGetAllAuthorBookRefCountsPebble_UndecodableJunctionRowIsFatal(t *testing.T) {
	store := seedAuthorRefStore(t, t.TempDir())
	mkAuthorRefBook(t, store, "Readable", 1, true, false)

	require.NoError(t, store.db.Set([]byte("book_authors:corrupt-book"), []byte("{not json"), nil))

	_, err := store.getAllAuthorBookRefCountsPebble()
	require.Error(t, err, "an undecodable credit list must abort the scan; authors 2..n "+
		"live ONLY in this table, so skipping the row can make a referenced author deletable")
	require.Contains(t, err.Error(), "undecodable book_authors row")
}

func TestGetAllAuthorBookRefCountsPebble_UndecodableBookRowIsFatal(t *testing.T) {
	store := seedAuthorRefStore(t, t.TempDir())
	mkAuthorRefBook(t, store, "Readable", 1, true, false)

	// The key must satisfy BOTH range conditions or the test would pass
	// vacuously by never being visited: exactly one colon (so it is not skipped
	// as a secondary index like book:path:), and a leading digit so it sorts
	// inside the book:0 .. book:; bounds. Real book IDs are ULIDs, which begin
	// with a digit for any timestamp this side of the year 10889.
	require.NoError(t, store.db.Set([]byte("book:01CORRUPTROWZZZZZZZZZZZZZZ"), []byte("{not json"), nil))

	_, err := store.getAllAuthorBookRefCountsPebble()
	require.Error(t, err, "an undecodable book row may carry a legacy author_id; "+
		"skipping it undercounts, which is fail-open for a delete guard")
	require.Contains(t, err.Error(), "undecodable book row")
}

// The corrupt row must NOT be reachable only through the fatal path — a healthy
// store must still answer. Positive control for the two tests above: without it
// they would pass against a scan that always errored.
func TestGetAllAuthorBookRefCountsPebble_HealthyStoreStillAnswers(t *testing.T) {
	store := seedAuthorRefStore(t, t.TempDir())
	mkAuthorRefBook(t, store, "Trashed", 9, true, true)

	counts, err := store.getAllAuthorBookRefCountsPebble()
	require.NoError(t, err)
	require.Equal(t, 1, counts[9], "a trashed book still references its author")
}

// TestAuthorRefCounts_SharedGuard pins the exported helper both delete paths
// use: it fails closed on a store that cannot answer, and resolves through a
// decorator on one that can.
func TestAuthorRefCounts_SharedGuard(t *testing.T) {
	store := seedAuthorRefStore(t, t.TempDir())
	mkAuthorRefBook(t, store, "Trashed", 4, true, true)

	counts, err := AuthorRefCounts(&decoratorStore{Store: store})
	require.NoError(t, err, "must resolve through the Bleve-style decorator")
	require.Equal(t, 1, counts[4])

	_, err = AuthorRefCounts(struct{}{})
	require.Error(t, err, "a store that cannot answer must make the caller refuse, never fall back")
	require.Contains(t, err.Error(), "refusing to delete from a filtered count")
}

// ── The BookID-less credit (open-findings §2) ──────────────────────────────
//
// memdb's book_authors primary index is a NON-AllowMissing compound index on
// {BookID, AuthorID}. A BookAuthor reaching it with an empty BookID makes
// go-memdb return "object missing primary index", which aborts the whole
// ReplaceBookAuthors transaction -- while SetBookAuthors, having already
// committed the rows to Pebble, returns nil.
//
// The result is a DIVERGENCE, not an error: Pebble holds the credit and memdb
// holds nothing. Since GetAllAuthorBookRefCounts prefers the memdb whenever it
// is warm (which in production is always), the guard that is supposed to stop
// a delete reads 0 for an author that is still referenced -- the exact
// fail-open the guard was written to close, arriving through a caller that
// simply forgot a field.
//
// So the assertion that matters is not "no error" (there never was one) but
// "the two backends agree". A test that only checked the memdb count would
// pass against a store that had lost the row from BOTH.

// TestSetBookAuthors_CreditWithoutBookIDStillReachesMemDB is the regression.
// The author-split op in handlers/operations built exactly this literal.
func TestSetBookAuthors_CreditWithoutBookIDStillReachesMemDB(t *testing.T) {
	store := seedAuthorRefStore(t, t.TempDir())
	book := mkAuthorRefBook(t, store, "SplitSource", 0, true, false)
	author, err := store.CreateAuthor("Credit Without BookID")
	require.NoError(t, err)

	// BookID deliberately omitted -- the shape the broken call site produced.
	require.NoError(t, store.SetBookAuthors(book.ID, []BookAuthor{
		{AuthorID: author.ID, Role: "author"},
	}))

	memCounts, err := store.GetAllAuthorBookRefCounts()
	require.NoError(t, err)
	pebCounts, err := store.getAllAuthorBookRefCountsPebble()
	require.NoError(t, err)

	require.Equal(t, 1, pebCounts[author.ID],
		"Pebble always held the credit -- if this fails the fixture is wrong, not the fix")
	require.Equal(t, 1, memCounts[author.ID],
		"memdb dropped the credit: the delete guard would report this author unreferenced")
	require.Equal(t, pebCounts[author.ID], memCounts[author.ID],
		"memdb and Pebble must not disagree about who references an author")
}

// TestSetBookAuthors_ExplicitBookIDIsUnchanged is the positive control. If the
// backfill were wrong in the other direction -- overwriting a BookID rather
// than filling an empty one -- the test above would still pass while credits
// silently reattached to the wrong book.
func TestSetBookAuthors_ExplicitBookIDIsUnchanged(t *testing.T) {
	store := seedAuthorRefStore(t, t.TempDir())
	keep := mkAuthorRefBook(t, store, "KeepsItsCredit", 0, true, false)
	other := mkAuthorRefBook(t, store, "MustNotReceiveIt", 0, true, false)
	author, err := store.CreateAuthor("Explicit BookID Author")
	require.NoError(t, err)

	// Written against `keep` while the call is made through `other`, so a
	// backfill that overwrites rather than fills would move the credit.
	require.NoError(t, store.SetBookAuthors(other.ID, []BookAuthor{
		{BookID: keep.ID, AuthorID: author.ID, Role: "author"},
	}))

	// Read the MEMDB row, not GetBookAuthors -- that reads Pebble directly and
	// never sees the backfill, so asserting on it would pass no matter what the
	// backfill did.
	rows := memBookAuthorRows(t, store)
	require.Len(t, rows, 1)
	require.Equal(t, keep.ID, rows[0].BookID,
		"an explicitly-set BookID must survive the backfill untouched")
}

// memBookAuthorRows returns every book_authors row the memdb actually holds.
func memBookAuthorRows(t *testing.T, s *PebbleStore) []*BookAuthor {
	t.Helper()
	m := s.mem()
	require.NotNil(t, m, "memdb must be warm for these tests to mean anything")
	txn := m.db.Txn(false)
	defer txn.Abort()
	it, err := txn.Get(memTableBookAuthors, memIdxID)
	require.NoError(t, err)
	var out []*BookAuthor
	for obj := it.Next(); obj != nil; obj = it.Next() {
		out = append(out, obj.(*BookAuthor))
	}
	return out
}
