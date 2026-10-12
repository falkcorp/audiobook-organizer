// file: internal/audiobooks/service_fieldfilter_authorseries_test.go
// version: 1.0.2
// guid: 7d4c2e91-8a3f-4b6d-9e5c-1f2a3b4c5d6e
// last-edited: 2026-10-12

package audiobooks

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// TODO item 16b regression test: a FieldFilter{Field:"author"} or
// {Field:"series"} on the Library listing always returned 0 books,
// independent of sort. Root cause: the field matcher (fieldMatchesCompiled in filter_compiled.go)
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

// b5SeedCoAuthoredBook adds a book whose primary is authorA and whose second
// credit is authorB, in credit order, to the fixture's store.
func b5SeedCoAuthoredBook(t *testing.T, ps *database.PebbleStore, fx b5AuthorSeriesFixture) string {
	t.Helper()
	book, err := ps.CreateBook(&database.Book{Title: "Synthetic Co-Authored", AuthorID: &fx.authorAID})
	require.NoError(t, err)
	require.NoError(t, ps.SetBookAuthors(book.ID, []database.BookAuthor{
		{BookID: book.ID, AuthorID: fx.authorAID, Role: "author", Position: 0},
		{BookID: book.ID, AuthorID: fx.authorBID, Role: "co-author", Position: 1},
	}))
	return book.ID
}

func b5AuthorFilterIDs(t *testing.T, svc *AudiobookService, ff FieldFilter) []string {
	t.Helper()
	got, err := svc.GetAudiobooks(context.Background(), 1000, 0, "", nil, nil, ListFilters{FieldFilters: []FieldFilter{ff}})
	require.NoError(t, err)
	ids := make([]string, 0, len(got))
	for _, b := range got {
		ids = append(ids, b.ID)
	}
	return ids
}

func b5AuthorFilterSvc(t *testing.T) (*AudiobookService, b5AuthorSeriesFixture, string) {
	t.Helper()
	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	ps.WaitForWarmup()
	fx := b5SeedAuthorSeriesBooks(t, ps)
	co := b5SeedCoAuthoredBook(t, ps, fx)
	return NewAudiobookService(ps), fx, co
}

// author:<second credit> matches the co-authored book, and still matches
// the book that credits that author as its only author.
func TestAuthorFilter_MatchesSecondCredit(t *testing.T) {
	svc, fx, co := b5AuthorFilterSvc(t)
	ids := b5AuthorFilterIDs(t, svc, FieldFilter{Field: "author", Value: "Herbert"})
	require.ElementsMatch(t, []string{fx.bookBID, co}, ids)
	count, err := svc.CountAudiobooksFiltered(context.Background(), ListFilters{
		FieldFilters: []FieldFilter{{Field: "author", Value: "Herbert"}},
	})
	require.NoError(t, err)
	require.Equal(t, 2, count)
}

func TestAuthorFilter_PrimaryStillMatches(t *testing.T) {
	svc, fx, co := b5AuthorFilterSvc(t)
	ids := b5AuthorFilterIDs(t, svc, FieldFilter{Field: "author", Value: "Le Guin"})
	require.ElementsMatch(t, []string{fx.bookAID, co}, ids)
}

// -author:<second credit> excludes the co-authored book: a negated filter
// holds only when NO credit matches.
func TestAuthorFilter_NegationRequiresNoCreditMatch(t *testing.T) {
	svc, fx, co := b5AuthorFilterSvc(t)
	ids := b5AuthorFilterIDs(t, svc, FieldFilter{Field: "author", Value: "Herbert", Negated: true})
	require.Equal(t, []string{fx.bookAID}, ids)
	require.NotContains(t, ids, co)
}

// A book with no junction rows behaves exactly as before: its primary author
// (resolved from AuthorID) is the only value.
func TestAuthorFilter_NoCreditsFallsBackToPrimary(t *testing.T) {
	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	ps.WaitForWarmup()
	fx := b5SeedAuthorSeriesBooks(t, ps)
	bare, err := ps.CreateBook(&database.Book{Title: "Synthetic No Credits", AuthorID: &fx.authorBID})
	require.NoError(t, err)
	svc := NewAudiobookService(ps)

	ids := b5AuthorFilterIDs(t, svc, FieldFilter{Field: "author", Value: "Herbert"})
	require.ElementsMatch(t, []string{fx.bookBID, bare.ID}, ids)
	require.NotContains(t, b5AuthorFilterIDs(t, svc, FieldFilter{Field: "author", Value: "Le Guin"}), bare.ID)
}

// A regex is tried against each name on its own: author:^A B$ must not match
// a book credited to A and B.
func TestAuthorFilter_RegexDoesNotSpanNames(t *testing.T) {
	credits := func(id string) []string {
		if id == "co" {
			return []string{"Synthetic A", "Synthetic B"}
		}
		return nil
	}
	book := database.Book{ID: "co", Author: &database.Author{Name: "Synthetic A"}}
	match := func(value string) bool {
		cf, err := compileFieldFilter(FieldFilter{Field: "author", Value: value})
		require.NoError(t, err)
		return fieldMatchesCompiled(&book, &cf, nil, credits)
	}
	require.False(t, match("/^Synthetic A Synthetic B$/"), "a regex must not see the names joined")
	require.False(t, match("/^Synthetic A, Synthetic B$/"))
	require.True(t, match("/^Synthetic B$/"), "an anchored regex matches the second credit alone")
	require.True(t, match("/^Synthetic A$/"))

	vals, known := bookFieldValues(book, "author", credits)
	require.True(t, known)
	require.Equal(t, []string{"Synthetic A", "Synthetic B"}, vals, "primary first, credit order kept, no duplicate")
}
