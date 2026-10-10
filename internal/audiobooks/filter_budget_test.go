// file: internal/audiobooks/filter_budget_test.go
// version: 1.0.0
// guid: 87f0a067-4601-427f-9ecf-398a030c9059
// last-edited: 2026-10-10

package audiobooks

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/querygrammar"
)

// slowToken is allowed by the size limits (95 instructions) but costs about
// 80 µs per 85-character title: \pL is one instruction and an expensive one.
const slowToken = `/(?:\pL?){45}zzz/`

// threeSlowTokens evaluates all three tokens on every row and keeps none:
// the two negated ones pass (nothing matches zzz) and the third rejects, so
// the walk can never stop early at a full page.
func threeSlowTokens() []FieldFilter {
	return []FieldFilter{
		{Field: "title", Value: slowToken, Negated: true},
		{Field: "title", Value: slowToken, Negated: true},
		{Field: "title", Value: slowToken},
	}
}

// longTitleStore is a real store of n books with 85-character titles.
func longTitleStore(t *testing.T, n int) *AudiobookService {
	t.Helper()
	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	ps.WaitForWarmup()
	// Concurrent creates share Pebble's commit syncs: sequentially, 1,000
	// books took 21 s here.
	var g errgroup.Group
	g.SetLimit(16)
	for i := range n {
		g.Go(func() error {
			title := fmt.Sprintf("%-85s", fmt.Sprintf("The Long Title Of Book Number %06d In A Long Series", i))
			_, err := ps.CreateBook(&database.Book{Title: title, FilePath: fmt.Sprintf("/synthetic/%06d.m4b", i)})
			return err
		})
	}
	require.NoError(t, g.Wait())
	return NewAudiobookService(ps)
}

// TestLibraryBudget_PredicateChargesEveryMatch: the pushdown predicate times
// every regex/glob match against the scan's budget, so three tokens spend it
// three times as fast as one. With each match costing exactly 1 ms, a 10 ms
// budget allows exactly 10 matches (three rows and one token into the
// fourth), after which no token runs again and Err reports the stop.
func TestLibraryBudget_PredicateChargesEveryMatch(t *testing.T) {
	svc := NewAudiobookService(mocks.NewMockStore(t))
	var mu sync.Mutex
	clock, reads := time.Unix(0, 0), 0
	budget := querygrammar.NewBudgetClock(10*time.Millisecond, func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		reads++
		clock = clock.Add(time.Millisecond)
		return clock
	})
	bsf, ok := svc.buildBookSummaryFilter(ListFilters{FieldFilters: threeSlowTokens()}, true, budget)
	require.True(t, ok)
	kept := 0
	for i := range 100 {
		if bsf.Predicate(&database.Book{ID: fmt.Sprint(i), Title: "A Title"}) {
			kept++
		}
	}
	require.Equal(t, 20, reads, "10 timed matches (two clock reads each), then none")
	require.Zero(t, kept)
	var slow *querygrammar.TooSlowError
	require.ErrorAs(t, budget.Err(), &slow)
}

// TestLibraryBudget_ListAndCountStop: through the service, several slow
// tokens stop the list and the count at about the budget with a
// *TooSlowError and no rows, never a partial page or a short count. A
// substring search is not timed at all.
func TestLibraryBudget_ListAndCountStop(t *testing.T) {
	if testing.Short() {
		t.Skip("creates 1,000 books")
	}
	setup := time.Now()
	svc := longTitleStore(t, 1000)
	t.Logf("store: %s", time.Since(setup))
	saved := libraryPatternBudget
	libraryPatternBudget = 30 * time.Millisecond
	t.Cleanup(func() { libraryPatternBudget = saved })
	f := ListFilters{FieldFilters: threeSlowTokens()}

	start := time.Now()
	books, _, err := svc.GetAudiobooksWithTotal(context.Background(), 50, 0, "", nil, nil, f)
	took := time.Since(start)
	t.Logf("list: stopped after %s (pattern budget %s)", took, libraryPatternBudget)
	var slow *querygrammar.TooSlowError
	require.ErrorAs(t, err, &slow)
	require.Nil(t, books)
	require.Less(t, took, libraryPatternBudget+2*time.Second, "loose: -race and load")

	start = time.Now()
	n, err := svc.CountAudiobooksFiltered(context.Background(), f)
	t.Logf("count: stopped after %s", time.Since(start))
	require.ErrorAs(t, err, &slow)
	require.Zero(t, n)

	// The same request with room to finish completes: the refusal was the
	// budget, not the filter. (An hour, not the default: under -race these
	// 3,000 matches alone can take most of a second.)
	libraryPatternBudget = time.Hour
	start = time.Now()
	books, _, err = svc.GetAudiobooksWithTotal(context.Background(), 50, 0, "", nil, nil, f)
	t.Logf("full list: %s", time.Since(start))
	require.NoError(t, err)
	require.Empty(t, books, "no title contains zzz")

	// A plain substring search takes no budget: a zero budget is irrelevant.
	libraryPatternBudget = 0
	books, _, err = svc.GetAudiobooksWithTotal(context.Background(), 50, 0, "", nil, nil,
		ListFilters{FieldFilters: []FieldFilter{{Field: "title", Value: "Number 00001"}}})
	require.NoError(t, err)
	require.Len(t, books, 10)
}

// TestLibraryPatternSlots_FullIsBusy: with every pattern slot held, a regex
// search waits the slot wait and returns a *BusyError; a substring search
// needs no slot.
func TestLibraryPatternSlots_FullIsBusy(t *testing.T) {
	svc := longTitleStore(t, 20)
	restore := querygrammar.SetPatternSlotsForTesting(1, 20*time.Millisecond)
	defer restore()
	release, err := querygrammar.AcquirePatternSlot(context.Background())
	require.NoError(t, err)
	defer release()

	_, _, err = svc.GetAudiobooksWithTotal(context.Background(), 50, 0, "", nil, nil,
		ListFilters{FieldFilters: []FieldFilter{{Field: "title", Value: "/number/"}}})
	var busy *querygrammar.BusyError
	require.ErrorAs(t, err, &busy)
	_, err = svc.CountAudiobooksFiltered(context.Background(),
		ListFilters{FieldFilters: []FieldFilter{{Field: "title", Value: "/number/"}}})
	require.ErrorAs(t, err, &busy)

	books, _, err := svc.GetAudiobooksWithTotal(context.Background(), 50, 0, "", nil, nil,
		ListFilters{FieldFilters: []FieldFilter{{Field: "title", Value: "number"}}})
	require.NoError(t, err)
	require.Len(t, books, 20)
}

// TestCheckFilterSetSize: the whole-set limits, at and past each edge.
func TestCheckFilterSetSize(t *testing.T) {
	patterns := func(n int) []FieldFilter {
		out := make([]FieldFilter, n)
		for i := range out {
			out[i] = FieldFilter{Field: "title", Value: fmt.Sprintf("/a%d/", i)}
		}
		return out
	}
	require.NoError(t, CheckFilterSetSize(patterns(MaxPatternFilters)))
	require.ErrorContains(t, CheckFilterSetSize(patterns(MaxPatternFilters+1)), "the limit is 8")
	// Plain words do not count as patterns.
	words := []FieldFilter{{Field: "title", Value: "a"}, {Field: "author", Value: "b"}}
	require.NoError(t, CheckFilterSetSize(append(patterns(MaxPatternFilters), words...)))
	// Bytes count across both slices.
	wide := []FieldFilter{{Field: "title", Value: strings.Repeat("w", 600)}}
	require.NoError(t, CheckFilterSetSize(wide))
	require.ErrorContains(t, CheckFilterSetSize(wide, wide), "total 1200 bytes; the limit is 1024")
	// The service refuses it too, for callers that skip the handler.
	svc := NewAudiobookService(mocks.NewMockStore(t))
	_, _, err := svc.GetAudiobooksWithTotal(context.Background(), 10, 0, "", nil, nil, ListFilters{FieldFilters: patterns(9)})
	require.ErrorContains(t, err, "the limit is 8")
}
