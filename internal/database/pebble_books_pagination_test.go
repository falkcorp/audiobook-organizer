// file: internal/database/pebble_books_pagination_test.go
// version: 1.2.0
// guid: 7f2a9c14-3b6d-4e81-9a2c-0d5f1e8b4a37
// last-edited: 2026-09-12

package database

import (
	"context"
	"fmt"
	"slices"
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

	for i := range total {
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

// seedMemBooks creates n books through the store and memdb write-through, and
// returns their IDs in the order GetAllBooksFullFrom walks them.
func seedMemBooks(t *testing.T, store *PebbleStore, n int) []string {
	t.Helper()
	for i := range n {
		b := &Book{Title: fmt.Sprintf("B%03d", i), FilePath: fmt.Sprintf("/tmp/b%03d.m4b", i)}
		created, err := store.CreateBook(b)
		require.NoError(t, err)
		store.UpsertBookToMemDB(context.Background(), created)
	}
	ids, err := store.mem().ListBookIDs()
	require.NoError(t, err)
	require.Len(t, ids, n)
	require.True(t, slices.IsSorted(ids), "memdb ID index must iterate in byte order for the cursor seek")
	return ids
}

// A cursor past the last ID has nothing after it. Seeking forward can never
// restart from the top, so there is no loop to guard against.
func TestGetAllBooksFullFrom_CursorPastTheEndReturnsNothing(t *testing.T) {
	store := setupTestPebbleStore(t)
	store.WaitForWarmup()
	require.NotNil(t, store.mem(), "test must exercise the memdb path")
	ids := seedMemBooks(t, store, 5)

	for _, cursor := range []string{ids[len(ids)-1], "zzzz-nonexistent-cursor"} {
		page, err := store.GetAllBooksFullFrom(cursor, 10)
		require.NoError(t, err)
		require.Empty(t, page, "cursor %q is at or past the last book", cursor)
	}
}

// TestGetAllBooksFullFrom_DeletedCursorResumesAtSuccessor: a cursor book that
// is merged or deleted between two pages must not end the walk. The memdb
// branch used to look the cursor up by exact match and return nothing, and the
// acoustid backfill reported that truncated walk as a complete run.
func TestGetAllBooksFullFrom_DeletedCursorResumesAtSuccessor(t *testing.T) {
	store := setupTestPebbleStore(t)
	store.WaitForWarmup()
	require.NotNil(t, store.mem(), "test must exercise the memdb path")
	ids := seedMemBooks(t, store, 12)

	first, err := store.GetAllBooksFullFrom("", 4)
	require.NoError(t, err)
	require.Len(t, first, 4)
	cursor := first[len(first)-1].ID
	require.NoError(t, store.DeleteBook(cursor))
	store.DeleteBookFromMemDB(context.Background(), cursor)

	var rest []string
	for {
		page, err := store.GetAllBooksFullFrom(cursor, 4)
		require.NoError(t, err)
		if len(page) == 0 {
			break
		}
		for _, b := range page {
			rest = append(rest, b.ID)
		}
		cursor = page[len(page)-1].ID
	}
	require.Equal(t, ids[4:], rest, "the walk must continue after the deleted cursor book")

	// A cursor that never existed but sorts mid-table resumes at its successor.
	page, err := store.GetAllBooksFullFrom(ids[5]+"~", 3)
	require.NoError(t, err)
	got := make([]string, 0, len(page))
	for _, b := range page {
		got = append(got, b.ID)
	}
	require.Equal(t, ids[6:9], got)
}

// A row that memdb still lists but Pebble no longer holds is skipped, and the
// page is filled from the rows after it: a short page must mean the end of the
// table, because some callers stop on len(page) < limit.
func TestGetAllBooksFullFrom_FillsPagePastVanishedRows(t *testing.T) {
	store := setupTestPebbleStore(t)
	store.WaitForWarmup()
	require.NotNil(t, store.mem(), "test must exercise the memdb path")
	ids := seedMemBooks(t, store, 10)

	// Remove the record under memdb's feet, as a concurrent delete would
	// between ListBookIDs and the point read.
	require.NoError(t, store.db.Delete([]byte("book:"+ids[1]), nil))

	page, err := store.GetAllBooksFullFrom("", 4)
	require.NoError(t, err)
	got := make([]string, 0, len(page))
	for _, b := range page {
		got = append(got, b.ID)
	}
	require.Equal(t, []string{ids[0], ids[2], ids[3], ids[4]}, got)
}

// TestListBookIDs_MixedCaseByteOrder locks the ordering GetAllBooksFullFrom's
// memdb cursor seek relies on: ListBookIDs returns IDs in plain byte order
// (digits, then uppercase, then lowercase) on both store paths, and the seek
// lands on the first ID greater than the cursor in that order. The memdb ID
// index (memdb_schema.go) is a StringFieldIndex with Lowercase unset, so it
// keys on the raw ID bytes. A case-folding index would interleave "01Zeta"
// and "01alpha", and sort.SearchStrings would resume at the wrong place.
func TestListBookIDs_MixedCaseByteOrder(t *testing.T) {
	ids := []string{"01zeta", "01Zeta", "01ALPHA", "01alpha", "01Alpha", "0b", "0B", "9Q", "9q", "a1", "A1", "Z9", "z9"}
	want := []string{"01ALPHA", "01Alpha", "01Zeta", "01alpha", "01zeta", "0B", "0b", "9Q", "9q", "A1", "Z9", "a1", "z9"}
	pages := []struct {
		cursor string
		want   []string
	}{
		{"01Alpha", []string{"01Zeta", "01alpha", "01zeta"}}, // present cursor
		{"01Beta", []string{"01Zeta", "01alpha", "01zeta"}},  // absent, between 01Alpha and 01Zeta
		{"01Zz", []string{"01alpha", "01zeta", "0B"}},        // absent, between 01Zeta and 01alpha
		{"0b", []string{"9Q", "9q", "A1"}},
		{"Z9", []string{"a1", "z9"}}, // short page: the end of the table
	}

	for _, path := range []struct {
		name     string
		useMemDB bool
	}{{"memdb", true}, {"pebble", false}} {
		t.Run(path.name, func(t *testing.T) {
			store := setupTestPebbleStore(t)
			store.WaitForWarmup()
			require.NotNil(t, store.mem(), "memdb must be published")
			for i, id := range ids {
				created, err := store.CreateBook(&Book{ID: id, Title: "Mixed " + id, FilePath: fmt.Sprintf("/tmp/mixed_%02d.m4b", i)})
				require.NoError(t, err)
				require.Equal(t, id, created.ID, "CreateBook must keep a caller-supplied ID")
				store.UpsertBookToMemDB(context.Background(), created)
			}
			store.UseMemDB = path.useMemDB

			got, err := store.ListBookIDs()
			require.NoError(t, err)
			require.Equal(t, want, got, "ListBookIDs must return IDs in plain byte order")

			for _, p := range pages {
				page, err := store.GetAllBooksFullFrom(p.cursor, 3)
				require.NoError(t, err)
				gotPage := make([]string, 0, len(page))
				for _, b := range page {
					gotPage = append(gotPage, b.ID)
				}
				require.Equal(t, p.want, gotPage, "cursor %q", p.cursor)
			}
		})
	}
}
