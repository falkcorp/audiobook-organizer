// file: internal/audiobooks/sort_page_pushdown_test.go
// version: 1.0.0
// guid: b4d7e21c-5f38-4a90-8c16-2e7d93f5a4b1
// last-edited: 2026-08-25

package audiobooks

import (
	"context"
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// A sorted request must ask the store for the PAGE, not for the whole library.
//
// The store orders the match set and paginates it, so pulling the full set back
// here costs twice: the store materialises every match to sort it, and
// bookSummariesToBooks then rebuilds every one as a database.Book. Book is 904
// bytes against BookSummary's 240 -- ~61 MB of Book values for a 68K-row
// library, to then discard all but `limit` of them.
//
// Asserting the returned ORDER cannot catch a regression here: fetching
// everything and paginating afterwards produces the same rows. Only the
// arguments the store was called with distinguish them, which is what this spy
// records.

// spg* names are task-unique per repo convention for package-shared helpers.

// spgSpyStore forwards everything to a real store but records the page geometry
// the service asked for. Embedding satisfies the rest of the store interface;
// database.AsCapability type-asserts the outermost value first, so this
// override is the one the service reaches.
type spgSpyStore struct {
	*database.PebbleStore
	gotLimit  int
	gotOffset int
	calls     int
}

func (s *spgSpyStore) GetAllBookSummariesFiltered(limit, offset int, f database.BookSummaryFilter) ([]database.BookSummary, error) {
	s.calls++
	s.gotLimit, s.gotOffset = limit, offset
	return s.PebbleStore.GetAllBookSummariesFiltered(limit, offset, f)
}

func spgSeed(t *testing.T, ps *database.PebbleStore, n int) map[string]string {
	t.Helper()
	marks := make(map[string]string, n)
	// Insert in an order that is neither ascending nor descending by genre.
	for _, rank := range []int{3, 0, 5, 1, 4, 2} {
		if rank >= n {
			continue
		}
		primary := true
		g := fmt.Sprintf("genre-%d", rank)
		created, err := ps.CreateBook(&database.Book{
			Title:            fmt.Sprintf("b-%d", rank),
			FilePath:         fmt.Sprintf("/tmp/spg_%d.m4b", rank),
			IsPrimaryVersion: &primary,
			Genre:            &g,
		})
		require.NoError(t, err)
		marks[created.ID] = fmt.Sprintf("r%d", rank)
	}
	ps.WaitForWarmup()
	return marks
}

func TestSortedRequestAsksTheStoreForOnePage(t *testing.T) {
	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	ps.WaitForWarmup()
	marks := spgSeed(t, ps, 6)

	spy := &spgSpyStore{PebbleStore: ps}
	svc := NewAudiobookService(spy)

	got, err := svc.GetAudiobooks(context.Background(), 2, 4, "", nil, nil,
		ListFilters{SortBy: "genre", SortOrder: "asc"})
	require.NoError(t, err)
	require.Positive(t, spy.calls, "the spy must actually be on the path, or this test proves nothing")

	require.Equal(t, 2, spy.gotLimit,
		"the store was asked for %d rows; a sorted page must be requested as a page, not as the whole library", spy.gotLimit)
	require.Equal(t, 4, spy.gotOffset, "offset must reach the store too")

	// And the page is still the right slice of the ORDERED set.
	order := make([]string, 0, len(got))
	for _, b := range got {
		order = append(order, marks[b.ID])
	}
	require.Equal(t, []string{"r4", "r5"}, order)
}

// TestUnknownSortStillFetchesTheFullSet is the negative control. Without it,
// the assertion above would also pass if the service simply always forwarded
// limit/offset -- including on the path where it must NOT, because it still
// owes the caller an in-memory pass.
func TestUnknownSortStillFetchesTheFullSet(t *testing.T) {
	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	ps.WaitForWarmup()
	spgSeed(t, ps, 6)

	spy := &spgSpyStore{PebbleStore: ps}
	svc := NewAudiobookService(spy)

	// SortBooks has no comparator for this, so bsf carries no sort and the
	// store cannot have ordered anything.
	_, err = svc.GetAudiobooks(context.Background(), 2, 0, "", nil, nil,
		ListFilters{SortBy: "not_a_sortable_field", SortOrder: "asc"})
	require.NoError(t, err)
	require.Positive(t, spy.calls)
	require.Equal(t, 0, spy.gotLimit,
		"an unsortable key must still pull the full set: the service owes an in-memory pass the store cannot do")
}
