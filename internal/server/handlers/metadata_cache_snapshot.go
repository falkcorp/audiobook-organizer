// file: internal/server/handlers/metadata_cache_snapshot.go
// version: 1.4.0
// guid: 9d3c7a51-2e6b-4f08-b4a9-7c1e5f2d8a36
// last-edited: 2026-10-02

package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// What the review snapshot holds, and when it is rebuilt.
//
// The snapshot is the expensive, cache-derived half of the review listing
// (cache reads, the legacy candidate filter, the search-title resolver, file
// facts, the candidate decode). Every request re-reads the books themselves
// (overlayLiveBooks), so review status, applied, "no match" and deleted books
// are always live; only what the snapshot derived can lag.
//
// The snapshot goes out of date, and the next request is served it while a
// background rebuild runs (stale-while-revalidate), when:
//
//   - the metadata cache was written since the snapshot's build began
//     (database.PebbleStore.MetadataCacheGeneration, which every writer of the
//     keyspace moves, including UpdateBook's identity-change delete) -- the
//     normal trigger, so paging an unchanged cache rebuilds nothing;
//   - a handler action marked it (apply dispatch, clear no-match);
//   - nobody asked for reviewSnapshotIdleMark: the next visit after a long
//     idle is served the old snapshot at once and refreshes it, rather than
//     waiting for a cold build;
//   - it is older than reviewSnapshotMaxAge, a safety net for what the write
//     counter cannot see: a retitle or a file change that alters a row's
//     searchability or runtime without touching the cache.
//
// A build allocates on the order of a GB at production scale, so none of these
// rebuilds on a timer -- a rebuild only follows a request that found the
// snapshot out of date -- and no rebuild starts within reviewSnapshotMinInterval
// of the last one began (a fetch op Puts continuously; the snapshot is served
// meanwhile). After a failed build no new build starts for
// reviewSnapshotFailBackoff. Every build, cold or background, runs on a
// goroutine the server tracks (spawn) under its lifetime context.
const (
	reviewSnapshotMaxAge      = 30 * time.Minute
	reviewSnapshotIdleMark    = 15 * time.Minute
	reviewSnapshotMinInterval = 45 * time.Second
	reviewSnapshotFailBackoff = 30 * time.Second
)

// snapshotRow is one loaded cache row plus the per-row work every review
// request used to redo: decoding the first stored candidate and hashing it.
// Its book is nil: the overlay supplies the live one.
type snapshotRow struct {
	loadedCacheRow
	// cand is the decoded first candidate; nil when the entry holds none or
	// the first one will not decode (decodeErr says why).
	cand      *metafetch.MetadataCandidate
	decodeErr error
	// hash is metafetch.CandidateHash(*cand), what every review-page apply
	// button echoes back in its pin.
	hash string
}

// reviewSnapshot is never mutated after it is published.
type reviewSnapshot struct {
	builtAt  time.Time
	rows     []snapshotRow
	orphaned int
	// cacheGen and invalGen are the write counter and the invalidation
	// counter read when this snapshot's build BEGAN: a write that raced the
	// build may not be in it, so it must still count as newer.
	cacheGen uint64
	invalGen uint64
}

func buildReviewSnapshot(ctx context.Context, store cacheRowBookReader, svc cacheRowCandidateReader) (*reviewSnapshot, error) {
	set, err := loadCacheRows(ctx, store, svc)
	if err != nil {
		return nil, err
	}
	snap := &reviewSnapshot{builtAt: time.Now(), rows: make([]snapshotRow, len(set.rows)), orphaned: set.orphaned}
	undecodable := 0
	var firstUndecodable string
	for i, r := range set.rows {
		sr := snapshotRow{loadedCacheRow: r}
		if r.first != nil {
			var cand metafetch.MetadataCandidate
			if derr := json.Unmarshal(r.first, &cand); derr != nil {
				sr.decodeErr = derr
				undecodable++
				if firstUndecodable == "" {
					firstUndecodable = r.sum.BookID
				}
			} else {
				sr.cand = &cand
				sr.hash = metafetch.CandidateHash(cand)
			}
		}
		// Neither is read after this point: the decoded candidate replaces
		// the raw one, and the overlay replaces the book.
		sr.first = nil
		sr.book = nil
		snap.rows[i] = sr
	}
	if undecodable > 0 {
		// One line per build, not one per row: the same rows fail on every
		// rebuild, and the review summary already counts them (`errors`).
		metadataCacheLog.Warn("review snapshot: %d stored candidates will not decode (first: bookID=%s); counted under errors", undecodable, firstUndecodable)
	}
	return snap, nil
}

// errReviewBuildBackoff is returned while a failed build's backoff runs.
var errReviewBuildBackoff = errors.New("review listing unavailable: the last build failed; retrying shortly")

// errReviewBuildNotStarted is a build the server refused to start (shutting down).
var errReviewBuildNotStarted = errors.New("review listing unavailable: the server is shutting down")

// buildCall is one build in flight, shared by every request that waits on it.
type buildCall struct {
	done chan struct{}
	snap *reviewSnapshot
	err  error
}

// reviewSnapshotCache holds the current snapshot and runs at most one build at
// a time. Safe for concurrent use.
type reviewSnapshotCache struct {
	build func(ctx context.Context) (*reviewSnapshot, error)
	// gen reads the metadata cache's write counter; nil when the store has
	// none, and then only marks and age make the snapshot out of date.
	gen         func() uint64
	maxAge      time.Duration
	idleMark    time.Duration
	minInterval time.Duration
	failBackoff time.Duration
	now         func() time.Time

	mu sync.Mutex
	// baseCtx parents every build: the server's lifetime context once
	// setBackground or warm has been called.
	baseCtx context.Context
	// spawn starts a build's goroutine and reports whether it did. The server
	// sets it to its tracked background group, so shutdown waits for a build
	// instead of closing the store under it, and refuses once shutting down.
	spawn     func(func()) bool
	snap      *reviewSnapshot
	invalGen  uint64
	inflight  *buildCall
	lastStart time.Time
	failedAt  time.Time
	failErr   error
	idle      *time.Timer
}

func newReviewSnapshotCache(build func(ctx context.Context) (*reviewSnapshot, error), gen func() uint64) *reviewSnapshotCache {
	return &reviewSnapshotCache{
		build: build, gen: gen,
		maxAge: reviewSnapshotMaxAge, idleMark: reviewSnapshotIdleMark,
		minInterval: reviewSnapshotMinInterval, failBackoff: reviewSnapshotFailBackoff,
		now: time.Now, baseCtx: context.Background(),
		spawn: func(f func()) bool { go f(); return true },
	}
}

func (c *reviewSnapshotCache) cacheGen() uint64 {
	if c.gen == nil {
		return 0
	}
	return c.gen()
}

// outOfDate reports whether snap no longer reflects the cache. Caller holds mu.
func (c *reviewSnapshotCache) outOfDate(snap *reviewSnapshot) bool {
	return snap.invalGen != c.invalGen || snap.cacheGen != c.cacheGen() || c.now().Sub(snap.builtAt) > c.maxAge
}

// inBackoff reports whether a failed build is still backing off. Caller holds mu.
func (c *reviewSnapshotCache) inBackoff() bool {
	return c.failErr != nil && c.now().Sub(c.failedAt) < c.failBackoff
}

// touch (re)arms the idle mark. Caller holds mu.
func (c *reviewSnapshotCache) touch() {
	if c.idleMark <= 0 {
		return
	}
	if c.idle == nil {
		c.idle = time.AfterFunc(c.idleMark, c.markIdle)
		return
	}
	c.idle.Reset(c.idleMark)
}

// markIdle marks the snapshot out of date after reviewSnapshotIdleMark with no
// request. It is a mark, not a drop: the snapshot is still served to the next
// visit while it refreshes. A build running across the mark began before it,
// so the snapshot it publishes is still out of date -- the same rule as any
// invalidation.
func (c *reviewSnapshotCache) markIdle() {
	c.invalidate()
}

// startBuild returns the build in flight, or starts one on a tracked goroutine.
// Caller holds mu.
func (c *reviewSnapshotCache) startBuild() *buildCall {
	if c.inflight != nil {
		return c.inflight
	}
	bc := &buildCall{done: make(chan struct{})}
	ctx := c.baseCtx
	if ctx.Err() != nil {
		bc.err = errReviewBuildNotStarted
		close(bc.done)
		return bc
	}
	c.inflight = bc
	c.lastStart = c.now()
	startInval, startGen := c.invalGen, c.cacheGen()
	run := func() {
		began := time.Now()
		snap, err := c.build(ctx)
		c.mu.Lock()
		c.inflight = nil
		if err != nil {
			c.failedAt, c.failErr = c.now(), err
		} else {
			c.failErr = nil
			snap.cacheGen, snap.invalGen = startGen, startInval
			c.snap = snap
		}
		c.mu.Unlock()
		if err != nil {
			if ctx.Err() == nil {
				metadataCacheLog.Warn("review snapshot build failed: %v", err)
			}
		} else {
			metadataCacheLog.Info("review snapshot built: rows=%d orphaned=%d took=%s", len(snap.rows), snap.orphaned, time.Since(began).Round(time.Millisecond))
		}
		bc.snap, bc.err = snap, err
		close(bc.done)
	}
	if !c.spawn(run) {
		c.inflight = nil
		bc.err = errReviewBuildNotStarted
		close(bc.done)
	}
	return bc
}

// get returns the current snapshot. With none (the first request after a
// restart) it starts a build, or joins the one running, and waits for it; a
// client that disconnects stops waiting but does not cancel the build. With one
// that is out of date it returns it at once and starts a background rebuild,
// unless one began within minInterval or a failure is backing off.
func (c *reviewSnapshotCache) get(ctx context.Context) (*reviewSnapshot, error) {
	c.mu.Lock()
	c.touch()
	snap := c.snap
	if snap == nil {
		if c.inBackoff() && c.inflight == nil {
			err := errors.Join(errReviewBuildBackoff, c.failErr)
			c.mu.Unlock()
			return nil, err
		}
		bc := c.startBuild()
		c.mu.Unlock()
		select {
		case <-bc.done:
			return bc.snap, bc.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if c.outOfDate(snap) && !c.inBackoff() && c.now().Sub(c.lastStart) >= c.minInterval {
		c.startBuild()
	}
	c.mu.Unlock()
	return snap, nil
}

// setBackground makes ctx the parent of every build and spawn the way their
// goroutines start.
func (c *reviewSnapshotCache) setBackground(ctx context.Context, spawn func(func()) bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ctx != nil {
		c.baseCtx = ctx
	}
	if spawn != nil {
		c.spawn = spawn
	}
}

// warm builds the snapshot now (or joins the build running) and waits for it.
func (c *reviewSnapshotCache) warm(ctx context.Context) error {
	c.mu.Lock()
	if ctx != nil {
		c.baseCtx = ctx
	}
	c.touch()
	bc := c.startBuild()
	c.mu.Unlock()
	<-bc.done
	return bc.err
}

// invalidate marks the snapshot out of date: the next request is served the
// current one and starts a rebuild.
func (c *reviewSnapshotCache) invalidate() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.invalGen++
	c.mu.Unlock()
}

// reviewOverlay is a snapshot with its rows' books read NOW: review status,
// "no match", applied and the book's own fields come from the live row. A
// book that has gone since the snapshot was built is counted orphaned; a book
// whose read FAILED is counted in readErrors -- it may well still exist, and
// calling it orphaned would point an operator at the reaper for a store fault.
type reviewOverlay struct {
	rows       []snapshotRow
	orphaned   int
	readErrors int
	books      map[string]*database.Book
}

// errOverlayAllReadsFailed: not one of the overlay's book reads succeeded.
var errOverlayAllReadsFailed = errors.New("every review book read failed")

// overlayLiveBooks re-reads the books of snap's rows. With want non-nil (an
// ids= lookup) only those rows are kept and only their books are read: a
// page's detail fetch must cost the page, not the library. The summary counts
// over such an overlay cover only the asked rows.
//
// The rows are read in one batch. A failed batch is logged and the rows are
// read one by one instead; a row the batch missed is confirmed by a point
// read too. A failed point read is a readErrors row, not an orphan. Only when
// every read failed is the overlay an error.
func overlayLiveBooks(snap *reviewSnapshot, store cacheRowBookReader, want map[string]bool) (reviewOverlay, error) {
	rows := snap.rows
	if want != nil {
		rows = make([]snapshotRow, 0, len(want))
		for _, r := range snap.rows {
			if want[r.sum.BookID] {
				rows = append(rows, r)
			}
		}
	}
	ids := make([]string, len(rows))
	for i := range rows {
		ids[i] = rows[i].sum.BookID
	}
	books := make(map[string]*database.Book, len(ids))
	batchOK := false
	if fetched, err := store.GetBooksByIDs(ids); err != nil {
		metadataCacheLog.Warn("review overlay: batch book read failed; reading %d books one by one: %v", len(ids), err)
	} else {
		batchOK = true
		for i := range fetched {
			books[fetched[i].ID] = &fetched[i]
		}
	}
	out := reviewOverlay{rows: make([]snapshotRow, 0, len(rows)), orphaned: snap.orphaned, books: books}
	reads := 0
	if batchOK {
		reads = 1
	}
	for _, r := range rows {
		b := books[r.sum.BookID]
		if b == nil {
			pb, perr := store.GetBookByID(r.sum.BookID)
			if perr != nil {
				out.readErrors++
				continue
			}
			reads++
			if pb == nil {
				out.orphaned++
				continue
			}
			b = pb
			books[r.sum.BookID] = pb
		}
		r.book = b
		out.rows = append(out.rows, r)
	}
	if len(rows) > 0 && reads == 0 {
		return reviewOverlay{}, errOverlayAllReadsFailed
	}
	if out.readErrors > 0 {
		metadataCacheLog.Warn("review overlay: %d book reads failed; those rows are counted under errors, not orphaned", out.readErrors)
	}
	return out, nil
}
