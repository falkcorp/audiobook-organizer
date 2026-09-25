// file: internal/audiobooks/service_search_build_hydrate_test.go
// version: 1.0.0
// guid: 8e3b1f64-2a9c-4d75-b0e8-5c6f7a2d9b13
// last-edited: 2026-09-25

package audiobooks

import (
	"context"
	"sort"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/stretchr/testify/require"
)

// Review finding 4b: a cache build with no post-filter keeps the Bleve hit
// IDs and reads no book rows (its pages are hydrated when served). The mock
// store has no expectations, so any GetBooksByIDs / GetBooksForSearch call
// fails the test.
func TestSearchBuild_NoPostFilterReadsNoRows(t *testing.T) {
	idx := buildSearchTestIndex(t, "b1", "b2", "b3")
	svc := NewAudiobookService(mocks.NewMockStore(t))
	svc.SetSearchIndex(completedIndex(t, idx))

	books, total, err := svc.queryAudiobooks(context.Background(), 0, 0, "author:sanderson", nil, nil, ListFilters{}, nil, true)
	require.NoError(t, err)
	ids := make([]string, 0, len(books))
	for _, b := range books {
		ids = append(ids, b.ID)
	}
	sort.Strings(ids)
	require.Equal(t, []string{"b1", "b2", "b3"}, ids)
	require.Equal(t, 3, total)
}
