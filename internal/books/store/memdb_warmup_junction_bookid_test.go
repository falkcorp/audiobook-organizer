// file: internal/books/store/memdb_warmup_junction_bookid_test.go
// version: 0.1.0
// guid: e4717d7f-dc0a-4ad5-ac02-fb5e8acb3d27
// last-edited: 2026-11-22

package store

import (
	"encoding/json"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"
)

// writeRawJunction stores a junction value exactly as a pre-fix setter would
// have, bypassing the stamp SetBookAuthors/SetBookNarrators now apply.
func writeRawJunction(t *testing.T, s *PebbleStore, prefix, bookID string, rows any) {
	t.Helper()
	data, err := json.Marshal(rows)
	require.NoError(t, err)
	require.NoError(t, s.db.Set([]byte(prefix+bookID), data, pebble.Sync))
}

// TestWarmupAdmitsJunctionRowWithoutBookID covers the restart path: warmup is
// async and races the startup migrations, so it must not depend on the rows
// having been repaired. Before the fix it rejected the row and flagged the
// table incomplete for the whole process lifetime.
func TestWarmupAdmitsJunctionRowWithoutBookID(t *testing.T) {
	dir := t.TempDir()
	first, err := NewPebbleStore(dir)
	require.NoError(t, err)
	first.WaitForWarmup()
	book := mkAuthorRefBook(t, first, "WarmupDamaged", 0, true, false)
	narrator, err := first.CreateNarrator("Warmup Narrator")
	require.NoError(t, err)
	writeRawJunction(t, first, "book_narrators:", book.ID, []BookNarrator{{NarratorID: narrator.ID}})
	require.NoError(t, first.Close())

	// Reopen the SAME on-disk directory. seedAuthorRefStore is in-memory and
	// would open an empty database here, so this reopen opens by path directly.
	store, err := NewPebbleStore(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	store.WaitForWarmup()
	require.Empty(t, store.mem().LostRows(), "warmup must admit the row, not reject it")
	rows := memBookNarratorRows(t, store)
	require.Len(t, rows, 1)
	require.Equal(t, book.ID, rows[0].BookID)
	requireUpdateReachesMemDB(t, store, book.ID, "WarmupDamaged (renamed)")
}
