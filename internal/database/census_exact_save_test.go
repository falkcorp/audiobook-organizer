// file: internal/database/census_exact_save_test.go
// version: 1.0.1
// guid: ded5da72-74be-465a-8eb6-585c351ab894
// last-edited: 2026-10-19

package database

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

// writeRandomCensusKeys writes n keys named key(i) with 200 incompressible
// bytes each and flushes, so EstimateDiskUsage cuts them into sub-ranges.
func writeRandomCensusKeys(t *testing.T, p *PebbleStore, n int, key func(i int) string) {
	t.Helper()
	rng := rand.New(rand.NewSource(7))
	b := p.db.NewBatch()
	for i := range n {
		v := make([]byte, 200)
		_, _ = rng.Read(v)
		require.NoError(t, b.Set([]byte(key(i)), v, nil))
	}
	require.NoError(t, b.Commit(pebble.Sync))
	require.NoError(t, p.db.Flush())
}

// countKeysIn counts the live keys in [lo, hi) with a plain iterator.
func countKeysIn(t *testing.T, p *PebbleStore, lo, hi []byte) int64 {
	t.Helper()
	it, err := p.db.NewIter(&pebble.IterOptions{LowerBound: lo, UpperBound: hi})
	require.NoError(t, err)
	var n int64
	for valid := it.First(); valid; valid = it.Next() {
		n++
	}
	require.NoError(t, it.Error())
	require.NoError(t, it.Close())
	return n
}

// shrinkExactCensusForSaveTests cuts sub-ranges at 64 KiB and makes the
// in-loop budget check never fire, so a sub-range is read in one pass and its
// only budget wait is the closing one. Restores everything on cleanup.
func shrinkExactCensusForSaveTests(t *testing.T, refresh time.Duration) {
	t.Helper()
	oldSub, oldCheck, oldRefresh := exactCensusSubrangeBytes, exactCensusCheckBytes, exactCensusIterRefresh
	exactCensusSubrangeBytes, exactCensusCheckBytes, exactCensusIterRefresh = 64<<10, 1<<30, refresh
	t.Cleanup(func() {
		exactCensusSubrangeBytes, exactCensusCheckBytes, exactCensusIterRefresh = oldSub, oldCheck, oldRefresh
		exactCensusAfterSave = nil
	})
}

// A refused deadline in a sub-range's closing budget wait must not lose the
// sub-range: countRange saves its end before the wait. The limiter is sized
// so the closing wait needs far longer than the context allows, which the
// limiter refuses at once; with the save after the wait (the old order) no
// save happens at all and every count of the sub-range is thrown away.
func TestRunExactCensus_CountRangeSavesTheRangeEndBeforeTheClosingWait(t *testing.T) {
	shrinkExactCensusForSaveTests(t, time.Hour)
	const n = 2000
	for _, tc := range []struct {
		name string
		hi   []byte
	}{
		{"bounded", []byte("cat;")},
		{"open end", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newCensusRawStore(t)
			writeRandomCensusKeys(t, p, n, func(i int) string { return fmt.Sprintf("cat:%07d", i) })

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			r := &exactCensusRun{
				p: p, ctx: ctx, st: &exactCensusState{Done: map[string]exactFamilyCount{}}, all: 1,
				// 1 KB/s with a 4 KiB burst: the first chunk of the closing
				// wait is free, the second needs ~4 s, past the 2 s deadline.
				lim:    rate.NewLimiter(rate.Limit(1000), 4096),
				report: func(string) {}, lastBeat: time.Now(), lastSave: time.Now(),
			}
			type saved struct {
				at    []byte
				force bool
				live  int64
			}
			var saves []saved
			var c exactFamilyCount
			err := r.countRange("cat:", []byte("cat:"), tc.hi, &c, nil, func(at []byte, force bool) error {
				saves = append(saves, saved{at: at, force: force, live: c.Live})
				return nil
			})
			require.ErrorIs(t, err, context.DeadlineExceeded, "the closing wait was refused")
			require.Len(t, saves, 1, "the sub-range end is saved before the wait that failed")
			require.True(t, saves[0].force, "the save is not throttled away")
			require.Equal(t, tc.hi, saves[0].at, "the saved position is the end of the sub-range")
			require.Equal(t, int64(n), saves[0].live, "every key of the sub-range is in the counts being saved")
			require.Equal(t, int64(n), c.Live)
		})
	}
}

// Cancelling inside the closing wait after a sub-range keeps that sub-range:
// the saved position is its end, the saved counts are exactly its keys, and
// the next run neither re-counts it nor skips anything after it.
func TestRunExactCensus_CancelInClosingWaitKeepsTheSubrange(t *testing.T) {
	shrinkExactCensusForSaveTests(t, time.Hour)
	const n = 6000
	p := newCensusRawStore(t)
	writeRandomCensusKeys(t, p, n, func(i int) string { return fmt.Sprintf("cat:%07d", i) })

	subs, err := p.exactSubranges([]byte("cat:"), []byte("cat;"))
	require.NoError(t, err)
	// Bisecting the key space leaves many empty sub-ranges (nothing to save
	// or wait for); the first one with keys is where the run first waits.
	first, firstKeys := -1, int64(0)
	for i, sub := range subs {
		if k := countKeysIn(t, p, sub[0], sub[1]); k > 0 {
			first, firstKeys = i, k
			break
		}
	}
	require.GreaterOrEqual(t, first, 0)
	require.Less(t, firstKeys, int64(n))
	var withKeys int
	for _, sub := range subs {
		if countKeysIn(t, p, sub[0], sub[1]) > 0 {
			withKeys++
		}
	}
	require.Greater(t, withKeys, 3, "the keys are cut into several sub-ranges")

	// The first in-family save of a throttled run is the one before the
	// first sub-range's closing wait; cancelling there makes that wait fail.
	ctx, cancel := context.WithCancel(context.Background())
	saves := 0
	exactCensusAfterSave = func(fam string) {
		if fam == "cat:" {
			if saves++; saves == 1 {
				cancel()
			}
		}
	}
	opts := ExactCensusOptions{ReadBytesPerSec: 1 << 20}
	_, err = p.RunExactCensus(ctx, opts)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, saves, "the run stopped at that first save, inside the wait")

	st, err := p.loadExactCensusState()
	require.NoError(t, err)
	require.NotNil(t, st)
	require.NotNil(t, st.Partial)
	require.Equal(t, "cat:", st.Partial.Family)
	require.Equal(t, 0, st.Partial.Piece)
	require.Equal(t, subs[first][1], st.Partial.ResumeAt, "the resume position is the end of the first sub-range with keys")
	require.Equal(t, firstKeys, st.Partial.Count.Live, "the saved counts are exactly the first sub-range's keys")

	exactCensusAfterSave = nil
	ex, err := p.RunExactCensus(context.Background(), opts)
	require.NoError(t, err)
	cat := censusFamily(t, ex, "cat:")
	require.Equal(t, int64(n), cat.Keys, "no key lost or counted twice across the resume")
	require.Equal(t, int64(n), cat.Entries)
}

// The same cut at every save point of a run: after the closing-wait save and
// after the throttled-or-not save that follows it, every stop resumes to the
// exact counts. With the refresh at zero both saves happen at each boundary,
// so this also covers a save immediately followed by the next one.
func TestRunExactCensus_EverySavePointResumesExactly(t *testing.T) {
	const n = 3000
	for k := 1; k <= 10; k++ {
		t.Run(fmt.Sprintf("cancel_at_save_%d", k), func(t *testing.T) {
			shrinkExactCensusForSaveTests(t, 0)
			p := newCensusRawStore(t)
			writeRandomCensusKeys(t, p, n, func(i int) string { return fmt.Sprintf("cat:%07d", i) })

			ctx, cancel := context.WithCancel(context.Background())
			saves := 0
			exactCensusAfterSave = func(fam string) {
				if fam == "cat:" {
					if saves++; saves == k {
						cancel()
					}
				}
			}
			opts := ExactCensusOptions{ReadBytesPerSec: 1 << 20}
			_, err := p.RunExactCensus(ctx, opts)
			require.ErrorIs(t, err, context.Canceled)
			require.Equal(t, k, saves)

			exactCensusAfterSave = nil
			ex, err := p.RunExactCensus(context.Background(), opts)
			require.NoError(t, err)
			cat := censusFamily(t, ex, "cat:")
			require.Equal(t, int64(n), cat.Keys)
			require.Equal(t, int64(n), cat.Entries)
		})
	}
}

// The last piece of the key space has no upper bound. A save at its end must
// not store a nil resume position (which means "restart the piece" and would
// count it twice): it points past the piece.
func TestRunExactCensus_CancelInClosingWaitOfTheOpenEndedPiece(t *testing.T) {
	shrinkExactCensusForSaveTests(t, time.Hour)
	var tail []keyRange
	for _, kr := range keyFamilyRanges(keyFamilies) {
		if kr.Family == unregisteredFamily {
			tail = append(tail, kr)
		}
	}
	require.NotEmpty(t, tail)
	last := tail[len(tail)-1]
	require.Nil(t, last.Hi, "the last unregistered piece is open-ended")

	// Few enough keys (about 25 KB on disk) that the piece stays one
	// sub-range, whose upper bound is the open end.
	const n = 100
	p := newCensusRawStore(t)
	writeRandomCensusKeys(t, p, n, func(i int) string { return fmt.Sprintf("%s%07d", last.Lo, i) })
	subs, err := p.exactSubranges(last.Lo, nil)
	require.NoError(t, err)
	require.Len(t, subs, 1)

	ctx, cancel := context.WithCancel(context.Background())
	saves := 0
	exactCensusAfterSave = func(fam string) {
		if fam == unregisteredFamily {
			if saves++; saves == 1 {
				cancel()
			}
		}
	}
	opts := ExactCensusOptions{ReadBytesPerSec: 1 << 20}
	_, err = p.RunExactCensus(ctx, opts)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, saves)

	st, err := p.loadExactCensusState()
	require.NoError(t, err)
	require.NotNil(t, st.Partial)
	require.Equal(t, unregisteredFamily, st.Partial.Family)
	require.Equal(t, len(tail), st.Partial.Piece, "the position is past the open-ended piece")
	require.Nil(t, st.Partial.ResumeAt)
	require.Equal(t, int64(n), st.Partial.Count.Live)

	exactCensusAfterSave = nil
	ex, err := p.RunExactCensus(context.Background(), opts)
	require.NoError(t, err)
	un := censusFamily(t, ex, unregisteredFamily)
	require.Equal(t, int64(n), un.Keys, "the open-ended piece is counted once")
	require.Equal(t, int64(n), un.Entries)
}
