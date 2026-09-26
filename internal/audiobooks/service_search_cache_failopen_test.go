// file: internal/audiobooks/service_search_cache_failopen_test.go
// version: 1.0.0
// guid: aaa9761e-be02-4d73-b401-4c5ce57dd330
// last-edited: 2026-09-25

package audiobooks

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/searchcache"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// Review 2, finding 2: a shared cache build that fails (here a storage read
// error in its fail-closed hydration) must not fail the caller. The request
// falls back to the per-request search, which is fail-open, exactly as before
// the cache existed. Before the fix the build error was returned as is, which
// the list handler answers with a 500.
func TestGetAudiobooksPage_SharedBuildErrorFallsBackUncached(t *testing.T) {
	idx := buildSearchTestIndex(t, "b1", "b2")
	store := mocks.NewMockStore(t)
	svc := NewAudiobookService(store)
	svc.SetSearchIndex(completedIndex(t, idx))
	svc.SetSearchResultCache(searchcache.New(searchcache.NewChangeLog(16), searchcache.Config{}))

	primary := true
	books := []database.Book{{ID: "b1", Title: "b1"}, {ID: "b2", Title: "b2"}}
	// The build's hydration (the IsPrimaryVersion post-filter reads rows)
	// hits a read error once; the uncached request then reads fine.
	store.EXPECT().GetBooksByIDs(mock.Anything).Return(nil, errors.New("pebble: transient read error")).Once()
	store.EXPECT().GetBooksByIDs(mock.Anything).Return(books, nil).Maybe()

	for _, ctx := range []context.Context{context.Background(), WithPendingSearchResponse(context.Background())} {
		got, total, meta, err := svc.GetAudiobooksPage(ctx, 10, 0, "author:sanderson", nil, nil, ListFilters{IsPrimaryVersion: &primary})
		require.NoError(t, err, "a shared build error reached the caller")
		require.False(t, meta.Stale)
		require.Equal(t, 2, total)
		require.Len(t, got, 2)
	}
}
