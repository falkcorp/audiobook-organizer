// file: internal/database/book_search_rows_test.go
// version: 1.0.0
// guid: 27ccd95d-06c5-483b-8a77-55668bb76d58
// last-edited: 2026-10-30

package database

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestListBookSearchRows_MemDBAndPebbleAgree holds the two implementations of
// the projection to the same answer on one fixture covering every join shape:
// a resolvable author, no author, an AuthorID with no author row, and a
// soft-deleted book. Asserting each path against its own hardcoded expectation
// would let them drift apart; the same fixture and the same expected rows is
// what pins them together.
func TestListBookSearchRows_MemDBAndPebbleAgree(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	p, ok := store.(*PebbleStore)
	require.True(t, ok)

	wayne, err := store.CreateAuthor("Bruce WAYNE")
	require.NoError(t, err)
	dangling := 987654

	withAuthor, err := store.CreateBook(&Book{Title: "Year One", FilePath: "/lib/yo.m4b", AuthorID: &wayne.ID})
	require.NoError(t, err)
	noAuthor, err := store.CreateBook(&Book{Title: "Hush", FilePath: "/lib/hush.m4b"})
	require.NoError(t, err)
	danglingAuthor, err := store.CreateBook(&Book{Title: "Endgame", FilePath: "/lib/eg.m4b", AuthorID: &dangling})
	require.NoError(t, err)
	trashed, err := store.CreateBook(&Book{Title: "Knightfall", FilePath: "/lib/kf.m4b", AuthorID: &wayne.ID})
	require.NoError(t, err)
	yes := true
	trashed.MarkedForDeletion = &yes
	_, err = store.UpdateBook(trashed.ID, trashed)
	require.NoError(t, err)

	want := map[string]BookSearchRow{
		withAuthor.ID:     {ID: withAuthor.ID, Title: "Year One", FilePath: "/lib/yo.m4b", AuthorName: "Bruce WAYNE"},
		noAuthor.ID:       {ID: noAuthor.ID, Title: "Hush", FilePath: "/lib/hush.m4b"},
		danglingAuthor.ID: {ID: danglingAuthor.ID, Title: "Endgame", FilePath: "/lib/eg.m4b"},
	}

	for _, useMemDB := range []bool{true, false} {
		p.UseMemDB = useMemDB
		label := "pebble-scan"
		if useMemDB {
			label = "memdb"
		}
		t.Run(label, func(t *testing.T) {
			rows, err := store.ListBookSearchRows()
			require.NoError(t, err)
			got := make(map[string]BookSearchRow, len(rows))
			for _, r := range rows {
				got[r.ID] = r
			}
			require.Equal(t, want, got,
				"projection must carry the author name in stored casing, an empty name for no or dangling author, and omit soft-deleted books")
		})
	}
}
