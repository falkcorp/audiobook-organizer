// file: internal/server/handlers/metadata_cache_snapshot_test.go
// version: 1.1.0
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

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
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
	h.c.idleMark = 0    // tested separately, with the real clock
	h.c.minInterval = 0 // tested separately
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

// After the idle mark the snapshot is NOT dropped: the next visit is served
// it at once and refreshes it in the background.
func TestReviewSnapshotCache_IdleMarksStaleInsteadOfDropping(t *testing.T) {
	h := newSnapHarness(t)
	h.c.idleMark = 20 * time.Millisecond
	first := h.get(t)
	time.Sleep(60 * time.Millisecond) // the mark fires
	require.Same(t, first, h.get(t), "served the old snapshot, no cold wait")
	h.waitBuilds(t, 2)
}

// Continuous cache writes (a fetch op) start at most one rebuild per
// minInterval; the snapshot is served meanwhile.
func TestReviewSnapshotCache_MinIntervalBetweenRebuilds(t *testing.T) {
	h := newSnapHarness(t)
	h.c.minInterval = time.Minute
	h.get(t)
	h.advance(2 * time.Minute)
	h.gen.Add(1)
	h.get(t)
	h.waitBuilds(t, 2)
	for i := 0; i < 10; i++ {
		h.gen.Add(1)
		h.advance(time.Second)
		h.get(t)
	}
	time.Sleep(20 * time.Millisecond)
	require.EqualValues(t, 2, h.builds.Load(), "inside minInterval: no rebuild")
	h.advance(time.Minute)
	h.get(t)
	h.waitBuilds(t, 3)
}

// Cold builds run on the spawn the server provides (its tracked group), and a
// refused spawn fails the request instead of hanging it.
func TestReviewSnapshotCache_ColdBuildUsesSpawnAndRefusalFails(t *testing.T) {
	h := newSnapHarness(t)
	var spawned atomic.Int64
	h.c.setBackground(context.Background(), func(f func()) bool { spawned.Add(1); go f(); return true })
	h.get(t)
	require.EqualValues(t, 1, spawned.Load())

	h2 := newSnapHarness(t)
	h2.c.setBackground(context.Background(), func(func()) bool { return false })
	_, err := h2.c.get(context.Background())
	require.ErrorIs(t, err, errReviewBuildNotStarted)
	require.EqualValues(t, 0, h2.builds.Load())

	// A cancelled lifetime context starts nothing either.
	h3 := newSnapHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h3.c.setBackground(ctx, nil)
	_, err = h3.c.get(context.Background())
	require.ErrorIs(t, err, errReviewBuildNotStarted)
}

// Run under -race: requests, invalidations and cache writes interleaving with
// background rebuilds.
func TestReviewSnapshotCache_ConcurrentGetInvalidateRebuild(t *testing.T) {
	h := newSnapHarness(t)
	h.c.idleMark = time.Millisecond
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

// overlayFake serves the overlay's two reads; everything else is unused.
type overlayFake struct {
	cacheRowBookReader
	batchErr error
	books    map[string]*database.Book
	pointErr map[string]error
}

func (f overlayFake) GetBooksByIDs(ids []string) ([]database.Book, error) {
	if f.batchErr != nil {
		return nil, f.batchErr
	}
	var out []database.Book
	for _, id := range ids {
		if b := f.books[id]; b != nil {
			out = append(out, *b)
		}
	}
	return out, nil
}

func (f overlayFake) GetBookByID(id string) (*database.Book, error) {
	if err := f.pointErr[id]; err != nil {
		return nil, err
	}
	return f.books[id], nil
}

func overlaySnap(ids ...string) *reviewSnapshot {
	s := &reviewSnapshot{}
	for _, id := range ids {
		s.rows = append(s.rows, snapshotRow{loadedCacheRow: loadedCacheRow{sum: metafetch.MetadataCacheSummary{BookID: id}}})
	}
	return s
}

func TestOverlay_BatchFailureFallsBackToPointReads(t *testing.T) {
	f := overlayFake{
		batchErr: errors.New("batch fault"),
		books:    map[string]*database.Book{"a": {ID: "a"}, "b": {ID: "b"}},
		pointErr: map[string]error{"c": errors.New("point fault")},
	}
	out, err := overlayLiveBooks(overlaySnap("a", "b", "c", "gone"), f, nil)
	require.NoError(t, err)
	require.Len(t, out.rows, 2)
	require.Equal(t, 1, out.readErrors, "a failed read is an error")
	require.Equal(t, 1, out.orphaned, "only the book that is really gone is orphaned")
}

func TestOverlay_EveryReadFailingIsAnError(t *testing.T) {
	f := overlayFake{
		batchErr: errors.New("batch fault"),
		pointErr: map[string]error{"a": errors.New("x"), "b": errors.New("y")},
	}
	_, err := overlayLiveBooks(overlaySnap("a", "b"), f, nil)
	require.ErrorIs(t, err, errOverlayAllReadsFailed)

	// A working batch with one failed miss confirmation is not "every read".
	f2 := overlayFake{books: map[string]*database.Book{"a": {ID: "a"}}, pointErr: map[string]error{"b": errors.New("y")}}
	out, err := overlayLiveBooks(overlaySnap("a", "b"), f2, nil)
	require.NoError(t, err)
	require.Equal(t, 1, out.readErrors)
}

func TestOverlay_IDsReadOnlyTheAskedBooks(t *testing.T) {
	var asked []string
	f := overlayFakeRecording{overlayFake: overlayFake{books: map[string]*database.Book{"a": {ID: "a"}, "b": {ID: "b"}}}, asked: &asked}
	out, err := overlayLiveBooks(overlaySnap("a", "b", "c"), f, map[string]bool{"b": true})
	require.NoError(t, err)
	require.Equal(t, []string{"b"}, asked)
	require.Len(t, out.rows, 1)
}

type overlayFakeRecording struct {
	overlayFake
	asked *[]string
}

func (f overlayFakeRecording) GetBooksByIDs(ids []string) ([]database.Book, error) {
	*f.asked = append(*f.asked, ids...)
	return f.overlayFake.GetBooksByIDs(ids)
}
