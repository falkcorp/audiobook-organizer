// file: internal/server/handlers/metadata_cache_snapshot_test.go
// version: 1.0.0
// guid: 5a0e9c37-1d4b-4f62-8b17-c2e6d9a40f13
// last-edited: 2026-10-02

package handlers

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// snapHarness drives a reviewSnapshotCache with a counting build, a write
// counter and a settable clock.
type snapHarness struct {
	c      *reviewSnapshotCache
	builds atomic.Int64
	gen    atomic.Uint64
	fail   atomic.Bool
	gate   chan struct{} // when non-nil, builds wait on it
	mu     sync.Mutex
	clock  time.Time
}

func newSnapHarness(t *testing.T) *snapHarness {
	t.Helper()
	h := &snapHarness{clock: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)}
	h.c = newReviewSnapshotCache(func(ctx context.Context) (*reviewSnapshot, error) {
		if h.gate != nil {
			<-h.gate
		}
		n := h.builds.Add(1)
		if h.fail.Load() {
			return nil, errors.New("store fault")
		}
		return &reviewSnapshot{builtAt: h.now(), orphaned: int(n)}, nil
	}, h.gen.Load)
	h.c.now = h.now
	h.c.idleDrop = 0 // tested separately, with the real clock
	return h
}

func (h *snapHarness) now() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.clock
}

func (h *snapHarness) advance(d time.Duration) {
	h.mu.Lock()
	h.clock = h.clock.Add(d)
	h.mu.Unlock()
}

func (h *snapHarness) get(t *testing.T) *reviewSnapshot {
	t.Helper()
	s, err := h.c.get(context.Background())
	require.NoError(t, err)
	return s
}

// waitBuilds waits for the background rebuild count to reach n, then for the
// published snapshot to be that build's.
func (h *snapHarness) waitBuilds(t *testing.T, n int64) {
	t.Helper()
	require.Eventually(t, func() bool {
		h.c.mu.Lock()
		defer h.c.mu.Unlock()
		return h.builds.Load() >= n && h.c.snap != nil && int64(h.c.snap.orphaned) == n
	}, 2*time.Second, time.Millisecond)
}

func TestReviewSnapshotCache_RebuildsOnlyWhenTheCacheWasWritten(t *testing.T) {
	h := newSnapHarness(t)
	first := h.get(t)
	require.EqualValues(t, 1, h.builds.Load())

	// Paging an unchanged cache rebuilds nothing, however long it goes on
	// (short of the safety-net age).
	for i := 0; i < 20; i++ {
		h.advance(time.Minute)
		require.Same(t, first, h.get(t))
	}
	time.Sleep(20 * time.Millisecond)
	require.EqualValues(t, 1, h.builds.Load())

	// A cache write: the request is served the current snapshot at once
	// (stale-while-revalidate) and starts one rebuild.
	h.gen.Add(1)
	require.Same(t, first, h.get(t))
	h.waitBuilds(t, 2)
	second := h.get(t)
	require.NotSame(t, first, second)
	time.Sleep(20 * time.Millisecond)
	require.EqualValues(t, 2, h.builds.Load(), "the rebuilt snapshot is current: no further build")
}

func TestReviewSnapshotCache_InvalidateAndMaxAgeRebuild(t *testing.T) {
	h := newSnapHarness(t)
	h.get(t)
	h.c.invalidate()
	h.get(t)
	h.waitBuilds(t, 2)

	h.advance(reviewSnapshotMaxAge + time.Second)
	h.get(t)
	h.waitBuilds(t, 3)
}

// A write that lands while a build runs may not be in it: the snapshot must
// still read as out of date afterwards.
func TestReviewSnapshotCache_WriteDuringBuildStillCountsAsNewer(t *testing.T) {
	h := newSnapHarness(t)
	h.gate = make(chan struct{})
	done := make(chan *reviewSnapshot)
	go func() {
		s, _ := h.c.get(context.Background())
		done <- s
	}()
	time.Sleep(10 * time.Millisecond)
	h.gen.Add(1) // the build is blocked: this write happens "during" it
	close(h.gate)
	<-done
	h.get(t)
	h.waitBuilds(t, 2)
}

func TestReviewSnapshotCache_ColdRequestsJoinOneBuild(t *testing.T) {
	h := newSnapHarness(t)
	h.gate = make(chan struct{})
	const n = 16
	got := make(chan *reviewSnapshot, n)
	for i := 0; i < n; i++ {
		go func() {
			s, err := h.c.get(context.Background())
			if err != nil {
				got <- nil
				return
			}
			got <- s
		}()
	}
	time.Sleep(20 * time.Millisecond)
	close(h.gate)
	var first *reviewSnapshot
	for i := 0; i < n; i++ {
		s := <-got
		require.NotNil(t, s)
		if first == nil {
			first = s
		}
		require.Same(t, first, s)
	}
	require.EqualValues(t, 1, h.builds.Load())
}

func TestReviewSnapshotCache_FailedBuildBacksOff(t *testing.T) {
	h := newSnapHarness(t)
	h.fail.Store(true)
	_, err := h.c.get(context.Background())
	require.ErrorContains(t, err, "store fault")
	require.EqualValues(t, 1, h.builds.Load())

	// Inside the backoff no new build starts; the caller is told why.
	_, err = h.c.get(context.Background())
	require.ErrorIs(t, err, errReviewBuildBackoff)
	require.ErrorContains(t, err, "store fault")
	require.EqualValues(t, 1, h.builds.Load())

	h.fail.Store(false)
	h.advance(reviewSnapshotFailBackoff + time.Second)
	h.get(t)
	require.EqualValues(t, 2, h.builds.Load())

	// A failed background rebuild keeps serving the last good snapshot.
	good := h.get(t)
	h.fail.Store(true)
	h.gen.Add(1)
	require.Same(t, good, h.get(t))
	require.Eventually(t, func() bool { return h.builds.Load() == 3 }, 2*time.Second, time.Millisecond)
	time.Sleep(10 * time.Millisecond)
	require.Same(t, good, h.get(t), "inside the backoff: the old snapshot, no new build")
	require.EqualValues(t, 3, h.builds.Load())
}

func TestReviewSnapshotCache_DropsAfterIdle(t *testing.T) {
	h := newSnapHarness(t)
	h.c.now = time.Now
	h.c.idleDrop = 20 * time.Millisecond
	h.get(t)
	require.Eventually(t, func() bool {
		h.c.mu.Lock()
		defer h.c.mu.Unlock()
		return h.c.snap == nil
	}, 2*time.Second, 5*time.Millisecond)
	h.get(t)
	require.EqualValues(t, 2, h.builds.Load(), "the next visit builds again")
}

// Run under -race: requests, invalidations and cache writes interleaving with
// background rebuilds.
func TestReviewSnapshotCache_ConcurrentGetInvalidateRebuild(t *testing.T) {
	h := newSnapHarness(t)
	h.c.idleDrop = time.Millisecond
	h.c.now = time.Now
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				switch (w + i) % 4 {
				case 0:
					h.c.invalidate()
				case 1:
					h.gen.Add(1)
				default:
					s, err := h.c.get(context.Background())
					if err == nil && s == nil {
						t.Error("nil snapshot without an error")
					}
				}
			}
		}(w)
	}
	wg.Wait()
}
