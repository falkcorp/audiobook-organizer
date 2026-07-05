// file: internal/database/pebble_books_pagination_test.go
// version: 1.0.1
// guid: 7f2a9c14-3b6d-4e81-9a2c-0d5f1e8b4a37
// last-edited: 2026-07-05

package database

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGetAllBooksFullFrom_PaginatesPastDoubleLimit is a regression test for the
// memdb-path bug where GetAllBooksFullFrom loaded only limit*2+1 books from the
// start and searched for the cursor within that window. Cursor pagination
// therefore stalled at the 2*limit boundary, so full-table backfills (intro
// transcription, search index) only ever processed the first ~2 pages of the
// library. See fix/transcribe-full-library.
func TestGetAllBooksFullFrom_PaginatesPastDoubleLimit(t *testing.T) {
	store := setupTestPebbleStore(t)
	store.WaitForWarmup()
	require.True(t, store.UseMemDB, "test must exercise the memdb path")
	require.NotNil(t, store.mem(), "memdb must be published")

	// Create enough books that the limit*2 boundary is well inside the set.
	const total = 55
	const pageSize = 10 // old cap was pageSize*2 = 20; 55 books exposes the bug

	for i := 0; i < total; i++ {
		b := &Book{
			Title:    fmt.Sprintf("Book %03d", i),
			FilePath: fmt.Sprintf("/tmp/book_%03d.m4b", i),
		}
		created, err := store.CreateBook(b)
		require.NoError(t, err)
		// Propagate into memdb so the memdb read path sees it (production does
		// this write-through on every book mutation).
		store.UpsertBookToMemDB(context.Background(), created)
	}

	// Walk the whole library via cursor pagination, exactly like the transcribe
	// op and search backfill do.
	seen := make(map[string]bool, total)
	cursor := ""
	pages := 0
	for {
		page, err := store.GetAllBooksFullFrom(cursor, pageSize)
		require.NoError(t, err)
		if len(page) == 0 {
			break
		}
		pages++
		require.Less(t, pages, total, "pagination did not terminate")
		for _, b := range page {
			require.False(t, seen[b.ID], "book %s returned on more than one page", b.ID)
			seen[b.ID] = true
		}
		if len(page) < pageSize {
			break // last page
		}
		cursor = page[len(page)-1].ID
	}

	// The whole point: every book is reachable, not just the first 2*pageSize.
	require.Equal(t, total, len(seen),
		"expected all %d books across pages, got %d (old bug stopped at %d)",
		total, len(seen), pageSize*2)
}

// TestGetAllBooksFullFrom_UnknownCursorEndsIteration verifies that a stale/unknown
// cursor returns no rows (ending iteration) rather than restarting from the
// top — which would loop forever.
func TestGetAllBooksFullFrom_UnknownCursorEndsIteration(t *testing.T) {
	store := setupTestPebbleStore(t)
	store.WaitForWarmup()

	for i := 0; i < 5; i++ {
		b := &Book{Title: fmt.Sprintf("B%d", i), FilePath: fmt.Sprintf("/tmp/b%d.m4b", i)}
		created, err := store.CreateBook(b)
		require.NoError(t, err)
		store.UpsertBookToMemDB(context.Background(), created)
	}

	page, err := store.GetAllBooksFullFrom("zzzz-nonexistent-cursor", 10)
	require.NoError(t, err)
	require.Empty(t, page, "unknown cursor must end iteration, not restart")
}
