// file: internal/audiobooks/service_query_search_pagination_test.go
// version: 1.0.0
// guid: 8b1c4f27-9d3e-4a60-b5c8-1e70a2f43d96
// last-edited: 2026-08-12

// Regression tests for search + post-filter pagination.
//
// Measured on production 2026-08-12, before the fix, with
// search=honour&is_primary_version=true&limit=5:
//
//	offset=0  -> 1 row
//	offset=5  -> 0 rows
//	offset=10 -> 0 rows
//	offset=20 -> 0 rows
//
// while the identical query with no filter paged correctly (5/5/5). Bleve
// returned one page, the post-filter block deleted most of that already-cut
// page with nothing to refill it, and paginateFilteredBooks then re-sliced the
// remainder by the ORIGINAL offset — out of range for a <=limit slice, so
// every page after the first was empty. The library UI always sends
// is_primary_version=true, so every user-facing search took this path.
//
// These tests pin both halves: page 2 must be full, and the reported count
// must be the number of MATCHES rather than the length of the page.

package audiobooks

import (
	"context"
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// paginationTestBookIDs returns n stable IDs, zero-padded so Bleve's ordering
// and the assertions below stay legible when a case fails.
func paginationTestBookIDs(n int) []string {
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		ids = append(ids, fmt.Sprintf("bk%02d", i))
	}
	return ids
}

// newPaginationTestService wires a throwaway Bleve index over n books that all
// match author:sanderson, with every book flagged primary so the
// is_primary_version post-filter keeps the whole set. That isolates the
// pagination arithmetic: any row missing from a page is the slicing bug, not
// the predicate.
func newPaginationTestService(t *testing.T, n int) *AudiobookService {
	t.Helper()
	ids := paginationTestBookIDs(n)
	idx := buildSearchTestIndex(t, ids...)

	mockStore := mocks.NewMockStore(t)
	mockStore.EXPECT().GetBooksByIDs(mock.Anything).RunAndReturn(func(reqIDs []string) ([]database.Book, error) {
		primary := true
		books := make([]database.Book, 0, len(reqIDs))
		for _, id := range reqIDs {
			books = append(books, database.Book{ID: id, IsPrimaryVersion: &primary})
		}
		return books, nil
	}).Maybe()

	svc := NewAudiobookService(mockStore)
	svc.SetSearchIndex(idx)
	return svc
}

// TestSearchWithPostFilterReturnsFullSecondPage is the core regression. Before
// the fix this returned zero rows for every offset > 0.
func TestSearchWithPostFilterReturnsFullSecondPage(t *testing.T) {
	const total, limit = 12, 5
	svc := newPaginationTestService(t, total)
	primary := true

	cases := []struct {
		name       string
		offset     int
		wantOnPage int
	}{
		{"page 1", 0, limit},
		{"page 2", limit, limit},                       // returned 0 before the fix
		{"page 3 partial", 2 * limit, total - 2*limit}, // 2 rows
		{"past the end", 3 * limit, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, gotTotal, err := svc.GetAudiobooksWithTotal(
				context.Background(), limit, tc.offset, "author:sanderson", nil, nil,
				ListFilters{IsPrimaryVersion: &primary},
			)
			assert.NoError(t, err)
			assert.Len(t, got, tc.wantOnPage,
				"offset=%d should yield %d rows; a short or empty page here is the "+
					"post-filter-after-pagination bug", tc.offset, tc.wantOnPage)
			assert.Equal(t, total, gotTotal,
				"count must be the number of MATCHES (%d), not the length of the page", total)
		})
	}
}

// TestSearchCountDoesNotTrackLimit pins the second half of the defect: the
// reported count moved with the requested limit, because len(page) was being
// substituted for a real total. A caller could therefore never learn how many
// matches existed.
func TestSearchCountDoesNotTrackLimit(t *testing.T) {
	const total = 12
	svc := newPaginationTestService(t, total)
	primary := true

	for _, limit := range []int{1, 3, 5, 50} {
		t.Run(fmt.Sprintf("limit=%d", limit), func(t *testing.T) {
			got, gotTotal, err := svc.GetAudiobooksWithTotal(
				context.Background(), limit, 0, "author:sanderson", nil, nil,
				ListFilters{IsPrimaryVersion: &primary},
			)
			assert.NoError(t, err)
			assert.Equal(t, total, gotTotal,
				"count tracked the limit instead of reporting the match total")

			wantPage := limit
			if wantPage > total {
				wantPage = total
			}
			assert.Len(t, got, wantPage, "page length should still honour limit")
		})
	}
}

// TestSearchWithoutPostFilterStillPages is the positive control. This path was
// already correct before the fix — Bleve paginated it directly — so it must
// stay correct. If this breaks, the over-fetch was applied where it should not
// have been.
func TestSearchWithoutPostFilterStillPages(t *testing.T) {
	const total, limit = 12, 5
	svc := newPaginationTestService(t, total)

	for _, offset := range []int{0, limit} {
		got, gotTotal, err := svc.GetAudiobooksWithTotal(
			context.Background(), limit, offset, "author:sanderson", nil, nil, ListFilters{},
		)
		assert.NoError(t, err)
		assert.Len(t, got, limit, "unfiltered search paged correctly before the fix and must still")
		assert.Equal(t, total, gotTotal, "Bleve's own total should be reported verbatim")
	}
}
