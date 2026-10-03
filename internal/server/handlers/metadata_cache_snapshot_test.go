// file: internal/server/handlers/metadata_cache_snapshot_test.go
// version: 2.0.0
// guid: 5a0e9c37-1d4b-4f62-8b17-c2e6d9a40f13
// last-edited: 2026-10-03

package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
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
	h.c = newReviewSnapshotCache(func(ctx context.Context, prev *reviewSnapshot) (*reviewSnapshot, error) {
		gen := h.gen.Load() // read at build begin, as the real builder does
		if h.gate != nil {
			<-h.gate
		}
		n := h.builds.Add(1)
		if h.fail.Load() {
			return nil, errors.New("store fault")
		}
		// The build count travels as the orphan count, so a test can tell
		// which build a published snapshot came from.
		snap := &reviewSnapshot{builtAt: h.now(), cacheGen: gen, orphanIDs: map[string]struct{}{}}
		for i := int64(0); i < n; i++ {
			snap.orphanIDs[fmt.Sprint(i)] = struct{}{}
		}
		return snap, nil
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
		return h.builds.Load() >= n && h.c.snap != nil && int64(h.c.snap.orphaned()) == n
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

// overlaySnap is a snapshot of rows for ids, each holding a snapshot-time
// book stamped "snapshot" so a test can tell it from a live read.
func overlaySnap(ids ...string) *reviewSnapshot {
	s := &reviewSnapshot{books: map[string]*database.Book{}, orphanIDs: map[string]struct{}{}}
	for _, id := range ids {
		s.rows = append(s.rows, snapshotRow{loadedCacheRow: loadedCacheRow{sum: metafetch.MetadataCacheSummary{BookID: id}}})
		s.books[id] = &database.Book{ID: id, Title: "snapshot"}
	}
	return s
}

// changedIDs is a change log answering with ids, ok.
func changedIDs(ids ...string) changedSinceFunc {
	return func(uint64) ([]string, uint64, bool) { return ids, 0, true }
}

func rowIDs(rows []snapshotRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.sum.BookID
	}
	return out
}

func TestOverlay_BatchFailureFallsBackToPointReads(t *testing.T) {
	f := overlayFake{
		batchErr: errors.New("batch fault"),
		books:    map[string]*database.Book{"a": {ID: "a"}, "b": {ID: "b"}},
		pointErr: map[string]error{"c": errors.New("point fault")},
	}
	out, err := overlayLiveBooks(overlaySnap("a", "b", "c", "gone"), f, nil, nil)
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
	_, err := overlayLiveBooks(overlaySnap("a", "b"), f, nil, nil)
	require.ErrorIs(t, err, errOverlayAllReadsFailed)

	// A working batch with one failed miss confirmation is not "every read".
	f2 := overlayFake{books: map[string]*database.Book{"a": {ID: "a"}}, pointErr: map[string]error{"b": errors.New("y")}}
	out, err := overlayLiveBooks(overlaySnap("a", "b"), f2, nil, nil)
	require.NoError(t, err)
	require.Equal(t, 1, out.readErrors)
}

func TestOverlay_IDsReadOnlyTheAskedBooks(t *testing.T) {
	var asked []string
	f := overlayFakeRecording{overlayFake: overlayFake{books: map[string]*database.Book{"a": {ID: "a"}, "b": {ID: "b"}}}, asked: &asked}
	out, err := overlayLiveBooks(overlaySnap("a", "b", "c"), f, map[string]bool{"b": true}, nil)
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

// TestOverlay_ReadsOnlyTheChangedBooks: the overlay reads the books the
// change log names (and only those of the snapshot's rows), serves every
// other row the snapshot's book, and asks for no rebuild.
func TestOverlay_ReadsOnlyTheChangedBooks(t *testing.T) {
	var asked []string
	live := map[string]*database.Book{"a": {ID: "a", Title: "live"}, "b": {ID: "b", Title: "live"}, "c": {ID: "c", Title: "live"}}
	f := overlayFakeRecording{overlayFake: overlayFake{books: live}, asked: &asked}
	out, err := overlayLiveBooks(overlaySnap("a", "b", "c"), f, nil, changedIDs("b", "not-a-row"))
	require.NoError(t, err)
	require.Equal(t, []string{"b"}, asked, "only the changed row's book is read")
	require.False(t, out.rebuild)
	require.Equal(t, []string{"a", "b", "c"}, rowIDs(out.rows))
	require.Equal(t, "snapshot", out.rows[0].book.Title)
	require.Equal(t, "live", out.rows[1].book.Title)
	require.Equal(t, "snapshot", out.rows[2].book.Title)
	require.Equal(t, "live", out.book("b").Title)
	require.Equal(t, "snapshot", out.book("a").Title)

	// Nothing changed: nothing is read.
	asked = nil
	out, err = overlayLiveBooks(overlaySnap("a", "b"), f, nil, changedIDs())
	require.NoError(t, err)
	require.Empty(t, asked)
	require.Len(t, out.rows, 2)
}

// TestOverlay_DeletedChangedBookIsOrphaned: a changed book the store no
// longer has drops its row and counts as orphaned for this request.
func TestOverlay_DeletedChangedBookIsOrphaned(t *testing.T) {
	f := overlayFake{books: map[string]*database.Book{"a": {ID: "a"}}}
	snap := overlaySnap("a", "b")
	snap.orphanIDs["old-orphan"] = struct{}{}
	out, err := overlayLiveBooks(snap, f, nil, changedIDs("b"))
	require.NoError(t, err)
	require.Equal(t, []string{"a"}, rowIDs(out.rows))
	require.Equal(t, 2, out.orphaned, "the snapshot's orphan plus the deleted book")
	require.Nil(t, out.book("b"))
}

// TestOverlay_UnlistableChangeReadsEveryBookAndAsksForARebuild: a change log
// that cannot name the change (or none at all) means every row's book is
// read, as before the log, and the handler is told to rebuild.
func TestOverlay_UnlistableChangeReadsEveryBookAndAsksForARebuild(t *testing.T) {
	for name, log := range map[string]changedSinceFunc{
		"not ok": func(uint64) ([]string, uint64, bool) { return nil, 0, false },
		"no log": nil,
	} {
		t.Run(name, func(t *testing.T) {
			var asked []string
			f := overlayFakeRecording{overlayFake: overlayFake{books: map[string]*database.Book{"a": {ID: "a"}, "b": {ID: "b"}}}, asked: &asked}
			out, err := overlayLiveBooks(overlaySnap("a", "b"), f, nil, log)
			require.NoError(t, err)
			require.Equal(t, []string{"a", "b"}, asked)
			require.True(t, out.rebuild)
			require.Len(t, out.rows, 2)
		})
	}
}

// TestOverlay_TooManyChangedBooksAsksForARebuild: past overlayRebuildThreshold
// changed books the overlay still serves the request from what it read, but
// asks for a rebuild so the next one does not read them again.
func TestOverlay_TooManyChangedBooksAsksForARebuild(t *testing.T) {
	changed := make([]string, overlayRebuildThreshold+1)
	for i := range changed {
		changed[i] = "x" + strconv.Itoa(i)
	}
	changed[0] = "b"
	var asked []string
	f := overlayFakeRecording{overlayFake: overlayFake{books: map[string]*database.Book{"a": {ID: "a"}, "b": {ID: "b"}}}, asked: &asked}
	out, err := overlayLiveBooks(overlaySnap("a", "b"), f, nil, changedIDs(changed...))
	require.NoError(t, err)
	require.True(t, out.rebuild)
	require.Equal(t, []string{"b"}, asked, "only the changed ROWS are read, however many books changed")
	require.Len(t, out.rows, 2)

	at := make([]string, overlayRebuildThreshold)
	for i := range at {
		at[i] = "x" + strconv.Itoa(i)
	}
	out, err = overlayLiveBooks(overlaySnap("a"), f, nil, changedIDs(at...))
	require.NoError(t, err)
	require.False(t, out.rebuild, "the threshold itself is fine")
}

// TestOverlay_IDsReadOnlyTheAskedChangedBooks: an ids= lookup reads only the
// asked rows that changed.
func TestOverlay_IDsReadOnlyTheAskedChangedBooks(t *testing.T) {
	var asked []string
	f := overlayFakeRecording{overlayFake: overlayFake{books: map[string]*database.Book{"a": {ID: "a"}, "b": {ID: "b"}, "c": {ID: "c"}}}, asked: &asked}
	out, err := overlayLiveBooks(overlaySnap("a", "b", "c"), f, map[string]bool{"b": true, "c": true}, changedIDs("a", "b"))
	require.NoError(t, err)
	require.Equal(t, []string{"b"}, asked)
	require.Equal(t, []string{"b", "c"}, rowIDs(out.rows))
}

// TestReviewSnapshotCache_RequestRebuildStartsOneNow: an overlay's rebuild
// request starts the build at once (under get's rules), rather than leaving
// it to the next request.
func TestReviewSnapshotCache_RequestRebuildStartsOneNow(t *testing.T) {
	h := newSnapHarness(t)
	h.get(t)
	h.c.requestRebuild()
	h.waitBuilds(t, 2)

	h.c.minInterval = time.Minute
	h.c.requestRebuild()
	time.Sleep(20 * time.Millisecond)
	require.EqualValues(t, 2, h.builds.Load(), "inside minInterval: marked, not started")
	h.advance(time.Minute)
	h.get(t)
	h.waitBuilds(t, 3)
}

// The cache hands the build the snapshot it replaces.
func TestReviewSnapshotCache_BuildGetsThePreviousSnapshot(t *testing.T) {
	var prevs []*reviewSnapshot
	var mu sync.Mutex
	c := newReviewSnapshotCache(func(_ context.Context, prev *reviewSnapshot) (*reviewSnapshot, error) {
		mu.Lock()
		prevs = append(prevs, prev)
		mu.Unlock()
		return &reviewSnapshot{builtAt: time.Now()}, nil
	}, nil)
	c.minInterval, c.idleMark = 0, 0
	first, err := c.get(context.Background())
	require.NoError(t, err)
	c.invalidate()
	_, err = c.get(context.Background())
	require.NoError(t, err)
	require.Eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(prevs) == 2 }, 2*time.Second, time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	require.Nil(t, prevs[0], "a cold build has no previous snapshot")
	require.Same(t, first, prevs[1])
}

// ---- incremental builds over the real store ----

// incrBuilder is the builder as NewMetadataCacheHandler wires it for a real
// store, with a settable clock.
func incrBuilder(t *testing.T, store *database.PebbleStore, svc cacheRowCandidateReader) (*reviewSnapshotBuilder, *time.Time) {
	t.Helper()
	b := newReviewSnapshotBuilder(store, svc)
	b.cacheGen = store.MetadataCacheGeneration
	b.cacheChangedSince = store.MetadataCacheChangedSince
	b.bookGen = store.LibraryGeneration().Value
	b.booksChangedSince = store.BooksChangedSince
	clock := time.Now()
	b.now = func() time.Time { return clock }
	return b, &clock
}

// snapKey reduces a snapshot to what the page sees: the rows in order with
// their hash and candidate count, the books by id and the orphans.
type snapKey struct {
	rows    []string
	books   map[string]database.Book
	orphans []string
}

func keyOf(s *reviewSnapshot) snapKey {
	k := snapKey{books: map[string]database.Book{}}
	for _, r := range s.rows {
		k.rows = append(k.rows, fmt.Sprintf("%s|%d|%s|%v|%s|%s", r.sum.BookID, r.candidateCount, r.hash, r.searchable, r.files.ITunesPath, r.lastChecked.UTC().Format(time.RFC3339Nano)))
	}
	for id, b := range s.books {
		k.books[id] = *b
	}
	for id := range s.orphanIDs {
		k.orphans = append(k.orphans, id)
	}
	sort.Strings(k.orphans)
	return k
}

// TestReviewSnapshot_IncrementalEqualsFull: after cache writes, book writes,
// deletes and orphan transitions, the incremental build over the previous
// snapshot is the full build of the same store state: same rows, same order,
// same books, same orphans. Its builtAt is the full build's; its generations
// are current.
func TestReviewSnapshot_IncrementalEqualsFull(t *testing.T) {
	store, svc := reviewSeed(t, 120)
	// A book with no file rows (DeleteBook refuses one that owns files) for
	// the row -> orphan transition.
	fileless, err := store.CreateBook(&database.Book{Title: "Fileless", FilePath: "/lib/Fileless/Fileless", Format: "mp3"})
	require.NoError(t, err)
	require.NoError(t, store.PutMetadataCache(&database.MetadataCandidateCache{BookID: fileless.ID, FetchedAt: time.Now().Add(-time.Hour)}))
	// A cache row with no book: an orphan, for the orphan -> row transition.
	const orphanID = "orphanbook0001"
	require.NoError(t, store.PutMetadataCache(&database.MetadataCandidateCache{BookID: orphanID, FetchedAt: time.Now().Add(-2 * time.Hour)}))
	b, clock := incrBuilder(t, store, svc)
	ctx := context.Background()
	first, err := b.build(ctx, nil)
	require.NoError(t, err)
	require.False(t, first.incremental)
	require.Equal(t, store.MetadataCacheGeneration(), first.cacheGen)
	require.Equal(t, store.LibraryGeneration().Value(), first.bookGen)

	// Nothing changed: an incremental build that re-reads nothing.
	*clock = clock.Add(time.Minute)
	same, err := b.build(ctx, first)
	require.NoError(t, err)
	require.True(t, same.incremental)
	require.Equal(t, keyOf(first), keyOf(same))
	require.Equal(t, first.builtAt, same.builtAt)

	rows := first.rows
	// A changed cache row (new candidates, newer FetchedAt: it moves to the
	// front), a deleted cache row, a new row, a book ruled on (a book write
	// the cache log does not see: its book must come back live), a book deleted (row
	// -> orphan), an orphan whose book comes back (orphan -> row), and a book
	// whose identity change deletes its cache row.
	changedID, deletedRowID, retitledID, deletedBookID, identityID := rows[3].sum.BookID, rows[5].sum.BookID, rows[7].sum.BookID, fileless.ID, rows[11].sum.BookID
	require.Contains(t, first.books, deletedBookID)
	entry, err := store.GetMetadataCache(changedID)
	require.NoError(t, err)
	entry.FetchedAt = time.Now().Add(time.Hour)
	entry.Candidates = entry.Candidates[:1]
	require.NoError(t, store.PutMetadataCache(entry))
	require.NoError(t, store.DeleteMetadataCache(deletedRowID))
	newBook, err := store.CreateBook(&database.Book{Title: "Brand New", FilePath: "/lib/New/Brand New", Format: "mp3"})
	require.NoError(t, err)
	require.NoError(t, store.PutMetadataCache(&database.MetadataCandidateCache{BookID: newBook.ID, FetchedAt: time.Now(), Candidates: []json.RawMessage{json.RawMessage(`{"title":"Brand New","author":"X","source":"audible"}`)}}))
	_, err = store.ModifyBook(retitledID, func(bk *database.Book) error { st := "no_match"; bk.MetadataReviewStatus = &st; return nil })
	require.NoError(t, err)
	require.NoError(t, store.DeleteBook(deletedBookID))
	// DeleteBook takes the cache row with it; put the row back so the book
	// is a true orphan (a cache row whose book is gone).
	require.NoError(t, store.PutMetadataCache(&database.MetadataCandidateCache{BookID: deletedBookID, FetchedAt: time.Now()}))
	require.Contains(t, first.orphanIDs, orphanID)
	_, err = store.CreateBook(&database.Book{ID: orphanID, Title: "Back", FilePath: "/lib/Back/" + orphanID, Format: "mp3"})
	require.NoError(t, err)
	asin := "B00INCR0001"
	_, err = store.ModifyBook(identityID, func(bk *database.Book) error { bk.ASIN = &asin; return nil })
	require.NoError(t, err)

	*clock = clock.Add(time.Minute)
	incr, err := b.build(ctx, first)
	require.NoError(t, err)
	require.True(t, incr.incremental)
	full, err := b.buildFull(ctx)
	require.NoError(t, err)
	require.Equal(t, keyOf(full), keyOf(incr))
	require.Equal(t, first.builtAt, incr.builtAt, "an incremental snapshot keeps the last full build's builtAt")
	require.Equal(t, store.MetadataCacheGeneration(), incr.cacheGen)
	require.Equal(t, store.LibraryGeneration().Value(), incr.bookGen)
	// And the transitions happened as named.
	ids := rowIDs(incr.rows)
	require.Equal(t, changedID, ids[0], "the refetched row sorts first by FetchedAt")
	require.NotContains(t, ids, deletedRowID)
	require.NotContains(t, ids, identityID, "the identity change deleted its cache row")
	require.NotContains(t, ids, deletedBookID)
	require.Contains(t, incr.orphanIDs, deletedBookID)
	require.NotContains(t, incr.orphanIDs, orphanID)
	require.Contains(t, ids, orphanID)
	require.Contains(t, ids, newBook.ID)
	require.NotNil(t, incr.books[retitledID].MetadataReviewStatus)
	require.Equal(t, "no_match", *incr.books[retitledID].MetadataReviewStatus)
	require.Nil(t, first.books[retitledID].MetadataReviewStatus, "the previous snapshot's book is untouched")
	require.Equal(t, 1, incr.rows[0].candidateCount)

	// A third build over the incremental one is incremental too.
	incr2, err := b.build(ctx, incr)
	require.NoError(t, err)
	require.True(t, incr2.incremental)
	require.Equal(t, keyOf(full), keyOf(incr2))
}

// TestReviewSnapshot_FullBuildWhen: no previous snapshot, a change log that
// cannot name the change, a store without the logs, a previous full build
// older than maxAge, or too many affected rows -- each is a full build.
func TestReviewSnapshot_FullBuildWhen(t *testing.T) {
	store, svc := reviewSeed(t, 40)
	b, clock := incrBuilder(t, store, svc)
	ctx := context.Background()
	first, err := b.build(ctx, nil)
	require.NoError(t, err)

	t.Run("cache log not ok", func(t *testing.T) {
		bb := *b
		bb.cacheChangedSince = func(uint64) ([]string, uint64, bool) { return nil, 0, false }
		s, err := bb.build(ctx, first)
		require.NoError(t, err)
		require.False(t, s.incremental)
	})
	t.Run("book log not ok", func(t *testing.T) {
		bb := *b
		bb.booksChangedSince = func(uint64) ([]string, uint64, bool) { return nil, 0, false }
		s, err := bb.build(ctx, first)
		require.NoError(t, err)
		require.False(t, s.incremental)
	})
	t.Run("no logs", func(t *testing.T) {
		bb := newReviewSnapshotBuilder(store, svc)
		s, err := bb.build(ctx, first)
		require.NoError(t, err)
		require.False(t, s.incremental)
	})
	t.Run("reset", func(t *testing.T) {
		// Reset's bumpAll: the cache log refuses, so the build is full (over
		// an empty store).
		st, sv := reviewSeed(t, 5)
		bb, _ := incrBuilder(t, st, sv)
		s0, err := bb.build(ctx, nil)
		require.NoError(t, err)
		require.NoError(t, st.Reset())
		s, err := bb.build(ctx, s0)
		require.NoError(t, err)
		require.False(t, s.incremental)
		require.Empty(t, s.rows)
	})
	t.Run("older than maxAge", func(t *testing.T) {
		*clock = first.builtAt.Add(reviewSnapshotMaxAge + time.Second)
		s, err := b.build(ctx, first)
		require.NoError(t, err)
		require.False(t, s.incremental)
		require.Equal(t, *clock, s.builtAt, "a full build resets builtAt")
		*clock = first.builtAt
	})
	t.Run("most rows affected", func(t *testing.T) {
		for _, r := range first.rows[:len(first.rows)*3/4] {
			require.NoError(t, store.DeleteMetadataCache(r.sum.BookID))
		}
		s, err := b.build(ctx, first)
		require.NoError(t, err)
		require.False(t, s.incremental)
	})
}

// writingStore is a store whose first batch book read during a build writes
// a cache row: the write lands DURING the build.
type writingStore struct {
	*database.PebbleStore
	once  sync.Once
	write func()
}

func (w *writingStore) GetBooksByIDs(ids []string) ([]database.Book, error) {
	w.once.Do(w.write)
	return w.PebbleStore.GetBooksByIDs(ids)
}

// TestReviewSnapshot_WriteDuringBuildIsPickedUpByTheNext: the generations
// are read at build BEGIN, so a row written while a build runs is named by
// the change log to the next (incremental) build, whether or not the first
// one happened to see it.
func TestReviewSnapshot_WriteDuringBuildIsPickedUpByTheNext(t *testing.T) {
	store, svc := reviewSeed(t, 30)
	ctx := context.Background()
	target := "written-during-build"
	book, err := store.CreateBook(&database.Book{ID: target, Title: "During", FilePath: "/lib/During", Format: "mp3"})
	require.NoError(t, err)
	ws := &writingStore{PebbleStore: store, write: func() {
		require.NoError(t, store.PutMetadataCache(&database.MetadataCandidateCache{BookID: book.ID, FetchedAt: time.Now().Add(time.Hour), Candidates: []json.RawMessage{json.RawMessage(`{"title":"During","author":"X","source":"audible"}`)}}))
	}}
	b, _ := incrBuilder(t, store, svc)
	b.store = ws
	first, err := b.build(ctx, nil)
	require.NoError(t, err)
	require.NotEqual(t, store.MetadataCacheGeneration(), first.cacheGen, "the write moved the generation after the build began")
	next, err := b.build(ctx, first)
	require.NoError(t, err)
	require.True(t, next.incremental)
	require.Equal(t, target, next.rows[0].sum.BookID)
	require.Equal(t, store.MetadataCacheGeneration(), next.cacheGen)
}

// TestLoadCacheRowsByID_MatchesTheFullLoader: the id-driven loader yields the
// same rows as the whole-cache loader for the same ids, and files each id
// under exactly one outcome.
func TestLoadCacheRowsByID_MatchesTheFullLoader(t *testing.T) {
	store, svc := reviewSeed(t, 80)
	ctx := context.Background()
	full, err := loadCacheRows(ctx, store, svc)
	require.NoError(t, err)
	ids := []string{"no-such-row"}
	want := map[string]loadedCacheRow{}
	for _, r := range full.rows[:20] {
		ids = append(ids, r.sum.BookID)
		want[r.sum.BookID] = r
	}
	ids = append(ids, full.orphanIDs...)
	got, err := loadCacheRowsByID(ctx, store, svc, ids)
	require.NoError(t, err)
	require.Equal(t, []string{"no-such-row"}, got.goneIDs)
	require.ElementsMatch(t, full.orphanIDs, got.orphanIDs)
	require.Empty(t, got.failedIDs)
	require.Len(t, got.rows, 20)
	for _, g := range got.rows {
		w := want[g.sum.BookID]
		require.Equal(t, w.sum.BookID, g.sum.BookID)
		require.Equal(t, w.sum.FetchedAt.UTC(), g.sum.FetchedAt.UTC())
		require.Equal(t, w.candidateCount, g.candidateCount)
		require.Equal(t, w.first, g.first)
		require.Equal(t, w.lastChecked.UTC(), g.lastChecked.UTC())
		require.Equal(t, w.searchable, g.searchable)
		require.Equal(t, w.files, g.files)
		require.Equal(t, w.book.ID, g.book.ID)
	}
}
