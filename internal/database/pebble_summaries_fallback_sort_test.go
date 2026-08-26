// file: internal/database/pebble_summaries_fallback_sort_test.go
// version: 1.0.0
// guid: 5b8e2c14-7a93-4d60-8f25-1c9d4e7a3b62
// last-edited: 2026-08-25

package database

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// This file pins the ORDERING half of the HonorsEveryBookSummaryFilter
// contract. Its sibling, pebble_summaries_fallback_filter_test.go, pins which
// ROWS come back; this one pins the order they come back in, on the same two
// backends.
//
// The two halves failed for the same reason a fortnight apart. The marker
// declares that this store applies "EVERY predicate on BookSummaryFilter",
// but it was written against an enumerated list of row predicates, and
// SortBy/SortAscending — fields on that same struct — were never counted as
// members of it. So the fallback went on ignoring SortBy exactly the way it
// had gone on ignoring LibraryState and ReviewStatus, and for the same
// reason: nothing tested the clause.
//
// The failure was not a visibly unsorted page, which is what makes it worth
// a dedicated file. AudiobookService.GetAudiobooks skips its own sort when a
// store claims pushdown, then re-sorts the finished page anyway as cheap
// insurance. On the Pebble fallback that insurance did active harm: the store
// cut an arbitrary window in key order, and the service sorted that window
// into a page that looked perfectly ordered. A request for "the 50 oldest
// books" returned 50 books in year order that were not the 50 oldest, 200 OK,
// no error surface anywhere.
//
// Hence ConstrainedPage below. A full-page assertion cannot see this defect —
// with every match on one page, sorting before and after paginating give the
// identical answer. The page has to be smaller than the match set before the
// two orders diverge.

// sortFixture is a seed row for the ordering tests. Only the title matters;
// the rest exists so the row survives the default filters.
type sortFixture struct {
	id    string
	title string
}

// seedSortBooks creates books in an order that is neither ascending nor
// descending by title.
//
// Both halves of that matter, and the second half was learned the hard way.
// Seeding in descending order makes the store's natural (key) order coincide
// with a correct DESCENDING result, so every descending assertion passes
// whether or not the sort ran at all — a mutation that disabled sorting
// entirely still left those subtests green. An unsorted fixture can only
// observe a missing sort in the directions its own layout does not already
// reproduce, so the layout has to reproduce neither.
func seedSortBooks(t *testing.T, p *PebbleStore) []sortFixture {
	t.Helper()

	fixtures := []sortFixture{
		{title: "Whiskey Lane"},
		{title: "Zulu Station"},
		{title: "Uniform Sky"},
		{title: "Yankee Ridge"},
		{title: "Victor Reach"},
		{title: "Xenon Drift"},
	}
	for i := range fixtures {
		f := &fixtures[i]
		primary := true
		created, err := p.CreateBook(&Book{
			Title:            f.title,
			FilePath:         fmt.Sprintf("/tmp/fallbacksort_%02d.m4b", i),
			IsPrimaryVersion: &primary,
		})
		require.NoError(t, err)
		require.NotNil(t, created)
		f.id = created.ID
	}
	return fixtures
}

// expectTitlesSorted returns the fixture titles in the order the request asks
// for. The oracle is computed from the seed data here rather than read back
// out of the store, so a defect shared by BOTH backends still fails.
func expectTitlesSorted(fixtures []sortFixture, ascending bool) []string {
	out := make([]string, 0, len(fixtures))
	for _, f := range fixtures {
		out = append(out, f.title)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i]), strings.ToLower(out[j])
		if ascending {
			return a < b
		}
		return a > b
	})
	return out
}

// summaryTitlesInOrder preserves the returned order — unlike idsOf, which sorts for set
// comparison. Sorting the actual result before comparing it to a sorted
// expectation is how an ordering test passes without testing ordering.
func summaryTitlesInOrder(summaries []BookSummary) []string {
	out := make([]string, 0, len(summaries))
	for _, s := range summaries {
		out = append(out, s.Title)
	}
	return out
}

// TestGetAllBookSummariesFiltered_FallbackHonorsSortBy asserts that both
// backends return the requested ORDER, and that they cut the page from the
// ordered match set rather than ordering whatever window they happened to cut.
func TestGetAllBookSummariesFiltered_FallbackHonorsSortBy(t *testing.T) {
	p := setupTestPebbleStore(t)
	p.WaitForWarmup()

	fixtures := seedSortBooks(t, p)
	wantAsc := expectTitlesSorted(fixtures, true)
	wantDesc := expectTitlesSorted(fixtures, false)
	require.Len(t, wantAsc, 6, "fixture guard: expected 6 seeded books")

	for _, useMemDB := range []bool{true, false} {
		backend := "MemDBPath"
		if !useMemDB {
			backend = "PebbleFallbackPath"
		}
		t.Run(backend, func(t *testing.T) {
			p.UseMemDB = useMemDB

			// Fixture guard. If the store's natural (unsorted) order already
			// happened to be title order, every assertion below would pass
			// without the sort doing anything — the test would be inert and
			// look green. Establish that the two orders actually differ
			// before trusting any of them.
			t.Run("FixtureGuard_UnsortedOrderDiffersBothWays", func(t *testing.T) {
				natural, err := p.GetAllBookSummariesFiltered(0, 0, BookSummaryFilter{})
				require.NoError(t, err)
				require.Len(t, natural, len(wantAsc))
				got := summaryTitlesInOrder(natural)
				// Checking only ascending is not enough: a fixture seeded in
				// descending order makes "no sort" and "correct descending
				// sort" the same sequence, and the descending subtests below
				// then hold for a store that never sorts.
				require.NotEqual(t, wantAsc, got,
					"%s: unsorted order already equals ASCENDING order — this "+
						"fixture cannot observe a missing ascending sort", backend)
				require.NotEqual(t, wantDesc, got,
					"%s: unsorted order already equals DESCENDING order — this "+
						"fixture cannot observe a missing descending sort", backend)
			})

			t.Run("FullPageAscending", func(t *testing.T) {
				got, err := p.GetAllBookSummariesFiltered(0, 0, BookSummaryFilter{
					SortBy: "title", SortAscending: true,
				})
				require.NoError(t, err)
				require.Equal(t, wantAsc, summaryTitlesInOrder(got), "%s", backend)
			})

			t.Run("FullPageDescending", func(t *testing.T) {
				got, err := p.GetAllBookSummariesFiltered(0, 0, BookSummaryFilter{
					SortBy: "title", SortAscending: false,
				})
				require.NoError(t, err)
				require.Equal(t, wantDesc, summaryTitlesInOrder(got), "%s", backend)
			})

			// The discriminator. A page smaller than the match set is the
			// only shape in which sort-then-paginate and paginate-then-sort
			// give different answers, so this is the subtest that actually
			// distinguishes the two designs.
			t.Run("ConstrainedPage_TakesFirstOfSortedSet", func(t *testing.T) {
				got, err := p.GetAllBookSummariesFiltered(2, 0, BookSummaryFilter{
					SortBy: "title", SortAscending: true,
				})
				require.NoError(t, err)
				require.Equal(t, wantAsc[:2], summaryTitlesInOrder(got),
					"%s: a limited page must hold the FIRST rows of the "+
						"ordered set, not an arbitrary window sorted after "+
						"the fact", backend)
			})

			t.Run("ConstrainedPage_Descending", func(t *testing.T) {
				got, err := p.GetAllBookSummariesFiltered(2, 0, BookSummaryFilter{
					SortBy: "title", SortAscending: false,
				})
				require.NoError(t, err)
				require.Equal(t, wantDesc[:2], summaryTitlesInOrder(got), "%s", backend)
			})

			// Pages must partition the ordered set in order: page N holds
			// exactly the rows ranked [N*size, (N+1)*size). Checking only
			// page 1 would miss an offset applied against the pre-sort set.
			t.Run("PagesPartitionTheOrderedSet", func(t *testing.T) {
				const pageSize = 2
				seen := make([]string, 0, len(wantAsc))
				for offset := 0; offset < len(wantAsc); offset += pageSize {
					page, err := p.GetAllBookSummariesFiltered(pageSize, offset, BookSummaryFilter{
						SortBy: "title", SortAscending: true,
					})
					require.NoError(t, err)
					end := min(offset+pageSize, len(wantAsc))
					require.Equal(t, wantAsc[offset:end], summaryTitlesInOrder(page),
						"%s: page at offset %d", backend, offset)
					seen = append(seen, summaryTitlesInOrder(page)...)
				}
				require.Equal(t, wantAsc, seen,
					"%s: pages must reassemble the ordered set exactly", backend)
			})

			// Offset past the end is a page request, not an error.
			t.Run("OffsetBeyondEndIsEmpty", func(t *testing.T) {
				got, err := p.GetAllBookSummariesFiltered(2, len(wantAsc)+5, BookSummaryFilter{
					SortBy: "title", SortAscending: true,
				})
				require.NoError(t, err)
				require.Empty(t, got, "%s", backend)
			})
		})
	}
}
