// file: internal/metabatch/upgrade_sweep_test.go
// version: 1.0.0
// guid: 6b2e9d14-83c5-4a7f-9e01-d4f8a2c5b736
// last-edited: 2026-09-27

package metabatch

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// Owner rule: Doctor Who / Big Finish / Torchwood are never touched by an
// automatic apply. Each way the book can be recognised (path, title, series
// name, a file row's path) skips it BEFORE the search, and RunUpgrade counts it.
func TestTryUpgradeBook_SkipsOwnerManualOnlyBeforeSearch(t *testing.T) {
	cases := map[string]func(f *rankFixture){
		"path":   func(f *rankFixture) { f.book.FilePath = "/library/Big Finish/The Stones of Venice/01.mp3" },
		"title":  func(f *rankFixture) { f.book.Title = "Doctor Who: The Stones of Venice" },
		"series": func(f *rankFixture) { f.book.SeriesID = new(7); f.seriesName = "Torchwood" },
		"file":   func(f *rankFixture) { f.files = []string{"/books/Doctor.Who/Stones/01.mp3"} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newRankFixture()
			mutate(f)
			svc, fetcher := f.service(candidate("Audible", 0.99))

			upgraded, err := svc.tryUpgradeBook(context.Background(), "rank-1", "open_library")
			require.ErrorIs(t, err, errOwnerManualOnly)
			require.False(t, upgraded)
			require.Zero(t, fetcher.searches, "no provider quota is spent on a manual-only book")
			require.Nil(t, f.written())

			res, err := svc.RunUpgrade(context.Background(), 200, nil)
			require.NoError(t, err)
			require.Equal(t, 1, res.OwnerManualOnly)
			require.Zero(t, res.Upgraded)
			require.Zero(t, res.Errors, "a manual-only skip is not an error")
		})
	}
}

// sweepFixture is rankFixture with n eligible books and an in-memory
// operation-state store, so the cursor persists between runs.
func sweepFixture(n int) (*rankFixture, []string) {
	f := newRankFixture()
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("book-%03d", i)
	}
	// Returned out of order: the sweep sorts by ID itself.
	shuffled := append([]string(nil), ids...)
	sort.Sort(sort.Reverse(sort.StringSlice(shuffled)))
	f.store.GetBooksByTagFunc = func(tag string) ([]string, error) {
		if tag == "metadata:source:open_library" {
			return shuffled, nil
		}
		return nil, nil
	}
	var mu sync.Mutex
	state := map[string][]byte{}
	f.store.GetOperationStateFunc = func(key string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		return state[key], nil
	}
	f.store.SaveOperationStateFunc = func(key string, data []byte) error {
		mu.Lock()
		defer mu.Unlock()
		state[key] = data
		return nil
	}
	return f, ids
}

// searched returns the book IDs the fetcher searched, sorted.
func (f *rankFetcher) searchedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]string(nil), f.ids...)
	sort.Strings(out)
	return out
}

// Books that never upgrade (the gate refuses every candidate) must not starve
// the cap: the second run starts strictly after the first run's last book,
// and the run that reaches the end wraps to the start.
func TestRunUpgrade_SweepCursorAdvancesAndWraps(t *testing.T) {
	f, ids := sweepFixture(10)
	// Score below the floor: nothing ever upgrades, the tag lists never shrink.
	svc, fetcher := f.service(candidate("Audible", 0.50))

	res1, err := svc.RunUpgrade(context.Background(), 4, nil)
	require.NoError(t, err)
	require.Equal(t, 4, res1.Checked)
	require.Equal(t, "", res1.CursorStart)
	require.Equal(t, ids[3], res1.CursorEnd)
	require.Equal(t, ids[0:4], fetcher.searchedIDs())

	svc2, fetcher2 := f.service(candidate("Audible", 0.50))
	res2, err := svc2.RunUpgrade(context.Background(), 4, nil)
	require.NoError(t, err)
	require.Equal(t, ids[3], res2.CursorStart, "the second run starts after the first run's last book")
	require.Equal(t, ids[4:8], fetcher2.searchedIDs())
	require.False(t, res2.Wrapped)

	svc3, fetcher3 := f.service(candidate("Audible", 0.50))
	res3, err := svc3.RunUpgrade(context.Background(), 4, nil)
	require.NoError(t, err)
	require.True(t, res3.Wrapped, "the third run reaches the end and wraps")
	require.Equal(t, []string{ids[0], ids[1], ids[8], ids[9]}, fetcher3.searchedIDs())
	require.Equal(t, ids[1], res3.CursorEnd)
}

// A limit larger than the eligible list takes every book once, never twice.
func TestRunUpgrade_SweepNeverRepeatsABookInOneRun(t *testing.T) {
	f, ids := sweepFixture(5)
	svc, fetcher := f.service(candidate("Audible", 0.50))
	_, err := svc.RunUpgrade(context.Background(), 4, nil) // cursor -> ids[3]
	require.NoError(t, err)

	svc2, fetcher2 := f.service(candidate("Audible", 0.50))
	res, err := svc2.RunUpgrade(context.Background(), 200, nil)
	require.NoError(t, err)
	require.Equal(t, 5, res.Checked)
	require.Equal(t, ids, fetcher2.searchedIDs())
	require.Len(t, fetcher.searchedIDs(), 4)
}
