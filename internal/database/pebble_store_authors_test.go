// file: internal/database/pebble_store_authors_test.go
// version: 1.1.0
// guid: 57e95a96-18e4-4bef-afd7-e33a56e37e98
// last-edited: 2026-09-12

package database

import (
	"encoding/json"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// scanBookAuthorRows reads every live book_authors:<bookID> row straight out of
// Pebble, bypassing GetBookAuthors and memdb, so the assertions below are about
// what is actually durable rather than what a cache happens to report.
func scanBookAuthorRows(t *testing.T, store Store) map[string][]BookAuthor {
	t.Helper()
	ps, ok := store.(*PebbleStore)
	require.True(t, ok, "expected a *PebbleStore")

	iter, err := ps.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book_authors:"),
		UpperBound: []byte("book_authors:~"),
	})
	require.NoError(t, err)
	defer iter.Close()

	rows := make(map[string][]BookAuthor)
	for iter.First(); iter.Valid(); iter.Next() {
		val, valErr := iter.ValueAndErr()
		require.NoError(t, valErr)
		var authors []BookAuthor
		require.NoError(t, json.Unmarshal(val, &authors))
		rows[string(iter.Key())[len("book_authors:"):]] = authors
	}
	require.NoError(t, iter.Error())
	return rows
}

// TestPebbleDeleteAuthorRemovesJunctionRows pins the three outcomes a junction
// sweep has to get right at once: the deleted author leaves no row anywhere, a
// shared book keeps its surviving co-author intact, and a book whose only
// author was deleted loses its row entirely instead of keeping an empty one.
//
// The assertion that discriminates a correct fix from a plausible wrong one is
// the co-author survival check — deleting the whole book_authors row would
// satisfy "no rows left for the deleted author" while silently orphaning every
// other author on that book.
func TestPebbleDeleteAuthorRemovesJunctionRows(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	doomed, err := store.CreateAuthor("Doomed Author")
	require.NoError(t, err)
	survivor, err := store.CreateAuthor("Surviving Co-Author")
	require.NoError(t, err)
	bystander, err := store.CreateAuthor("Unrelated Author")
	require.NoError(t, err)

	const (
		sharedBook  = "book-shared"
		soleBook    = "book-sole"
		unrelatedBk = "book-unrelated"
	)

	// A book co-authored by the doomed author and a survivor.
	require.NoError(t, store.SetBookAuthors(sharedBook, []BookAuthor{
		{BookID: sharedBook, AuthorID: doomed.ID, Role: "author", Position: 0},
		{BookID: sharedBook, AuthorID: survivor.ID, Role: "co-author", Position: 1},
	}))
	// A book whose only author is the doomed one.
	require.NoError(t, store.SetBookAuthors(soleBook, []BookAuthor{
		{BookID: soleBook, AuthorID: doomed.ID, Role: "author", Position: 0},
	}))
	// A book that must be left completely alone.
	require.NoError(t, store.SetBookAuthors(unrelatedBk, []BookAuthor{
		{BookID: unrelatedBk, AuthorID: bystander.ID, Role: "author", Position: 0},
	}))

	before := scanBookAuthorRows(t, store)
	require.Len(t, before, 3, "fixture should have written three junction rows")

	require.NoError(t, store.DeleteAuthor(doomed.ID))

	after := scanBookAuthorRows(t, store)

	// 1. No junction row anywhere still references the deleted author.
	for bookID, authors := range after {
		for _, a := range authors {
			require.NotEqual(t, doomed.ID, a.AuthorID,
				"book %s still carries a junction row for the deleted author %d", bookID, doomed.ID)
		}
	}

	// 2. The shared book keeps its co-author, with role and position intact.
	shared, ok := after[sharedBook]
	require.True(t, ok, "the shared book's junction row must survive, not be deleted wholesale")
	require.Len(t, shared, 1, "the shared book should keep exactly its surviving co-author")
	require.Equal(t, survivor.ID, shared[0].AuthorID)
	require.Equal(t, "co-author", shared[0].Role, "surviving co-author's role must be preserved")
	require.Equal(t, 1, shared[0].Position, "surviving co-author's position must be preserved")
	require.Equal(t, sharedBook, shared[0].BookID)

	// 3. The sole-author book's row is gone, not left as an empty array.
	_, stillThere := after[soleBook]
	require.False(t, stillThere,
		"a junction row whose only author was deleted must be removed, not left empty")

	// 4. The unrelated book is untouched.
	require.Equal(t, before[unrelatedBk], after[unrelatedBk],
		"an unrelated book's junction row must not be modified")

	// 5. The store's own accessor agrees with the raw keyspace.
	sharedViaAPI, err := store.GetBookAuthors(sharedBook)
	require.NoError(t, err)
	require.Len(t, sharedViaAPI, 1)
	require.Equal(t, survivor.ID, sharedViaAPI[0].AuthorID)

	soleViaAPI, err := store.GetBookAuthors(soleBook)
	require.NoError(t, err)
	require.Empty(t, soleViaAPI)
}

// TestPebbleDeleteAuthorJunctionSweepMemDB checks the memdb mirror of the
// junction rewrite. Pebble is the source of truth, but every read path that
// runs with memdb enabled answers from memdb, so a Pebble-only fix would leave
// the orphan visible to the whole query layer.
func TestPebbleDeleteAuthorJunctionSweepMemDB(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	ps, ok := store.(*PebbleStore)
	require.True(t, ok)
	if !ps.UseMemDB || ps.mem() == nil {
		t.Skip("memdb not enabled in this build/config")
	}

	doomed, err := store.CreateAuthor("MemDB Doomed")
	require.NoError(t, err)
	survivor, err := store.CreateAuthor("MemDB Survivor")
	require.NoError(t, err)

	const bookID = "book-memdb"
	require.NoError(t, store.SetBookAuthors(bookID, []BookAuthor{
		{BookID: bookID, AuthorID: doomed.ID, Role: "author", Position: 0},
		{BookID: bookID, AuthorID: survivor.ID, Role: "co-author", Position: 1},
	}))

	require.NoError(t, store.DeleteAuthor(doomed.ID))
	ps.WaitForWarmup()

	txn := ps.mem().db.Txn(false)
	defer txn.Abort()
	it, err := txn.Get(memTableBookAuthors, memIdxAuthorID, doomed.ID)
	require.NoError(t, err)
	var leftovers []string
	for obj := it.Next(); obj != nil; obj = it.Next() {
		ba, castOK := obj.(*BookAuthor)
		require.True(t, castOK)
		leftovers = append(leftovers, ba.BookID)
	}
	require.Empty(t, leftovers,
		"memdb still holds book_authors rows for the deleted author on books %v", leftovers)

	surv, err := txn.Get(memTableBookAuthors, memIdxAuthorID, survivor.ID)
	require.NoError(t, err)
	count := 0
	for obj := surv.Next(); obj != nil; obj = surv.Next() {
		count++
	}
	require.Equal(t, 1, count, "the surviving co-author's memdb row must remain")
}

// scanBookNarratorRows reads every live book_narrators:<bookID> row straight
// out of Pebble, bypassing GetBookNarrators and memdb. It uses
// prefixUpperBound rather than a "~" sentinel so non-ASCII book IDs are not
// silently excluded from the assertions.
func scanBookNarratorRows(t *testing.T, store Store) map[string][]BookNarrator {
	t.Helper()
	ps, ok := store.(*PebbleStore)
	require.True(t, ok, "expected a *PebbleStore")

	prefix := []byte("book_narrators:")
	iter, err := ps.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixUpperBound(prefix),
	})
	require.NoError(t, err)
	defer iter.Close()

	rows := make(map[string][]BookNarrator)
	for iter.First(); iter.Valid(); iter.Next() {
		val, valErr := iter.ValueAndErr()
		require.NoError(t, valErr)
		var narrators []BookNarrator
		require.NoError(t, json.Unmarshal(val, &narrators))
		rows[string(iter.Key())[len(prefix):]] = narrators
	}
	require.NoError(t, iter.Error())
	return rows
}

// TestDeleteNarrator_RemovesRecordAndIndex proves both keys go: the record
// (GetNarratorByID) and the narrator_name: index (GetNarratorByName). A delete
// that only removed the record would still pass the ID check.
func TestDeleteNarrator_RemovesRecordAndIndex(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	doomed, err := store.CreateNarrator("Doomed Narrator")
	require.NoError(t, err)
	keeper, err := store.CreateNarrator("Kept Narrator")
	require.NoError(t, err)

	require.NoError(t, store.DeleteNarrator(doomed.ID))

	byID, err := store.GetNarratorByID(doomed.ID)
	require.NoError(t, err)
	require.Nil(t, byID, "narrator record must be gone")

	byName, err := store.GetNarratorByName("Doomed Narrator")
	require.NoError(t, err)
	require.Nil(t, byName, "narrator_name index must be gone")

	ps := store.(*PebbleStore)
	_, closer, getErr := ps.db.Get([]byte("narrator_name:" + util.NormalizeAuthor("Doomed Narrator")))
	if closer != nil {
		closer.Close()
	}
	require.ErrorIs(t, getErr, pebble.ErrNotFound, "raw narrator_name key must be deleted")

	// A different narrator is untouched.
	kept, err := store.GetNarratorByName("Kept Narrator")
	require.NoError(t, err)
	require.NotNil(t, kept)
	require.Equal(t, keeper.ID, kept.ID)

	all, err := store.ListNarrators()
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.Equal(t, keeper.ID, all[0].ID)

	// Re-creating the same name after delete works and gets a fresh row.
	again, err := store.CreateNarrator("Doomed Narrator")
	require.NoError(t, err)
	require.NotEqual(t, doomed.ID, again.ID)
}

// TestDeleteNarrator_UnknownID_Idempotent pins the not-found contract to
// DeleteAuthor's: a missing id (never created, or already deleted) is nil.
func TestDeleteNarrator_UnknownID_Idempotent(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	require.NoError(t, store.DeleteNarrator(424242))
	require.NoError(t, store.DeleteAuthor(424242), "sibling contract this test mirrors")

	n, err := store.CreateNarrator("Twice Deleted")
	require.NoError(t, err)
	require.NoError(t, store.DeleteNarrator(n.ID))
	require.NoError(t, store.DeleteNarrator(n.ID))
}

// TestDeleteNarrator_RemovesJunctionRows mirrors
// TestPebbleDeleteAuthorRemovesJunctionRows: the deleted narrator leaves no
// junction entry, a shared book keeps its co-narrator intact, a sole-narrator
// book loses its row, and an unrelated book is untouched.
func TestDeleteNarrator_RemovesJunctionRows(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	doomed, err := store.CreateNarrator("Junction Doomed")
	require.NoError(t, err)
	survivor, err := store.CreateNarrator("Junction Survivor")
	require.NoError(t, err)
	bystander, err := store.CreateNarrator("Junction Bystander")
	require.NoError(t, err)

	const (
		sharedBook  = "book-shared"
		soleBook    = "book-sole"
		unrelatedBk = "book-unrelated"
	)
	require.NoError(t, store.SetBookNarrators(sharedBook, []BookNarrator{
		{BookID: sharedBook, NarratorID: doomed.ID, Role: "narrator", Position: 0},
		{BookID: sharedBook, NarratorID: survivor.ID, Role: "co-narrator", Position: 1},
	}))
	require.NoError(t, store.SetBookNarrators(soleBook, []BookNarrator{
		{BookID: soleBook, NarratorID: doomed.ID, Role: "narrator", Position: 0},
	}))
	require.NoError(t, store.SetBookNarrators(unrelatedBk, []BookNarrator{
		{BookID: unrelatedBk, NarratorID: bystander.ID, Role: "narrator", Position: 0},
	}))

	before := scanBookNarratorRows(t, store)
	require.Len(t, before, 3)

	require.NoError(t, store.DeleteNarrator(doomed.ID))

	after := scanBookNarratorRows(t, store)
	for bookID, narrators := range after {
		for _, n := range narrators {
			require.NotEqual(t, doomed.ID, n.NarratorID,
				"book %s still references deleted narrator %d", bookID, doomed.ID)
		}
	}

	shared, ok := after[sharedBook]
	require.True(t, ok, "shared book's row must survive, not be deleted wholesale")
	require.Len(t, shared, 1)
	require.Equal(t, survivor.ID, shared[0].NarratorID)
	require.Equal(t, "co-narrator", shared[0].Role)
	require.Equal(t, 1, shared[0].Position)
	require.Equal(t, sharedBook, shared[0].BookID)

	_, stillThere := after[soleBook]
	require.False(t, stillThere, "sole-narrator row must be removed, not left empty")

	require.Equal(t, before[unrelatedBk], after[unrelatedBk])

	viaAPI, err := store.GetBookNarrators(soleBook)
	require.NoError(t, err)
	require.Empty(t, viaAPI)
}

// TestDeleteNarrator_MemDBSync checks both memdb cleanups — the narrator row
// in memTableNarrators and its memTableBookNarrators junction rows — because
// they are separate code paths and every memdb-enabled reader answers from
// memdb. It also covers a surviving junction row stored with an empty BookID:
// without the sweep's backfill, the memdb replay aborts and the survivor
// vanishes from memdb.
func TestDeleteNarrator_MemDBSync(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	ps, ok := store.(*PebbleStore)
	require.True(t, ok)
	if !ps.UseMemDB || ps.mem() == nil {
		t.Skip("memdb not enabled in this build/config")
	}

	doomed, err := store.CreateNarrator("MemDB Doomed Narrator")
	require.NoError(t, err)
	survivor, err := store.CreateNarrator("MemDB Survivor Narrator")
	require.NoError(t, err)

	const bookID = "book-memdb-narr"
	// Written straight to Pebble with an empty BookID on the survivor, the way
	// an older writer could have stored it; memdb is seeded with a correct copy.
	raw, err := json.Marshal([]BookNarrator{
		{BookID: bookID, NarratorID: doomed.ID, Role: "narrator", Position: 0},
		{NarratorID: survivor.ID, Role: "co-narrator", Position: 1},
	})
	require.NoError(t, err)
	require.NoError(t, ps.db.Set([]byte("book_narrators:"+bookID), raw, pebble.Sync))
	ps.ReplaceBookNarratorsInMemDB(bookID, []BookNarrator{
		{BookID: bookID, NarratorID: doomed.ID, Role: "narrator", Position: 0},
		{BookID: bookID, NarratorID: survivor.ID, Role: "co-narrator", Position: 1},
	})
	ps.WaitForWarmup()

	require.NoError(t, store.DeleteNarrator(doomed.ID))
	ps.WaitForWarmup()

	txn := ps.mem().db.Txn(false)
	defer txn.Abort()

	gone, err := txn.First(memTableNarrators, memIdxID, doomed.ID)
	require.NoError(t, err)
	require.Nil(t, gone, "memdb still holds the deleted narrator row")

	kept, err := txn.First(memTableNarrators, memIdxID, survivor.ID)
	require.NoError(t, err)
	require.NotNil(t, kept, "surviving narrator must remain in memdb")

	it, err := txn.Get(memTableBookNarrators, memIdxNarratorID, doomed.ID)
	require.NoError(t, err)
	var leftovers []string
	for obj := it.Next(); obj != nil; obj = it.Next() {
		bn, castOK := obj.(*BookNarrator)
		require.True(t, castOK)
		leftovers = append(leftovers, bn.BookID)
	}
	require.Empty(t, leftovers, "memdb still holds book_narrators rows for the deleted narrator on %v", leftovers)

	surv, err := txn.Get(memTableBookNarrators, memIdxNarratorID, survivor.ID)
	require.NoError(t, err)
	var survBooks []string
	for obj := surv.Next(); obj != nil; obj = surv.Next() {
		survBooks = append(survBooks, obj.(*BookNarrator).BookID)
	}
	require.Equal(t, []string{bookID}, survBooks, "the surviving co-narrator's memdb row must remain")
}
