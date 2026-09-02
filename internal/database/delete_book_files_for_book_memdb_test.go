// file: internal/database/delete_book_files_for_book_memdb_test.go
// version: 1.0.1
// guid: 2b81f6d4-9e07-4a53-b2c9-e15a08d47f36
// last-edited: 2026-09-02

package database

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDeleteBookFilesForBook_RemovesMemdbRows pins the 2026-08-14 fix: the
// method deleted the Pebble rows and their indexes but never told memdb, so
// every memdb-backed read kept serving the deleted files until a restart.
// The assertion reads back through the MEMDB dispatch path specifically —
// a Pebble-path read passes with or without the fix and proves nothing.
func TestDeleteBookFilesForBook_RemovesMemdbRows(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	p, ok := store.(*PebbleStore)
	require.True(t, ok, "expected *PebbleStore from setupPebbleTestDB")
	// Writes during warmup are buffered; wait so the create/delete below are
	// applied live and the memdb read path is actually selectable.
	p.WaitForWarmup()
	require.True(t, p.IsMemReady(), "memdb must be published so the read below takes the memdb path")
	p.UseMemDB = true

	book, err := store.CreateBook(&Book{Title: "Doomed Files", FilePath: "/lib/doomed"})
	require.NoError(t, err)
	// ZERO duration/size on purpose. With non-zero aggregates the delete's
	// notifyBookFileChange -> RecomputeBookAggregates -> UpdateBook chain
	// resyncs the book's memdb file set from Pebble as a SIDE EFFECT, healing
	// the divergence and letting a missing memdb delete pass (verified by
	// mutation: with Duration:100 the unfixed code was green). When the
	// aggregates do not change, RecomputeBookAggregates early-returns without
	// UpdateBook (pebble_store_book_aggregates.go:131), no resync happens, and
	// the missing memdb delete is observable — the only path where it bites,
	// and exactly the shape the 2026-08-04 canary hit in production.
	for i := range 3 {
		require.NoError(t, store.CreateBookFile(&BookFile{
			BookID:   book.ID,
			FilePath: fmt.Sprintf("/lib/doomed/part-%d.mp3", i),
			Duration: 0,
		}))
	}

	// GetBookFilesForIDsCore is the instrument, NOT GetBookFiles:
	// GetBookFiles reads Pebble unconditionally, so it passes with or without
	// the memdb delete and can prove nothing (verified — the first version of
	// this test used it and the mutant survived). GetBookFilesForIDsCore
	// dispatches to memdb when warm, and it is what feeds total_file_count —
	// the exact read the 2026-08-03 production canary saw stay stale.
	before, err := p.GetBookFilesForIDsCore([]string{book.ID})
	require.NoError(t, err)
	require.Len(t, before[book.ID], 3, "fixture: three files must be visible on the memdb path before the delete")

	require.NoError(t, store.DeleteBookFilesForBook(book.ID))

	// Memdb path — the one that stayed stale before the fix.
	after, err := p.GetBookFilesForIDsCore([]string{book.ID})
	require.NoError(t, err)
	require.Empty(t, after[book.ID],
		"memdb still serves book_file rows that DeleteBookFilesForBook removed from Pebble — the memdb delete is missing")

	// And the Pebble rows are genuinely gone, so the two reads agree.
	afterPebble, err := store.GetBookFiles(book.ID)
	require.NoError(t, err)
	require.Empty(t, afterPebble, "pebble rows must be gone after the delete")
}
