// file: internal/database/junction_bookid_regression_test.go
// version: 1.0.0
// guid: fca556d0-2e10-4ec6-bdbc-2adca286b893
// last-edited: 2026-09-12

package database

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// These regressions deliberately use only symbols that existed before the
// junction book_id fix, so the file can be dropped onto the unfixed tree to
// show it failing there.
//
// The defect: SetBookNarrators (and SetBookAuthors) wrote caller rows to Pebble
// verbatim. A row with no book_id is rejected by memdb's {BookID, NarratorID}
// primary index -- and because every UpsertBookToMemDB reloads the junction
// from Pebble, EVERY later update of the book then aborted its whole memdb
// transaction. The symptom a user sees is the last assertion helper below:
// the book's own row in memdb stops following its updates.

// rawJunctionRows reads a junction value straight from Pebble. The getters
// cannot be used for this: since the fix they stamp the key's ID on read, so
// they would report a stamped row whether or not the stored bytes were fixed.
func rawJunctionRows[T any](t *testing.T, s *PebbleStore, prefix, bookID string) []T {
	t.Helper()
	val, closer, err := s.db.Get([]byte(prefix + bookID))
	require.NoError(t, err)
	defer closer.Close()
	var rows []T
	require.NoError(t, json.Unmarshal(val, &rows))
	return rows
}

// memBookNarratorRows is the narrator twin of memBookAuthorRows.
func memBookNarratorRows(t *testing.T, s *PebbleStore) []*BookNarrator {
	t.Helper()
	m := s.mem()
	require.NotNil(t, m, "memdb must be warm for these tests to mean anything")
	txn := m.db.Txn(false)
	defer txn.Abort()
	it, err := txn.Get(memTableBookNarrators, memIdxID)
	require.NoError(t, err)
	var out []*BookNarrator
	for obj := it.Next(); obj != nil; obj = it.Next() {
		out = append(out, obj.(*BookNarrator))
	}
	return out
}

// requireUpdateReachesMemDB renames the book through UpdateBook and reads it
// back through GetAllBooksCore, which answers from memdb when it is warm. If
// the book's memdb upsert aborted, the old title comes back.
func requireUpdateReachesMemDB(t *testing.T, s *PebbleStore, bookID, newTitle string) {
	t.Helper()
	full, err := s.GetBookByID(bookID)
	require.NoError(t, err)
	require.NotNil(t, full)
	full.Title = newTitle
	_, err = s.UpdateBook(bookID, full)
	require.NoError(t, err)

	require.NotNil(t, s.mem(), "GetAllBooksCore must be answered by memdb for this check to mean anything")
	cores, err := s.GetAllBooksCore(0, 0)
	require.NoError(t, err)
	for _, c := range cores {
		if c.ID == bookID {
			require.Equal(t, newTitle, c.Title,
				"memdb still holds the old title: the book's memdb upsert aborted on its junction rows")
			return
		}
	}
	t.Fatalf("book %s missing from memdb GetAllBooksCore", bookID)
}

// TestSetBookNarrators_RowWithoutBookIDKeepsMemDBLive is the narrator-split
// shape POST /operations/optimize-database produced, and the shape PUT
// /audiobooks/:id/narrators accepts from a client that omits book_id.
func TestSetBookNarrators_RowWithoutBookIDKeepsMemDBLive(t *testing.T) {
	store := seedAuthorRefStore(t, t.TempDir())
	book := mkAuthorRefBook(t, store, "NarratorSplitSource", 0, true, false)
	narrator, err := store.CreateNarrator("Narrator Without BookID")
	require.NoError(t, err)

	require.NoError(t, store.SetBookNarrators(book.ID, []BookNarrator{
		{NarratorID: narrator.ID, Role: "narrator"}, // BookID deliberately omitted
	}))

	requireUpdateReachesMemDB(t, store, book.ID, "NarratorSplitSource (renamed)")

	raw := rawJunctionRows[BookNarrator](t, store, "book_narrators:", book.ID)
	require.Len(t, raw, 1)
	require.Equal(t, book.ID, raw[0].BookID, "Pebble must store the row under its book's ID")

	rows := memBookNarratorRows(t, store)
	require.Len(t, rows, 1)
	require.Equal(t, book.ID, rows[0].BookID)
	require.Equal(t, narrator.ID, rows[0].NarratorID)
	require.Empty(t, store.mem().LostRows(), "no memdb insert may have been rejected")
}

// TestSetBookAuthors_RowWithoutBookIDKeepsMemDBLive is the author twin. The
// earlier fix backfilled BookID on the memdb side only, so the memdb row was
// admitted while Pebble kept the empty book_id -- and the next UpdateBook,
// reloading from Pebble, aborted exactly like the narrator case.
func TestSetBookAuthors_RowWithoutBookIDKeepsMemDBLive(t *testing.T) {
	store := seedAuthorRefStore(t, t.TempDir())
	book := mkAuthorRefBook(t, store, "AuthorSplitSource", 0, true, false)
	author, err := store.CreateAuthor("Author Without BookID")
	require.NoError(t, err)

	require.NoError(t, store.SetBookAuthors(book.ID, []BookAuthor{
		{AuthorID: author.ID, Role: "author"},
	}))

	requireUpdateReachesMemDB(t, store, book.ID, "AuthorSplitSource (renamed)")

	raw := rawJunctionRows[BookAuthor](t, store, "book_authors:", book.ID)
	require.Len(t, raw, 1)
	require.Equal(t, book.ID, raw[0].BookID, "Pebble must store the row under its book's ID")
	require.Empty(t, store.mem().LostRows(), "no memdb insert may have been rejected")
}
