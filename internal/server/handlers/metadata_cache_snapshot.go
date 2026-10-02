// file: internal/server/handlers/metadata_cache_snapshot.go
// version: 1.3.0
// guid: 9d3c7a51-2e6b-4f08-b4a9-7c1e5f2d8a36
// last-edited: 2026-10-02

package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

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
// A rebuild is triggered, and the CURRENT snapshot served meanwhile
// (stale-while-revalidate), when:
//
//   - the metadata cache was written since the snapshot's build began
//     (database.PebbleStore.MetadataCacheGeneration: every Put and Delete of
//     a cache row moves it) -- the normal trigger, so paging an idle library
//     rebuilds nothing;
//   - a handler action marked it dirty (apply dispatch, clear no-match);
//   - it is older than reviewSnapshotMaxAge, a safety net for what the write
//     counter cannot see: a retitle or a file change that alters a row's
//     searchability or runtime without touching the cache.
//
// A build allocates on the order of a GB at production scale, so none of these
// is a timer: a rebuild only ever follows a request that found the snapshot
// out of date. After reviewSnapshotIdleDrop with no request the snapshot is
// dropped (the next visit builds again), and after a failed build no new build
// starts for reviewSnapshotFailBackoff.
const (
	reviewSnapshotMaxAge      = 30 * time.Minute
	reviewSnapshotIdleDrop    = 15 * time.Minute
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

// reviewSnapshotCache holds the current snapshot and rebuilds it
// single-flight. Safe for concurrent use.
type reviewSnapshotCache struct {
	build func(ctx context.Context) (*reviewSnapshot, error)
	// gen reads the metadata cache's write counter; nil when the store has
	// none, and then only invalidation and age trigger a rebuild.
	gen         func() uint64
	maxAge      time.Duration
	idleDrop    time.Duration
	failBackoff time.Duration
	now         func() time.Time
	// spawn starts a background rebuild's goroutine. The server sets it to
	// its tracked background group (SetBackgroundRunner), so shutdown waits
	// for a rebuild instead of closing the store under it.
	spawn func(func())

	mu sync.Mutex
	// baseCtx parents background rebuilds: the server's lifetime context once
	// warm has been called, so shutdown stops a rebuild.
	baseCtx  context.Context
	snap     *reviewSnapshot
	invalGen uint64
	failedAt time.Time
	failErr  error
	lastUse  time.Time
	idle     *time.Timer
	// refreshing is set while a background rebuild goroutine runs, so a burst
	// of out-of-date requests starts one goroutine, not one each.
	refreshing bool
	sf         singleflight.Group
}

func newReviewSnapshotCache(build func(ctx context.Context) (*reviewSnapshot, error), gen func() uint64) *reviewSnapshotCache {
	return &reviewSnapshotCache{
		build: build, gen: gen,
		maxAge: reviewSnapshotMaxAge, idleDrop: reviewSnapshotIdleDrop, failBackoff: reviewSnapshotFailBackoff,
		now: time.Now, baseCtx: context.Background(),
		spawn: func(f func()) { go f() },
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

// touch records a request and (re)arms the idle drop. Caller holds mu.
func (c *reviewSnapshotCache) touch() {
	c.lastUse = c.now()
	if c.idleDrop <= 0 {
		return
	}
	if c.idle == nil {
		c.idle = time.AfterFunc(c.idleDrop, c.dropIfIdle)
		return
	}
	c.idle.Reset(c.idleDrop)
}

func (c *reviewSnapshotCache) dropIfIdle() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.now().Sub(c.lastUse) >= c.idleDrop {
		c.snap = nil
	}
}

// get returns the current snapshot. With none (first request after a restart
// or an idle drop) it builds one and waits; a client that disconnects does not
// cancel a build other requests share. With one that is out of date it
// returns it at once and starts a background rebuild.
func (c *reviewSnapshotCache) get(ctx context.Context) (*reviewSnapshot, error) {
	c.mu.Lock()
	c.touch()
	snap := c.snap
	backoff := c.inBackoff()
	failErr := c.failErr
	stale := snap != nil && c.outOfDate(snap)
	c.mu.Unlock()
	if snap == nil {
		if backoff {
			return nil, errors.Join(errReviewBuildBackoff, failErr)
		}
		ch := c.sf.DoChan("build", func() (any, error) { return c.rebuild(context.WithoutCancel(ctx)) })
		select {
		case res := <-ch:
			if res.Err != nil {
				return nil, res.Err
			}
			return res.Val.(*reviewSnapshot), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if stale && !backoff {
		c.refreshAsync()
	}
	return snap, nil
}

// refreshAsync starts a rebuild unless one is already running (or the
// server is shutting down).
func (c *reviewSnapshotCache) refreshAsync() {
	c.mu.Lock()
	ctx := c.baseCtx
	if c.refreshing || ctx.Err() != nil {
		c.mu.Unlock()
		return
	}
	c.refreshing = true
	spawn := c.spawn
	c.mu.Unlock()
	spawn(func() {
		defer func() {
			c.mu.Lock()
			c.refreshing = false
			c.mu.Unlock()
		}()
		// Do, not DoChan: the rebuild runs on THIS goroutine, the one the
		// server tracks; a cold build already running is joined.
		_, err, _ := c.sf.Do("build", func() (any, error) { return c.rebuild(ctx) })
		if err != nil && ctx.Err() == nil {
			metadataCacheLog.Warn("background review snapshot rebuild failed; serving the previous snapshot: %v", err)
		}
	})
}

// setBackground makes ctx the parent of background rebuilds and spawn the
// way their goroutines start.
func (c *reviewSnapshotCache) setBackground(ctx context.Context, spawn func(func())) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ctx != nil {
		c.baseCtx = ctx
	}
	if spawn != nil {
		c.spawn = spawn
	}
}

func (c *reviewSnapshotCache) rebuild(ctx context.Context) (*reviewSnapshot, error) {
	c.mu.Lock()
	startInval := c.invalGen
	c.mu.Unlock()
	startGen := c.cacheGen()
	began := time.Now()
	snap, err := c.build(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.failedAt, c.failErr = c.now(), err
		return nil, err
	}
	c.failErr = nil
	snap.cacheGen, snap.invalGen = startGen, startInval
	c.snap = snap
	metadataCacheLog.Info("review snapshot built: rows=%d orphaned=%d took=%s", len(snap.rows), snap.orphaned, time.Since(began).Round(time.Millisecond))
	return snap, nil
}

// warm builds the snapshot now, under ctx, and makes ctx the parent of every
// later background rebuild.
func (c *reviewSnapshotCache) warm(ctx context.Context) error {
	c.mu.Lock()
	c.baseCtx = ctx
	c.touch()
	c.mu.Unlock()
	_, err, _ := c.sf.Do("build", func() (any, error) { return c.rebuild(ctx) })
	return err
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
// "no match", applied and the book's own fields come from the live row, and a
// book that has gone since the snapshot was built is counted orphaned.
type reviewOverlay struct {
	rows     []snapshotRow
	orphaned int
	books    map[string]*database.Book
}

// overlayLiveBooks re-reads the books of snap's rows in one batch. With want
// non-nil (an ids= lookup) only those rows are kept and only their books are
// read: a page's detail fetch must cost the page, not the library. The
// summary counts over such an overlay cover only the asked rows.
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
	fetched, err := store.GetBooksByIDs(ids)
	if err != nil {
		return reviewOverlay{}, err
	}
	books := make(map[string]*database.Book, len(fetched))
	for i := range fetched {
		books[fetched[i].ID] = &fetched[i]
	}
	out := reviewOverlay{rows: make([]snapshotRow, 0, len(rows)), orphaned: snap.orphaned, books: books}
	for _, r := range rows {
		b := books[r.sum.BookID]
		if b == nil {
			// The same rule as loadCacheRows' lookupBook: a batch miss is
			// confirmed by a point read before the row is called orphaned.
			if pb, perr := store.GetBookByID(r.sum.BookID); perr == nil && pb != nil {
				b = pb
				books[r.sum.BookID] = pb
			}
		}
		if b == nil {
			out.orphaned++
			continue
		}
		r.book = b
		out.rows = append(out.rows, r)
	}
	return out, nil
}
