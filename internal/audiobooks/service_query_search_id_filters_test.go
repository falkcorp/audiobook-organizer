// file: internal/audiobooks/service_query_search_id_filters_test.go
// version: 1.0.0
// guid: de1c4eca-5371-4c44-9c6e-8a7d61b37aeb
// last-edited: 2026-09-12

// Regression tests for author_id / series_id surviving a search, and for
// author_id + series_id intersecting instead of author winning.
//
// Before the fix GetAudiobooksWithTotal picked exactly ONE base set — search,
// else author, else series — and nothing downstream re-applied the ids it had
// passed over. So GET /api/v1/audiobooks?series_id=N&search=foo answered from
// the whole library, and author_id silently overrode series_id when both were
// sent. The library page sends series_id from a book-detail link, which made
// "search inside this series" user-reachable.
//
// The fixture is built so each wrong answer is distinguishable from the right
// one:
//   - every book matches the search, so "search ignored the id" returns all 12;
//   - author B is a SECONDARY credit only (book_authors position 1) and is
//     never any book's Book.AuthorID, so a post-filter that read the
//     denormalized AuthorID field instead of the junction returns 0;
//   - author B's books only partially overlap series S1, so "author wins"
//     returns 6 where the intersection is 4.

package audiobooks

import (
	"context"
	"sort"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

const idFilterSearch = "author:sanderson"

type idFilterFixture struct {
	svc       *AudiobookService
	all       []string
	authorA   int // primary credit on every book
	authorB   int // secondary credit on even-indexed books only
	seriesS1  int // books with i%3 != 0
	seriesS2  int // books with i%3 == 0
	inB       map[string]bool
	inS1      map[string]bool
	bookCount int
}

func newIDFilterFixture(t *testing.T) *idFilterFixture {
	t.Helper()
	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	ps.WaitForWarmup()

	a, err := ps.CreateAuthor("Author A")
	require.NoError(t, err)
	b, err := ps.CreateAuthor("Author B")
	require.NoError(t, err)
	s1, err := ps.CreateSeries("Series One", &a.ID)
	require.NoError(t, err)
	s2, err := ps.CreateSeries("Series Two", &a.ID)
	require.NoError(t, err)

	fx := &idFilterFixture{
		authorA: a.ID, authorB: b.ID, seriesS1: s1.ID, seriesS2: s2.ID,
		inB: map[string]bool{}, inS1: map[string]bool{}, bookCount: 12,
	}
	for i := range fx.bookCount {
		seriesID := s1.ID
		if i%3 == 0 {
			seriesID = s2.ID
		}
		created, err := ps.CreateBook(&database.Book{
			Title:    "idfilter book",
			AuthorID: &a.ID,
			SeriesID: &seriesID,
		})
		require.NoError(t, err)
		credits := []database.BookAuthor{{BookID: created.ID, AuthorID: a.ID, Role: "author", Position: 0}}
		if i%2 == 0 {
			credits = append(credits, database.BookAuthor{BookID: created.ID, AuthorID: b.ID, Role: "co-author", Position: 1})
			fx.inB[created.ID] = true
		}
		require.NoError(t, ps.SetBookAuthors(created.ID, credits))
		if seriesID == s1.ID {
			fx.inS1[created.ID] = true
		}
		fx.all = append(fx.all, created.ID)
	}

	// Guard the fixture, not the service: if the junction write never reached
	// the store's author getter, every assertion below would read as a service
	// bug. Author B must be visible through the same getter the listing uses.
	bCore, err := ps.GetBooksByAuthorIDCore(b.ID)
	require.NoError(t, err)
	require.Len(t, bCore, len(fx.inB), "fixture: secondary-author credits not visible via GetBooksByAuthorIDCore")
	for _, bc := range bCore {
		require.True(t, fx.inB[bc.ID], "fixture: unexpected book %s credited to author B", bc.ID)
		require.NotNil(t, bc.AuthorID)
		require.Equal(t, a.ID, *bc.AuthorID, "fixture: author B must never be the denormalized Book.AuthorID")
	}

	svc := NewAudiobookService(ps)
	svc.SetSearchIndex(buildSearchTestIndex(t, fx.all...))
	fx.svc = svc
	return fx
}

func (fx *idFilterFixture) want(pick func(id string) bool) []string {
	out := []string{}
	for _, id := range fx.all {
		if pick(id) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func bookIDsSorted(books []database.Book) []string {
	out := make([]string, 0, len(books))
	for _, b := range books {
		out = append(out, b.ID)
	}
	sort.Strings(out)
	return out
}

// TestSearchKeepsAuthorAndSeriesIDFilters is the core regression. Each case
// asserts the listing equals the expected set AND that the reported total
// equals the listing length — the handler reports that total as "count", so a
// disagreement would render a wrong "N results".
func TestSearchKeepsAuthorAndSeriesIDFilters(t *testing.T) {
	fx := newIDFilterFixture(t)
	primary := true
	intp := func(v int) *int { return &v }

	cases := []struct {
		name     string
		search   string
		authorID *int
		seriesID *int
		filters  ListFilters
		pick     func(id string) bool
	}{
		{
			name: "search + series_id", search: idFilterSearch, seriesID: intp(fx.seriesS1),
			pick: func(id string) bool { return fx.inS1[id] },
		},
		{
			name: "search + series_id + is_primary_version", search: idFilterSearch, seriesID: intp(fx.seriesS1),
			filters: ListFilters{IsPrimaryVersion: &primary},
			pick:    func(id string) bool { return fx.inS1[id] },
		},
		{
			name: "search + secondary author_id", search: idFilterSearch, authorID: intp(fx.authorB),
			pick: func(id string) bool { return fx.inB[id] },
		},
		{
			name: "search + author_id + series_id", search: idFilterSearch,
			authorID: intp(fx.authorB), seriesID: intp(fx.seriesS1),
			pick: func(id string) bool { return fx.inB[id] && fx.inS1[id] },
		},
		{
			name: "search + author_id + the other series_id", search: idFilterSearch,
			authorID: intp(fx.authorB), seriesID: intp(fx.seriesS2),
			pick: func(id string) bool { return fx.inB[id] && !fx.inS1[id] },
		},
		{
			name:     "author_id + series_id without search intersects",
			authorID: intp(fx.authorB), seriesID: intp(fx.seriesS1),
			pick: func(id string) bool { return fx.inB[id] && fx.inS1[id] },
		},
		{
			name:     "author_id + series_id + is_primary_version without search",
			authorID: intp(fx.authorB), seriesID: intp(fx.seriesS1),
			filters: ListFilters{IsPrimaryVersion: &primary},
			pick:    func(id string) bool { return fx.inB[id] && fx.inS1[id] },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := fx.want(tc.pick)
			// The expected set must be a PROPER, non-empty subset of the
			// library, or the case cannot tell a dropped filter from an applied
			// one.
			require.NotEmpty(t, want)
			require.Less(t, len(want), fx.bookCount)

			got, gotTotal, err := fx.svc.GetAudiobooksWithTotal(
				context.Background(), 100, 0, tc.search, tc.authorID, tc.seriesID, tc.filters)
			require.NoError(t, err)
			require.Equal(t, want, bookIDsSorted(got),
				"listing must be the id-filtered set, not the whole search/author result")
			require.Equal(t, len(got), gotTotal, "reported total must equal the listing length")
		})
	}
}

// TestSearchWithSeriesIDPagesAcrossPages pins the over-fetch half of the fix.
// A post-filter applied to one page that Bleve already cut deletes most of
// that page with nothing to refill it, and paginateFilteredBooks then
// re-slices the remainder by the original offset, so page 2 comes back empty.
// No other post-filter is sent here: the id filter ALONE must trigger the
// over-fetch.
func TestSearchWithSeriesIDPagesAcrossPages(t *testing.T) {
	fx := newIDFilterFixture(t)
	s1 := fx.seriesS1
	want := fx.want(func(id string) bool { return fx.inS1[id] })
	const limit = 5
	require.Greater(t, len(want), limit, "need more matches than one page")
	require.Less(t, len(want), 2*limit, "page 2 should be a partial page")

	seen := map[string]bool{}
	var union []string
	for _, page := range []struct{ offset, wantLen int }{
		{0, limit},
		{limit, len(want) - limit},
		{2 * limit, 0},
	} {
		got, gotTotal, err := fx.svc.GetAudiobooksWithTotal(
			context.Background(), limit, page.offset, idFilterSearch, nil, &s1, ListFilters{})
		require.NoError(t, err)
		require.Len(t, got, page.wantLen, "offset=%d: short or empty page is the post-filter-after-pagination bug", page.offset)
		require.Equal(t, len(want), gotTotal, "offset=%d: total must be the filtered match count", page.offset)
		for _, b := range got {
			require.False(t, seen[b.ID], "book %s returned on two pages", b.ID)
			seen[b.ID] = true
			union = append(union, b.ID)
		}
	}
	sort.Strings(union)
	require.Equal(t, want, union, "pages together must be exactly the filtered set")
}
