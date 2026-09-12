// file: internal/audiobooks/service_query_series_position_sort_test.go
// version: 1.1.0
// guid: 5b1e9c3a-7d42-4f8e-a0c6-3e9d18b27f45
// last-edited: 2026-09-12

// Regression tests for the series_position sort key (TODO: "No sort key for
// position within a series").
//
// The library list's `series` sort orders by series NAME, which ties for
// every book in one series, so /library?series_id=N had no way to list a
// series in reading order. These pin the series_position contract: numeric
// (not lexical) order, decimals from series_position_raw, missing positions
// last in both directions, a title-then-ID tie-break so pages are stable, the
// sort applied to the whole set before paging, and the default for a bare
// series_id listing.

package audiobooks

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// seriesPositionCores is a series whose getter order is deliberately NOT
// reading order. It covers:
//   - "10" vs "2"/"3": a lexical sort would put 10 before 2;
//   - "An Interlude" at raw "1.5" (integer sequence 1): only the raw value
//     places it after "One" -- on the integer it would tie at 1 and win the
//     title tie-break, so this fails if the raw decimal is ignored;
//   - "Three" with an unparseable raw "Book 3": falls back to sequence 3;
//   - two books at position 2 ("A Second Two", "Two"): title tie-break;
//   - two books at 10 with the same title: ID tie-break;
//   - two unnumbered books: last, by title (case-insensitive).
func seriesPositionCores() []database.BookCore {
	seq := func(n int) *int { return &n }
	raw := func(s string) *string { return &s }
	return []database.BookCore{
		{ID: "b-ten-dup", Title: "Ten", SeriesSequence: seq(10)},
		{ID: "b-none-beta", Title: "Beta"},
		{ID: "b-two", Title: "Two", SeriesSequence: seq(2), SeriesPositionRaw: raw("2")},
		{ID: "b-interlude", Title: "An Interlude", SeriesSequence: seq(1), SeriesPositionRaw: raw("1.5")},
		{ID: "b-ten", Title: "Ten", SeriesSequence: seq(10), SeriesPositionRaw: raw("10")},
		{ID: "b-none-alpha", Title: "alpha"},
		{ID: "b-three", Title: "Three", SeriesSequence: seq(3), SeriesPositionRaw: raw("Book 3")},
		{ID: "b-one", Title: "One", SeriesSequence: seq(1)},
		{ID: "b-two-a", Title: "A Second Two", SeriesSequence: seq(2)},
	}
}

var seriesPositionAsc = []string{
	"b-one", "b-interlude", "b-two-a", "b-two", "b-three", "b-ten", "b-ten-dup", "b-none-alpha", "b-none-beta",
}

// pageSeries walks a bare series_id listing in pages of limit and returns the
// IDs in the order the pages delivered them, asserting the exact total on
// every page.
func pageSeries(t *testing.T, f ListFilters, limit int) []string {
	t.Helper()
	seriesID := 11
	cores := seriesPositionCores()
	pages := (len(cores) + limit - 1) / limit
	mockStore := mocks.NewMockStore(t)
	mockStore.EXPECT().GetBooksBySeriesIDCore(seriesID).Return(cores, nil).Times(pages)
	svc := NewAudiobookService(mockStore)

	var got []string
	for offset := 0; offset < len(cores); offset += limit {
		books, total, err := svc.GetAudiobooksWithTotal(context.Background(), limit, offset, "", nil, &seriesID, f)
		require.NoError(t, err)
		require.Equal(t, len(cores), total)
		got = append(got, idsInOrder(books)...)
	}
	return got
}

// TestSeriesPositionSortOrdersNumericallyAcrossPages: an explicit
// sort_by=series_position orders the whole series before paging, so pages of
// 4 concatenate to exactly the reading order.
func TestSeriesPositionSortOrdersNumericallyAcrossPages(t *testing.T) {
	got := pageSeries(t, ListFilters{SortBy: SortBySeriesPosition}, 4)
	require.Equal(t, seriesPositionAsc, got)
}

// TestSeriesPositionIsDefaultForSeriesIDListing: a series_id listing with no
// sort_by gets series_position order rather than whatever order the store
// getter happened to return.
func TestSeriesPositionIsDefaultForSeriesIDListing(t *testing.T) {
	got := pageSeries(t, ListFilters{}, 4)
	require.Equal(t, seriesPositionAsc, got)
}

// TestSeriesPositionSortDescendingKeepsMissingLast: descending reverses the
// positions only. Unnumbered books stay last and ties keep title-then-ID
// order, so a descending series view still ends with its loose books rather
// than opening with them.
func TestSeriesPositionSortDescendingKeepsMissingLast(t *testing.T) {
	got := pageSeries(t, ListFilters{SortBy: SortBySeriesPosition, SortOrder: "desc"}, 3)
	require.Equal(t, []string{
		"b-ten", "b-ten-dup", "b-three", "b-two-a", "b-two", "b-interlude", "b-one", "b-none-alpha", "b-none-beta",
	}, got)
}

// TestSeriesPositionPagesAreStableAcrossCalls: the same page asked for
// repeatedly returns the same rows in the same order.
func TestSeriesPositionPagesAreStableAcrossCalls(t *testing.T) {
	seriesID := 11
	mockStore := mocks.NewMockStore(t)
	mockStore.EXPECT().GetBooksBySeriesIDCore(seriesID).Return(seriesPositionCores(), nil).Times(4)
	svc := NewAudiobookService(mockStore)
	f := ListFilters{SortBy: SortBySeriesPosition}

	first, _, err := svc.GetAudiobooksWithTotal(context.Background(), 3, 3, "", nil, &seriesID, f)
	require.NoError(t, err)
	require.Equal(t, seriesPositionAsc[3:6], idsInOrder(first))
	for range 3 {
		again, _, err := svc.GetAudiobooksWithTotal(context.Background(), 3, 3, "", nil, &seriesID, f)
		require.NoError(t, err)
		require.Equal(t, idsInOrder(first), idsInOrder(again))
	}
}

// TestSeriesIDSearchWithoutSortKeepsMatchOrder pins a deliberate limit on the
// default: a series_id request that also carries a search and no sort_by
// keeps the search's own order (book-ID order on the no-index path, relevance
// with an index) rather than being switched to series_position. The web UI
// always sends an explicit sort, so this governs API callers only.
func TestSeriesIDSearchWithoutSortKeepsMatchOrder(t *testing.T) {
	seriesID := 11
	cores := seriesPositionCores()
	books := make([]database.Book, 0, len(cores))
	for _, c := range cores {
		books = append(books, database.Book{
			ID: c.ID, Title: c.Title, SeriesSequence: c.SeriesSequence, SeriesPositionRaw: c.SeriesPositionRaw,
		})
	}
	mockStore := mocks.NewMockStore(t)
	mockStore.EXPECT().GetBooksBySeriesIDCore(seriesID).Return(cores, nil).Once()
	mockStore.EXPECT().GetBooksByIDs(mock.Anything).Return(books, nil).Once()
	svc := NewAudiobookService(mockStore)

	got, total, err := svc.GetAudiobooksWithTotal(context.Background(), 20, 0, "t", nil, &seriesID, ListFilters{})
	require.NoError(t, err)
	require.Equal(t, 7, total)
	require.Equal(t, []string{
		"b-interlude", "b-none-beta", "b-ten", "b-ten-dup", "b-three", "b-two", "b-two-a",
	}, idsInOrder(got))
}

// storeOrderFilteredStore declares the filtered-summary capability, returns
// its rows in its own order (paged when asked for a page), and records every
// call. An unpaged call (limit <= 0 means "every match") fails the test: on a
// whole-library listing that is the 68K-row materialisation this guards.
type storeOrderFilteredStore struct {
	*mocks.MockStore
	t         *testing.T
	summaries []database.BookSummary
	calls     [][2]int
	filters   []database.BookSummaryFilter
}

func (s *storeOrderFilteredStore) GetAllBookSummariesFiltered(limit, offset int, f database.BookSummaryFilter) ([]database.BookSummary, error) {
	s.t.Helper()
	if limit <= 0 {
		s.t.Errorf("unpaged GetAllBookSummariesFiltered(%d, %d) with sort_by=%q: a whole-library fetch", limit, offset, f.SortBy)
	}
	s.calls = append(s.calls, [2]int{limit, offset})
	s.filters = append(s.filters, f)
	out := append([]database.BookSummary(nil), s.summaries...)
	if offset >= len(out) {
		return nil, nil
	}
	out = out[offset:]
	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}
	return out, nil
}

func (s *storeOrderFilteredStore) HonorsEveryBookSummaryFilter() {}

// TestSeriesPositionSortWithoutScopeNeverFetchesUnpaged: with no author_id or
// series_id, series_position is dropped (ScopedSort) and the listing takes the
// store's paged path in the default order. Every page asks the store for that
// page only, never for every match, and sort_order goes with the sort so the
// default order is not reversed.
//
// Until 2026-09-12 this test asserted the opposite: {0, 0} on every page,
// pinning a fetch-convert-sort of the whole library (about 61 MB at 68K rows)
// on every page request for an order that was mostly "every book 1 first".
func TestSeriesPositionSortWithoutScopeNeverFetchesUnpaged(t *testing.T) {
	for _, order := range []string{"", "asc", "desc"} {
		t.Run("order="+order, func(t *testing.T) {
			seq := func(n int) *int { return &n }
			store := &storeOrderFilteredStore{
				MockStore: mocks.NewMockStore(t),
				t:         t,
				summaries: []database.BookSummary{
					{ID: "s-10", Title: "Ten", SeriesSequence: seq(10)},
					{ID: "s-none", Title: "None"},
					{ID: "s-2", Title: "Two", SeriesSequence: seq(2)},
					{ID: "s-1", Title: "One", SeriesSequence: seq(1)},
					{ID: "s-3", Title: "Three", SeriesSequence: seq(3)},
				},
			}
			svc := NewAudiobookService(store)

			var got []string
			for offset := 0; offset < len(store.summaries); offset += 2 {
				books, _, err := svc.GetAudiobooksWithTotal(context.Background(), 2, offset, "", nil, nil,
					ListFilters{SortBy: SortBySeriesPosition, SortOrder: order})
				require.NoError(t, err)
				got = append(got, idsInOrder(books)...)
			}
			require.Equal(t, [][2]int{{2, 0}, {2, 2}, {2, 4}}, store.calls,
				"every page must ask the store for that page only")
			for _, f := range store.filters {
				require.Empty(t, f.SortBy, "series_position must not reach the store unscoped")
				require.True(t, f.SortAscending, "sort_order must be dropped with the sort")
			}
			require.Equal(t, []string{"s-10", "s-none", "s-2", "s-1", "s-3"}, got,
				"the store's default order, unsorted by the service")
		})
	}
}

// TestSeriesPositionSortKeptForAuthorIDListing: an author_id narrows the set
// the way a series_id does, and that listing already holds the whole scoped
// set to page it, so ScopedSort keeps series_position there.
func TestSeriesPositionSortKeptForAuthorIDListing(t *testing.T) {
	authorID := 7
	cores := seriesPositionCores()
	const limit = 4
	mockStore := mocks.NewMockStore(t)
	mockStore.EXPECT().GetBooksByAuthorIDCore(authorID).Return(cores, nil).Times((len(cores) + limit - 1) / limit)
	svc := NewAudiobookService(mockStore)

	var got []string
	for offset := 0; offset < len(cores); offset += limit {
		books, total, err := svc.GetAudiobooksWithTotal(context.Background(), limit, offset, "", &authorID, nil,
			ListFilters{SortBy: SortBySeriesPosition})
		require.NoError(t, err)
		require.Equal(t, len(cores), total)
		got = append(got, idsInOrder(books)...)
	}
	require.Equal(t, seriesPositionAsc, got)
}

// TestScopedSort pins the rule the service applies and the handler reports.
func TestScopedSort(t *testing.T) {
	id := 1
	cases := []struct {
		name             string
		sortBy, order    string
		authorID, series *int
		wantBy, wantOrd  string
	}{
		{"unscoped series_position is dropped with its order", SortBySeriesPosition, "desc", nil, nil, "", ""},
		{"series_id keeps it", SortBySeriesPosition, "desc", nil, &id, SortBySeriesPosition, "desc"},
		{"author_id keeps it", SortBySeriesPosition, "asc", &id, nil, SortBySeriesPosition, "asc"},
		{"other keys are untouched", "title", "desc", nil, nil, "title", "desc"},
		{"no sort stays no sort", "", "", nil, nil, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			by, ord := ScopedSort(tc.sortBy, tc.order, tc.authorID, tc.series)
			require.Equal(t, tc.wantBy, by)
			require.Equal(t, tc.wantOrd, ord)
		})
	}
}
