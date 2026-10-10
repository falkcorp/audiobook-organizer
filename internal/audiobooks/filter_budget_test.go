// file: internal/audiobooks/filter_budget_test.go
// version: 1.1.0
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

// TestLibraryBudget_OneMegabyteDescription: one row with a 1 MB description
// and the slowest admitted pattern. Uncut, that one match took 5.1 s, which
// no budget can interrupt; cut to querygrammar.MaxPatternInputBytes it takes
// about 80 ms, so the search finishes (or is refused) within the budget plus
// about one cut match.
func TestLibraryBudget_OneMegabyteDescription(t *testing.T) {
	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	ps.WaitForWarmup()
	desc := strings.Repeat("A long description of the book with many words and sentences. ", 16700) // ~1 MB
	_, err = ps.CreateBook(&database.Book{Title: "One Long Book", FilePath: "/synthetic/long.m4b", Description: &desc})
	require.NoError(t, err)
	svc := NewAudiobookService(ps)

	const pattern = `/(?:\pL?){245}zzz/`
	// The unit no budget can interrupt is one match over the cut input:
	// measure it here, so the bound holds under -race (which slows each
	// match about 18x) as well as without it (about 80 ms).
	tm, err := querygrammar.CompileText(pattern, false)
	require.NoError(t, err)
	mStart := time.Now()
	tm.Match(desc[:querygrammar.MaxPatternInputBytes])
	oneMatch := time.Since(mStart)

	f := ListFilters{FieldFilters: []FieldFilter{{Field: "description", Value: pattern}}}
	start := time.Now()
	books, _, err := svc.GetAudiobooksWithTotal(context.Background(), 50, 0, "", nil, nil, f)
	took := time.Since(start)
	t.Logf("1 MB description: %s (one cut match %s), err=%v", took, oneMatch, err)
	var slow *querygrammar.TooSlowError
	if err != nil {
		require.ErrorAs(t, err, &slow)
	} else {
		require.Empty(t, books)
	}
	// Uncut, this one match took 5.1 s (far longer under -race), so the
	// bound fails if the cut is removed.
	require.Less(t, took, libraryPatternBudget+2*oneMatch+100*time.Millisecond)
}

// TestSharedAllowance_OneSlotOneBudget: under WithSharedSearchAllowance the
// list and the count of one request take ONE slot (held until done, so a
// second count needs no new one) and spend ONE budget.
func TestSharedAllowance_OneSlotOneBudget(t *testing.T) {
	svc := longTitleStore(t, 50)
	restore := querygrammar.SetPatternSlotsForTesting(1, 10*time.Millisecond)
	defer restore()
	f := ListFilters{FieldFilters: []FieldFilter{{Field: "title", Value: "/number/"}}}

	ctx, done := WithSharedSearchAllowance(context.Background())
	_, _, err := svc.GetAudiobooksWithTotal(ctx, 10, 0, "", nil, nil, f)
	require.NoError(t, err)
	a := ctx.Value(searchAllowanceKey{}).(*sharedSearchAllowance)
	afterList := a.budget.Spent()
	require.Positive(t, afterList)

	// The request still holds the only slot: anyone else is busy...
	_, err = querygrammar.AcquirePatternSlot(context.Background())
	var busy *querygrammar.BusyError
	require.ErrorAs(t, err, &busy)
	// ...but its own count runs on it, against the same budget.
	n, err := svc.CountAudiobooksFiltered(ctx, f)
	require.NoError(t, err)
	require.Equal(t, 50, n)
	require.Greater(t, a.budget.Spent(), afterList, "the count charged the request's budget")

	done()
	release, err := querygrammar.AcquirePatternSlot(context.Background())
	require.NoError(t, err, "done returns the slot")
	release()
	done() // idempotent
}

// TestSharedAllowance_CountSpendsWhatTheListLeft: a budget the list nearly
// spent refuses the count, so the request is refused once, never served
// with two budgets.
func TestSharedAllowance_CountSpendsWhatTheListLeft(t *testing.T) {
	svc := longTitleStore(t, 400)
	saved := libraryPatternBudget
	t.Cleanup(func() { libraryPatternBudget = saved })
	f := ListFilters{FieldFilters: threeSlowTokens()}
	// Measure one scan, then give the request a budget for about 1.5 of them.
	probe := querygrammar.NewBudget(time.Hour)
	bsf, ok := svc.buildBookSummaryFilter(f, true, probe)
	require.True(t, ok)
	_, err := svc.countSummariesPushdownFiltered(bsf)
	require.NoError(t, err)
	one := probe.Spent()
	libraryPatternBudget = one * 3 / 2

	ctx, done := WithSharedSearchAllowance(context.Background())
	defer done()
	_, _, err = svc.GetAudiobooksWithTotal(ctx, 50, 0, "", nil, nil, f)
	require.NoError(t, err, "one scan fits a budget of 1.5 scans")
	_, err = svc.CountAudiobooksFiltered(ctx, f)
	var slow *querygrammar.TooSlowError
	require.ErrorAs(t, err, &slow, "the second scan spends what the first left")

	// Without the shared allowance each scan would have had its own budget.
	_, err = svc.CountAudiobooksFiltered(context.Background(), f)
	require.NoError(t, err)
}

// TestLibraryPatternSlots_ReleasedAfterEveryCall: with ONE slot, sequential
// costly lists and counts never find it busy, including after a call that
// failed inside (a spent budget), so every path returns its slot.
func TestLibraryPatternSlots_ReleasedAfterEveryCall(t *testing.T) {
	svc := longTitleStore(t, 30)
	restore := querygrammar.SetPatternSlotsForTesting(1, 10*time.Millisecond)
	defer restore()
	f := ListFilters{FieldFilters: []FieldFilter{{Field: "title", Value: "/number/"}}}
	for i := range 3 {
		_, _, err := svc.GetAudiobooksWithTotal(context.Background(), 10, 0, "", nil, nil, f)
		require.NoError(t, err, "list %d", i)
		_, err = svc.CountAudiobooksFiltered(context.Background(), f)
		require.NoError(t, err, "count %d", i)
	}
	saved := libraryPatternBudget
	libraryPatternBudget = 0 // the next scans fail inside, after taking the slot
	var slow *querygrammar.TooSlowError
	_, _, err := svc.GetAudiobooksWithTotal(context.Background(), 10, 0, "", nil, nil, f)
	require.ErrorAs(t, err, &slow)
	_, err = svc.CountAudiobooksFiltered(context.Background(), f)
	require.ErrorAs(t, err, &slow)
	libraryPatternBudget = saved
	_, _, err = svc.GetAudiobooksWithTotal(context.Background(), 10, 0, "", nil, nil, f)
	require.NoError(t, err, "a failed call still returned its slot")
}

// longestEcho is the length of the longest run of input that msg repeats.
func longestEcho(msg, input string) int {
	best := 0
	for i := range input {
		for j := i + best + 1; j <= len(input); j++ {
			if !strings.Contains(msg, input[i:j]) {
				break
			}
			best = j - i
		}
	}
	return best
}

// TestValidateFilterValue_DoesNotEchoLongValue: the 400 a Library filter
// earns names the value shortened to 64 bytes; the reason that follows it
// does not quote the value again (regexp's own message would quote all of it).
func TestValidateFilterValue_DoesNotEchoLongValue(t *testing.T) {
	unclosed := "/" + strings.Repeat("ab", 101)                        // 203 bytes
	complexRe := "/(?:\\pL?){245}zzz" + strings.Repeat("q", 222) + "/" // 240 bytes
	badParen := "/(" + strings.Repeat("c", 238) + "/"                  // 241 bytes
	for _, v := range []string{unclosed, complexRe, badParen} {
		err := ValidateFilterValue(FieldFilter{Field: "title", Value: v})
		require.Error(t, err)
		msg := "invalid filter value: " + err.Error() // the handler's whole 400 text
		n := longestEcho(msg, v)
		t.Logf("%d bytes -> echoes %d: %s", len(v), n, msg)
		require.LessOrEqual(t, n, 64, msg)
	}
}
