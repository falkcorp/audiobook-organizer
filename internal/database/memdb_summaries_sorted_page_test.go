// file: internal/database/memdb_summaries_sorted_page_test.go
// version: 1.0.0
// guid: 7c2e59b1-30da-4f68-b4e7-9a51c6d820fe
// last-edited: 2026-08-25

package database

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// The memdb summary walker must choose its page from the ORDERED match set.
//
// GetAudiobooks cannot observe this: for a sort it cannot stream, the service
// zeroes limit/offset and paginates the full set itself, so the store's own
// pagination never runs on that path. Every other caller of
// GetAllBookSummariesFiltered does reach it. Sorting a page instead of paging
// a sorted set yields a page that looks perfectly ordered and holds the wrong
// rows -- the failure #2892 fixed on the Pebble twin
// (pebble_summaries_fallback_sort_test.go); this is the memdb side of it.

// msp* names are task-unique per repo convention for package-shared helpers.

func mspSeed(t *testing.T, p *PebbleStore) map[string]string {
	t.Helper()
	// Genres deliberately in neither ascending nor descending order, so the
	// natural walk order can satisfy neither direction by accident.
	order := []int{3, 0, 5, 1, 4, 2}
	marks := make(map[string]string, len(order))
	for _, rank := range order {
		primary := true
		g := fmt.Sprintf("genre-%d", rank)
		created, err := p.CreateBook(&Book{
			Title:            fmt.Sprintf("book-%d", rank),
			FilePath:         fmt.Sprintf("/tmp/msp_%d.m4b", rank),
			IsPrimaryVersion: &primary,
			Genre:            &g,
		})
		require.NoError(t, err)
		marks[created.ID] = fmt.Sprintf("r%d", rank)
	}
	p.WaitForWarmup()
	return marks
}

func mspOrder(marks map[string]string, out []BookSummary) []string {
	got := make([]string, 0, len(out))
	for _, s := range out {
		got = append(got, marks[s.ID])
	}
	return got
}

func TestMemdbSortedSummaryPage(t *testing.T) {
	p := setupTestPebbleStore(t)
	p.WaitForWarmup()
	marks := mspSeed(t, p)
	require.True(t, p.UseMemDB && p.mem() != nil, "this test must exercise the memdb path")

	full := []string{"r0", "r1", "r2", "r3", "r4", "r5"}

	t.Run("FixtureGuard_WalkOrderMatchesNeitherDirection", func(t *testing.T) {
		// Unsorted, the walk must equal neither answer -- otherwise a page that
		// was never sorted could still satisfy the assertions below.
		got, err := p.GetAllBookSummariesFiltered(0, 0, BookSummaryFilter{})
		require.NoError(t, err)
		natural := mspOrder(marks, got)
		require.NotEqual(t, full, natural, "fixture cannot detect a missing ascending sort")
		require.NotEqual(t, []string{"r5", "r4", "r3", "r2", "r1", "r0"}, natural,
			"fixture cannot detect a missing descending sort")
	})

	t.Run("FullPageAscending", func(t *testing.T) {
		got, err := p.GetAllBookSummariesFiltered(0, 0,
			BookSummaryFilter{SortBy: "genre", SortAscending: true})
		require.NoError(t, err)
		require.Equal(t, full, mspOrder(marks, got))
	})

	t.Run("FullPageDescending", func(t *testing.T) {
		got, err := p.GetAllBookSummariesFiltered(0, 0,
			BookSummaryFilter{SortBy: "genre", SortAscending: false})
		require.NoError(t, err)
		require.Equal(t, []string{"r5", "r4", "r3", "r2", "r1", "r0"}, mspOrder(marks, got))
	})

	t.Run("ConstrainedPageTakesFirstOfSortedSet", func(t *testing.T) {
		got, err := p.GetAllBookSummariesFiltered(2, 0,
			BookSummaryFilter{SortBy: "genre", SortAscending: true})
		require.NoError(t, err)
		require.Equal(t, []string{"r0", "r1"}, mspOrder(marks, got),
			"the first page must be the two SMALLEST genres, not the first two walked")
	})

	t.Run("PagesPartitionTheOrderedSet", func(t *testing.T) {
		var seen []string
		for offset := 0; offset < len(full); offset += 2 {
			page, err := p.GetAllBookSummariesFiltered(2, offset,
				BookSummaryFilter{SortBy: "genre", SortAscending: true})
			require.NoErrorf(t, err, "offset %d", offset)
			seen = append(seen, mspOrder(marks, page)...)
		}
		require.Equal(t, full, seen, "successive pages must partition the ordered set in order")
	})

	t.Run("OffsetBeyondEndIsEmpty", func(t *testing.T) {
		got, err := p.GetAllBookSummariesFiltered(5, len(full)+3,
			BookSummaryFilter{SortBy: "genre", SortAscending: true})
		require.NoError(t, err)
		require.Empty(t, got)
	})
}
