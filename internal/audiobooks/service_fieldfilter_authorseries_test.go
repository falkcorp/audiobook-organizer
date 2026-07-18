// file: internal/audiobooks/service_fieldfilter_authorseries_test.go
// version: 1.0.0
// guid: 7d4c2e91-8a3f-4b6d-9e5c-1f2a3b4c5d6e
// last-edited: 2026-07-18

package audiobooks

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// TODO item 16b regression test: a FieldFilter{Field:"author"} or
// {Field:"series"} on the Library listing always returned 0 books,
// independent of sort. Root cause: fieldMatchesValue (service_filtering.go)
// reads book.Author.Name / book.Series.Name, but those joined struct fields
// are populated ONLY via joins (see database.Book's "Related objects" doc
// comment) — the memdb-resident *Book used by the production pushdown path
// never carries them, only AuthorID/SeriesID. So every author/series
// FieldFilter compared against "" and rejected every row.
//
// This harness uses a real PebbleStore (like service_filtering_pushdown_test.go)
// so the production memdb pushdown path is actually exercised — a mock store
// would bypass the bug entirely.

// b5AuthorSeriesFixture seeds two authors (each with a series) and one
// author-only book, so a FieldFilter can discriminate between authors/series
// instead of just "matched something".
type b5AuthorSeriesFixture struct {
	authorAID, authorBID int
	seriesAID            int
	bookAID, bookBID     string // bookA: author A + series A; bookB: author B, no series
}

func b5SeedAuthorSeriesBooks(t *testing.T, ps *database.PebbleStore) b5AuthorSeriesFixture {
	t.Helper()

	authorA, err := ps.CreateAuthor("Ursula Le Guin")
	require.NoError(t, err)
	authorB, err := ps.CreateAuthor("Frank Herbert")
	require.NoError(t, err)

	seriesA, err := ps.CreateSeries("Earthsea", &authorA.ID)
	require.NoError(t, err)

	bookA, err := ps.CreateBook(&database.Book{
		Title:    "A Wizard of Earthsea",
		AuthorID: &authorA.ID,
		SeriesID: &seriesA.ID,
	})
	require.NoError(t, err)
	require.NoError(t, ps.SetBookAuthors(bookA.ID, []database.BookAuthor{
		{BookID: bookA.ID, AuthorID: authorA.ID, Role: "author", Position: 0},
	}))

	bookB, err := ps.CreateBook(&database.Book{
		Title:    "Dune",
		AuthorID: &authorB.ID,
	})
	require.NoError(t, err)
	require.NoError(t, ps.SetBookAuthors(bookB.ID, []database.BookAuthor{
		{BookID: bookB.ID, AuthorID: authorB.ID, Role: "author", Position: 0},
	}))

	return b5AuthorSeriesFixture{
		authorAID: authorA.ID,
		authorBID: authorB.ID,
		seriesAID: seriesA.ID,
		bookAID:   bookA.ID,
		bookBID:   bookB.ID,
	}
}

// TestFieldFilterAuthor_MemdbHydration proves an author-name FieldFilter
// returns exactly the matching book(s) via the memdb-resident *Book path
// (book.Author is never persisted; only AuthorID is), not zero regardless of
// which author is searched.
func TestFieldFilterAuthor_MemdbHydration(t *testing.T) {
	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	ps.WaitForWarmup()

	fx := b5SeedAuthorSeriesBooks(t, ps)
	svc := NewAudiobookService(ps)
	ctx := context.Background()

	// Positive: filtering by "Le Guin" must return exactly bookA, not bookB.
	got, err := svc.GetAudiobooks(ctx, 1000, 0, "", nil, nil, ListFilters{
		FieldFilters: []FieldFilter{{Field: "author", Value: "Le Guin"}},
	})
	require.NoError(t, err)
	require.Len(t, got, 1, "author FieldFilter must return exactly the matching book, not 0 or all")
	require.Equal(t, fx.bookAID, got[0].ID)

	// The count pushdown shares buildBookSummaryFilterWithLookupCount, so it
	// must agree with the list result (previously: list=0 AND count=0).
	count, err := svc.CountAudiobooksFiltered(ctx, ListFilters{
		FieldFilters: []FieldFilter{{Field: "author", Value: "Le Guin"}},
	})
	require.NoError(t, err)
	require.Equal(t, 1, count)

	// Discriminating positive: filtering by the OTHER author must return
	// exactly bookB, proving the filter isn't just matching everything.
	got, err = svc.GetAudiobooks(ctx, 1000, 0, "", nil, nil, ListFilters{
		FieldFilters: []FieldFilter{{Field: "author", Value: "Herbert"}},
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, fx.bookBID, got[0].ID)

	// Negative: an author name that matches nothing must return 0 books, not
	// silently match everything.
	got, err = svc.GetAudiobooks(ctx, 1000, 0, "", nil, nil, ListFilters{
		FieldFilters: []FieldFilter{{Field: "author", Value: "NoSuchAuthorXYZ"}},
	})
	require.NoError(t, err)
	require.Empty(t, got)
}

// TestFieldFilterSeries_MemdbHydration is the series-field analogue.
func TestFieldFilterSeries_MemdbHydration(t *testing.T) {
	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	ps.WaitForWarmup()

	fx := b5SeedAuthorSeriesBooks(t, ps)
	svc := NewAudiobookService(ps)
	ctx := context.Background()

	// Positive: only bookA carries a series (Earthsea); bookB has none.
	got, err := svc.GetAudiobooks(ctx, 1000, 0, "", nil, nil, ListFilters{
		FieldFilters: []FieldFilter{{Field: "series", Value: "Earthsea"}},
	})
	require.NoError(t, err)
	require.Len(t, got, 1, "series FieldFilter must return exactly the matching book")
	require.Equal(t, fx.bookAID, got[0].ID)

	count, err := svc.CountAudiobooksFiltered(ctx, ListFilters{
		FieldFilters: []FieldFilter{{Field: "series", Value: "Earthsea"}},
	})
	require.NoError(t, err)
	require.Equal(t, 1, count)

	// Negative: a series name that matches nothing must return 0 books.
	got, err = svc.GetAudiobooks(ctx, 1000, 0, "", nil, nil, ListFilters{
		FieldFilters: []FieldFilter{{Field: "series", Value: "NoSuchSeriesXYZ"}},
	})
	require.NoError(t, err)
	require.Empty(t, got)
}
