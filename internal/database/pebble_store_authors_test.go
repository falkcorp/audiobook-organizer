// file: internal/database/pebble_store_authors_test.go
// version: 1.0.0
// guid: 57e95a96-18e4-4bef-afd7-e33a56e37e98
// last-edited: 2026-08-23

package database

import (
	"encoding/json"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"
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
