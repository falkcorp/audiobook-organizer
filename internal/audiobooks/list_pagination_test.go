// file: internal/audiobooks/list_pagination_test.go
// version: 1.0.0
// guid: 7c1e9b42-3a6d-4f01-9d8e-2b5c6a7e1f04
// last-edited: 2026-06-28

package audiobooks

import (
	"context"
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// seedPaginationStore builds a real PebbleStore (memdb pushdown active) with
// `n` primary, title-sorted books whose titles are zero-padded so A→Z order is
// deterministic ("Book 0001" … "Book NNNN"). Draining warmupDone first ensures
// memdb is published before CreateBook write-through populates it.
func seedPaginationStore(t *testing.T, n int) (*AudiobookService, []string) {
	t.Helper()
	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	// Drain the async warmup so memPtr is published (empty); subsequent
	// CreateBook calls write through into the live memdb.
	ps.WaitForWarmup()

	primary := true
	wantTitles := make([]string, 0, n)
	for i := 0; i < n; i++ {
		title := fmt.Sprintf("Book %04d", i)
		wantTitles = append(wantTitles, title)
		_, err := ps.CreateBook(&database.Book{
			Title:            title,
			IsPrimaryVersion: &primary,
		})
		require.NoError(t, err)
	}
	return NewAudiobookService(ps), wantTitles
}

// TestGetAudiobooks_TitlePrimaryPagination is the regression test for the
// library "page 2 returns 0 items" bug. The light-pushdown path paginated in
// the store, then the post-filter block re-paginated the already-sliced page
// using the original offset — so any offset>0 returned nothing.
func TestGetAudiobooks_TitlePrimaryPagination(t *testing.T) {
	const total = 60
	svc, want := seedPaginationStore(t, total)
	ctx := context.Background()
	primary := true
	filt := ListFilters{IsPrimaryVersion: &primary, SortBy: "title", SortOrder: "asc"}

	pageSize := 20
	for page := 0; page < total/pageSize; page++ {
		off := page * pageSize
		got, err := svc.GetAudiobooks(ctx, pageSize, off, "", nil, nil, filt)
		require.NoError(t, err)
		require.Lenf(t, got, pageSize, "page %d (offset %d) must return %d books", page, off, pageSize)
		for i, b := range got {
			require.Equalf(t, want[off+i], b.Title,
				"page %d row %d: titles must be contiguous in sort order", page, i)
		}
	}
}

// unwrapStore mimics the production indexedStore decorator: it embeds a
// database.Store and exposes Unwrap() so the pushdown's single-level Unwrap can
// reach the concrete PebbleStore underneath. This proves the fix holds for the
// real production store shape (service store is wrapped, not bare).
type unwrapStore struct {
	database.Store
}

func (u unwrapStore) Unwrap() database.Store { return u.Store }

// TestGetAudiobooks_TitlePrimaryPagination_WrappedStore is the production-shape
// variant: the service's store is a decorator, and pushdown must reach the
// PebbleStore via Unwrap(). Without that, summariesPushdown would fall back to
// the unfiltered path and pagination would break differently.
func TestGetAudiobooks_TitlePrimaryPagination_WrappedStore(t *testing.T) {
	const total = 60
	svcInner, want := seedPaginationStore(t, total)
	// Re-wrap the same underlying store in a decorator and rebuild the service.
	svc := NewAudiobookService(unwrapStore{Store: svcInner.store.(database.Store)})
	ctx := context.Background()
	primary := true
	filt := ListFilters{IsPrimaryVersion: &primary, SortBy: "title", SortOrder: "asc"}

	pageSize := 20
	for page := 0; page < total/pageSize; page++ {
		off := page * pageSize
		got, err := svc.GetAudiobooks(ctx, pageSize, off, "", nil, nil, filt)
		require.NoError(t, err)
		require.Lenf(t, got, pageSize, "wrapped: page %d (offset %d) must return %d books", page, off, pageSize)
		for i, b := range got {
			require.Equalf(t, want[off+i], b.Title,
				"wrapped: page %d row %d: titles must be contiguous", page, i)
		}
	}
}

// TestGetAudiobooks_LargePageSize covers requirement #3: "if I set 500 it
// should load 500 books". Also asserts offset=pageSize returns the next slice.
func TestGetAudiobooks_LargePageSize(t *testing.T) {
	const total = 1000
	svc, want := seedPaginationStore(t, total)
	ctx := context.Background()
	primary := true
	filt := ListFilters{IsPrimaryVersion: &primary, SortBy: "title", SortOrder: "asc"}

	first, err := svc.GetAudiobooks(ctx, 500, 0, "", nil, nil, filt)
	require.NoError(t, err)
	require.Len(t, first, 500, "page size 500 at offset 0 must return 500 books")
	require.Equal(t, want[0], first[0].Title)
	require.Equal(t, want[499], first[499].Title)

	second, err := svc.GetAudiobooks(ctx, 500, 500, "", nil, nil, filt)
	require.NoError(t, err)
	require.Len(t, second, 500, "page size 500 at offset 500 must return the next 500 books")
	require.Equal(t, want[500], second[0].Title)
	require.Equal(t, want[999], second[499].Title)
}
