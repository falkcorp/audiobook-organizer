// file: internal/audiobooks/service_query_search_hydration_test.go
// version: 1.0.0
// guid: 9f0a1b2c-3d4e-5f60-7182-93a4b5c6d7e8
// last-edited: 2026-07-11

// Tests for the searchWithBleve batch-hydration fail-open path (INIT-4 T3).
// GetBookByID's per-hit loop was replaced with a single GetBooksByIDs call
// (see pebble_store_books_by_ids_test.go for the store-level getter
// contract); this file pins the service-level fail-open contract: a
// non-nil error from GetBooksByIDs must never fail the whole search
// request, only shrink the page to whatever rows were hydrated.

package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// TestSearchWithBleveHydrationErrorPartialPage covers spec §C3 parity: when
// GetBooksByIDs returns (partial rows, error) — e.g. a corrupt row mid-batch
// per pebble_store_books_by_ids_test.go's ErrorAlongsideRows contract — the
// service must serve the partial page and log a warning, never propagate
// the error to the caller (fail-open, mirroring the old per-hit loop's
// silent-skip-on-error semantics).
func TestSearchWithBleveHydrationErrorPartialPage(t *testing.T) {
	idx := buildSearchTestIndex(t, "b1", "b2", "b3")
	mockStore := mocks.NewMockStore(t)

	hydrateErr := errors.New("unmarshal book \"b3\": unexpected end of JSON input")
	// Bleve's hit order for equally-scored docs isn't guaranteed to match
	// seed order, so the "corrupt row" position is determined at call time
	// from whatever order GetBooksByIDs actually receives — the assertions
	// below key off that same position rather than assuming b3 is 3rd.
	var wantHydrated int
	mockStore.EXPECT().GetBooksByIDs(mock.Anything).RunAndReturn(func(ids []string) ([]database.Book, error) {
		// Simulate a corrupt row for "b3": return rows read so far
		// (everything before it) alongside the error, per the getter's
		// error-alongside-rows contract.
		partial := make([]database.Book, 0, len(ids))
		for _, id := range ids {
			if id == "b3" {
				wantHydrated = len(partial)
				return partial, hydrateErr
			}
			partial = append(partial, database.Book{ID: id})
		}
		t.Fatal("test setup error: \"b3\" not present in hydration ids")
		return partial, nil
	})

	buf := captureWarnLog(t)

	svc := NewAudiobookService(mockStore)
	svc.SetSearchIndex(idx)

	got, err := svc.GetAudiobooks(context.Background(), 50, 0, "author:sanderson", nil, nil, ListFilters{})
	assert.NoError(t, err, "hydration error must never fail the whole search request")
	assert.Len(t, got, wantHydrated, "partial page: only rows hydrated before the corrupt row are served")
	assert.Contains(t, buf.String(), "search: batch hydrate failed; serving partial page")
	assert.Contains(t, buf.String(), fmt.Sprintf("hydrated=%d", wantHydrated))
}
