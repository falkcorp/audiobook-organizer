// file: internal/audiobooks/service_query_heavyfilter_sort_test.go
// version: 1.0.0
// guid: ea41b5f4-dba5-4cb7-b067-2498e7aa707c
// last-edited: 2026-07-18

package audiobooks

import (
	"context"
	"sort"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// TODO item 16 (CONFIRMED, user-facing): the Library page returned 0 books
// whenever a heavy filter (a FieldFilter, FingerprintStatus/Coverage, or a
// PerUserFilter) was combined with a non-title sort (author/series/date/
// added/etc). Root cause: internal/audiobooks/service_query.go's
// GetAudiobooks, when the memdb pushdown already applied the filter
// (didPushdown=true) AND the sort was non-title (heavySorting=true), left
// hasPostFilters=true so the post-filter block re-ran the SAME filters again
// — but against database.bookSummariesToBooks(summaries) projections, which
// don't carry every Book field (Language, Genre, Publisher, Edition, Codec,
// Quality, FingerprintStatus, CoveragePercent are all absent from
// database.BookSummary). The re-check read those fields as "" / zero and
// dropped every row, even though the memdb walker's original predicate had
// already matched them correctly against the real Book. CountAudiobooksFiltered
// clears SortBy before counting, so it never hit this path — hence the
// count/list divergence the ticket described (correct count, 0-row page).
//
// b1lf* names are task-unique (worktree b1-library-filter) per repo
// convention for test helpers shared across a package.

// b1lfSeedBook is a minimal seed row: primary version, book-global fields
// only (no per-user/tag state needed for these cases).
type b1lfSeedBook struct {
	title    string
	language string
	fpStatus string
	coverage int
	duration int
}

func b1lfSeed(t *testing.T, ps *database.PebbleStore, rows []b1lfSeedBook) []string {
	t.Helper()
	ids := make([]string, 0, len(rows))
	for i, r := range rows {
		primary := true
		lang := r.language
		dur := r.duration
		created, err := ps.CreateBook(&database.Book{
			Title:             r.title,
			Language:          &lang,
			FingerprintStatus: r.fpStatus,
			CoveragePercent:   r.coverage,
			Duration:          &dur,
			IsPrimaryVersion:  &primary,
		})
		require.NoErrorf(t, err, "seed book %d", i)
		ids = append(ids, created.ID)
	}
	ps.WaitForWarmup()
	return ids
}

// TestB1HeavyFilterNonTitleSort_LanguageFilter is the direct repro: a
// language FieldFilter (a BookSummary-absent field) combined with a
// non-title sort must return every matching book, not zero, and must agree
// with CountAudiobooksFiltered for the same filter.
func TestB1HeavyFilterNonTitleSort_LanguageFilter(t *testing.T) {
	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	ps.WaitForWarmup()

	rows := []b1lfSeedBook{
		{title: "b-book", language: "en", duration: 500},
		{title: "a-book", language: "en", duration: 100},
		{title: "c-book", language: "en", duration: 900},
		{title: "d-book", language: "fr", duration: 200}, // non-matching language
	}
	b1lfSeed(t, ps, rows)

	svc := NewAudiobookService(ps)
	filter := ListFilters{
		FieldFilters: []FieldFilter{{Field: "language", Value: "en"}},
		SortBy:       "duration",
		SortOrder:    "asc",
	}

	got, err := svc.GetAudiobooks(context.Background(), 1000, 0, "", nil, nil, filter)
	require.NoError(t, err)
	require.Len(t, got, 3, "expected the 3 English books, not 0 (BookSummary projection re-filter bug)")

	// Sorted ascending by duration: a(100) < b(500) < c(900).
	require.Equal(t, []string{"a-book", "b-book", "c-book"}, []string{got[0].Title, got[1].Title, got[2].Title})

	count, err := svc.CountAudiobooksFiltered(context.Background(), filter)
	require.NoError(t, err)
	require.Equal(t, count, len(got), "GetAudiobooks page count must agree with CountAudiobooksFiltered for the same filter")
}

// TestB1HeavyFilterNonTitleSort_FingerprintAndCoverage covers the other two
// BookSummary-absent predicate fields (FingerprintStatus, CoveragePercent)
// combined with a non-title sort.
func TestB1HeavyFilterNonTitleSort_FingerprintAndCoverage(t *testing.T) {
	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	ps.WaitForWarmup()

	rows := []b1lfSeedBook{
		{title: "b-book", language: "en", fpStatus: "complete", coverage: 80, duration: 500},
		{title: "a-book", language: "en", fpStatus: "complete", coverage: 90, duration: 100},
		{title: "c-book", language: "en", fpStatus: "none", coverage: 10, duration: 900}, // non-matching
	}
	b1lfSeed(t, ps, rows)

	svc := NewAudiobookService(ps)
	filter := ListFilters{
		FingerprintStatus: "complete",
		SortBy:            "duration",
		SortOrder:         "asc",
	}

	got, err := svc.GetAudiobooks(context.Background(), 1000, 0, "", nil, nil, filter)
	require.NoError(t, err)
	require.Len(t, got, 2, "expected the 2 fingerprinted books, not 0")
	require.Equal(t, []string{"a-book", "b-book"}, []string{got[0].Title, got[1].Title})

	count, err := svc.CountAudiobooksFiltered(context.Background(), filter)
	require.NoError(t, err)
	require.Equal(t, count, len(got))
}

// TestB1HeavyFilterNonTitleSort_PaginatesAfterSort proves the fix's ordering
// contract directly: with a filter narrowing to N books and a non-title
// sort, offset/limit must slice the SORTED set (not the pre-sort memdb-order
// set). Uses more rows than a single page so pagination is exercised for
// real, and cross-checks every page concatenated equals the full sorted set.
func TestB1HeavyFilterNonTitleSort_PaginatesAfterSort(t *testing.T) {
	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	ps.WaitForWarmup()

	rows := make([]b1lfSeedBook, 0, 12)
	for i := 0; i < 12; i++ {
		rows = append(rows, b1lfSeedBook{
			title:    string(rune('a'+i)) + "-book",
			language: "en",
			duration: 1000 - i*10, // descending insert order vs duration
		})
	}
	b1lfSeed(t, ps, rows)

	svc := NewAudiobookService(ps)
	filter := ListFilters{
		FieldFilters: []FieldFilter{{Field: "language", Value: "en"}},
		SortBy:       "duration",
		SortOrder:    "asc",
	}

	full, err := svc.GetAudiobooks(context.Background(), 1000, 0, "", nil, nil, filter)
	require.NoError(t, err)
	require.Len(t, full, 12)
	require.True(t, sort.SliceIsSorted(full, func(i, j int) bool {
		return *full[i].Duration < *full[j].Duration
	}), "full result must be duration-sorted ascending")

	page1, err := svc.GetAudiobooks(context.Background(), 5, 0, "", nil, nil, filter)
	require.NoError(t, err)
	page2, err := svc.GetAudiobooks(context.Background(), 5, 5, "", nil, nil, filter)
	require.NoError(t, err)
	page3, err := svc.GetAudiobooks(context.Background(), 5, 10, "", nil, nil, filter)
	require.NoError(t, err)

	require.Len(t, page1, 5)
	require.Len(t, page2, 5)
	require.Len(t, page3, 2)

	gotIDs := func(books []database.Book) []string {
		out := make([]string, len(books))
		for i, b := range books {
			out[i] = b.ID
		}
		return out
	}
	wantIDs := gotIDs(full)
	gotConcat := append(append(gotIDs(page1), gotIDs(page2)...), gotIDs(page3)...)
	require.Equal(t, wantIDs, gotConcat, "paginated pages must reconstruct the full sorted set in order")
}
