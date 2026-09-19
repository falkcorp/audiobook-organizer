// file: internal/database/search_books_filtered_test.go
// version: 1.0.0
// guid: 5d9d7272-0eb0-4b4c-8b28-90e895f4e6fd
// last-edited: 2026-09-19

package database

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestSearchBooksFiltered_LimitCountsAdmittedHitsOnly is the store half of the
// item-6 B6 fix: the ABS search used unfiltered SearchBooks, so merge losers and
// hidden copies filled the limit (89% of prod hits) and their redirecting sync
// ids sent GET /api/items/:id to a different book. The filter must apply INSIDE
// the scan: with 5 hidden matches sorting before 2 visible ones, limit=2 must
// return both visible books on BOTH the memdb and the Pebble path — a
// post-filtered limit-2 result would return none.
func TestSearchBooksFiltered_LimitCountsAdmittedHitsOnly(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	p, ok := store.(*PebbleStore)
	require.True(t, ok)

	yes, no := true, false
	organized, source := "organized", "organized_source"
	now := time.Now()
	mk := func(title string, mut func(*Book)) string {
		t.Helper()
		b := &Book{Title: title, IsPrimaryVersion: &yes, LibraryState: &organized}
		if mut != nil {
			mut(b)
		}
		created, err := store.CreateBook(b)
		require.NoError(t, err)
		return created.ID
	}
	// Hidden first, so they sort first in ULID order.
	mk("Needle loser", func(b *Book) { b.IsPrimaryVersion = &no; b.LibraryState = &source })
	mk("Needle source copy", func(b *Book) { b.LibraryState = &source })
	mk("Needle non-primary", func(b *Book) { b.IsPrimaryVersion = &no })
	mk("Needle quarantined", func(b *Book) { b.QuarantinedAt = &now })
	trashed := mk("Needle trashed", nil)
	tb, err := store.GetBookByID(trashed)
	require.NoError(t, err)
	tb.MarkedForDeletion = &yes
	_, err = store.UpdateBook(trashed, tb)
	require.NoError(t, err)
	visible1 := mk("Needle visible one", nil)
	visible2 := mk("Needle visible two", nil)

	f := BookSummaryFilter{IsPrimaryVersion: &yes, LibraryState: organized, ExcludeQuarantined: true}
	for _, useMemDB := range []bool{true, false} {
		p.UseMemDB = useMemDB
		if useMemDB {
			require.NotNil(t, p.mem(), "memdb must be published for the fast path")
		}
		got, err := p.SearchBooksFiltered("needle", 2, 0, f)
		require.NoError(t, err)
		require.Equal(t, []string{visible1, visible2}, searchIDs(got), "memdb=%v", useMemDB)

		// offset skips ADMITTED matches, not hidden ones.
		got, err = p.SearchBooksFiltered("needle", 0, 1, f)
		require.NoError(t, err)
		require.Equal(t, []string{visible2}, searchIDs(got), "memdb=%v offset", useMemDB)

		// SearchBooks itself stays unfiltered for its other callers.
		all, err := p.SearchBooks("needle", 0, 0)
		require.NoError(t, err)
		require.Len(t, all, 7, "memdb=%v: SearchBooks must keep returning every row", useMemDB)
	}
	p.UseMemDB = true
}
