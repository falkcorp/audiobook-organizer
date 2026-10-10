// file: internal/server/handlers/metadata_cache_snapshot.go
// version: 2.2.1
// guid: 9d3c7a51-2e6b-4f08-b4a9-7c1e5f2d8a36
// last-edited: 2026-10-10

package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// What the review snapshot holds, and when it is rebuilt.
//
// The snapshot is the expensive, cache-derived half of the review listing
// (cache reads, the legacy candidate filter, the search-title resolver, file
// facts, the candidate decode), plus every row's book as read when the build
// began. A request re-reads only the books the library change log says were
// written since (overlayLiveBooks, database.BooksChangedSince), so review
// status, applied, "no match" and deleted books are always live; only what the
// snapshot derived from the cache can lag.
//
// The snapshot goes out of date, and the next request is served it while a
// background rebuild runs (stale-while-revalidate), when:
//
//   - the metadata cache was written since the snapshot's build began
//     (database.PebbleStore.MetadataCacheGeneration, which every writer of the
//     keyspace moves, including UpdateBook's identity-change delete) -- the
//     normal trigger, so paging an unchanged cache rebuilds nothing;
//   - a handler action marked it (apply dispatch, clear no-match), or an
//     overlay found more changed books than it will read per request;
//   - nobody asked for reviewSnapshotIdleMark: the next visit after a long
//     idle is served the old snapshot at once and refreshes it, rather than
//     waiting for a cold build;
//   - it is older than reviewSnapshotMaxAge, a safety net for what the write
//     counter cannot see: a retitle or a file change that alters a row's
//     searchability or runtime without touching the cache.
//
// A rebuild is INCREMENTAL when it can be (reviewSnapshotBuilder.build): the
// two change logs name the cache rows and books written since the previous
// snapshot's build began, only those rows are re-read, and every other row is
// carried over. An apply op deletes one cache row per book it applies, so on
// production every rebuild used to be a full one (56k rows, ~1 GB allocated,
// every 45 s for as long as the op ran); an incremental one re-reads the
// handful of rows that moved. A full build still runs when there is no
// previous snapshot, when a log cannot name the change (a Reset, a log
// overrun, a generation bumped without a record), or when the previous
// snapshot's FULL build is older than reviewSnapshotMaxAge -- an incremental
// snapshot inherits the builtAt of the last full build, so the age net still
// catches what neither log records.
//
// None of these rebuilds on a timer -- a rebuild only follows a request that
// found the snapshot out of date -- and no rebuild starts within
// reviewSnapshotMinInterval of the last one began (a fetch op Puts
// continuously; the snapshot is served meanwhile). After a failed build no new
// build starts for reviewSnapshotFailBackoff. Every build, cold or background,
// runs on a goroutine the server tracks (spawn) under its lifetime context.
const (
	reviewSnapshotMaxAge      = 30 * time.Minute
	reviewSnapshotIdleMark    = 15 * time.Minute
	reviewSnapshotMinInterval = 45 * time.Second
	reviewSnapshotFailBackoff = 30 * time.Second
	// reviewSnapshotIncrementalMax is the share of the previous snapshot's rows
	// past which an incremental build is not worth it: re-reading that many
	// rows by id costs more than one key-range scan of the whole cache.
	reviewSnapshotIncrementalMax = 0.5
	// overlayRebuildThreshold is how many changed books a request will re-read
	// before it asks for a rebuild instead: the per-request cost stays
	// bounded, and the rebuilt snapshot carries the new books.
	overlayRebuildThreshold = 2000
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
	// title is the book's title as read when the row was built, and
	// titleFold its strings.ToLower: what a substring Title filter compares
	// against (the server-side review query, metadata_cache_query.go), so a
	// keystroke does not lower-case every title in the library again. A book
	// the overlay re-read is matched on its live title instead
	// (snapshotRow.foldedTitle).
	title     string
	titleFold string
}

// foldedTitle returns strings.ToLower(title), from the precomputed fold when
// title equals the one the row was built with. The equality is a string
// comparison, not a pointer check: it compares lengths first and then the
// bytes, and the bytes compare is cheap in the usual case only because the
// title is normally the very same string the row was built from (the
// runtime's memequal returns early when both sides share a backing array).
func (r *snapshotRow) foldedTitle(title string) string {
	if title == r.title {
		return r.titleFold
	}
	return strings.ToLower(title)
}

// reviewSnapshot is never mutated after it is published.
type reviewSnapshot struct {
	// builtAt is when the last FULL build ran; an incremental build inherits
	// it, so reviewSnapshotMaxAge measures from the last full read of the
	// cache.
	builtAt time.Time
	// rows is every cache row whose book resolved when the row was last read,
	// ordered FetchedAt descending, BookID ascending (ListMetadataCacheKeys'
	// order, which the page's grouping and paging depend on).
	rows []snapshotRow
	// books is the book of every row as read when the row was last read; the
	// overlay replaces the ones written since (BooksChangedSince).
	books map[string]*database.Book
	// orphanIDs is every cache row whose book did not resolve.
	orphanIDs map[string]struct{}
	// cacheGen and bookGen are the metadata-cache and library generations read
	// when this snapshot's build BEGAN: a write that raced the build may not
	// be in it, so it must still count as newer, and the next build (or the
	// overlay) asks the change logs what moved after them. invalGen is the
	// invalidation counter read then.
	cacheGen uint64
	bookGen  uint64
	invalGen uint64
	// incremental records how this snapshot was built, for the build log line
	// and the tests.
	incremental bool
}

// orphaned counts the cache rows whose book did not resolve.
func (s *reviewSnapshot) orphaned() int { return len(s.orphanIDs) }

// changedSinceFunc is database.BooksChangedSince / MetadataCacheChangedSince:
// the ids written after gen, the generation that brings a reader up to date,
// and whether the list is complete.
type changedSinceFunc func(gen uint64) (ids []string, upTo uint64, ok bool)

// reviewSnapshotBuilder builds review snapshots, incrementally when it can.
type reviewSnapshotBuilder struct {
	store cacheRowBookReader
	svc   cacheRowCandidateReader
	// cacheGen and bookGen read the two generations; nil reads 0.
	cacheGen func() uint64
	bookGen  func() uint64
	// cacheChangedSince and booksChangedSince are the two change logs; nil
	// (a store without one) means every build is full.
	cacheChangedSince changedSinceFunc
	booksChangedSince changedSinceFunc
	maxAge            time.Duration
	now               func() time.Time
}

func newReviewSnapshotBuilder(store cacheRowBookReader, svc cacheRowCandidateReader) *reviewSnapshotBuilder {
	return &reviewSnapshotBuilder{store: store, svc: svc, maxAge: reviewSnapshotMaxAge, now: time.Now}
}

func readGen(f func() uint64) uint64 {
	if f == nil {
		return 0
	}
	return f()
}

// build returns a new snapshot: incremental over prev when prev exists, its
// last full build is within maxAge, and both change logs can name what was
// written since prev's generations (and it is less than
// reviewSnapshotIncrementalMax of prev's rows); else a full build. The
// generations are read at the BEGIN of the build, before any row.
func (b *reviewSnapshotBuilder) build(ctx context.Context, prev *reviewSnapshot) (*reviewSnapshot, error) {
	if prev != nil && b.cacheChangedSince != nil && b.booksChangedSince != nil && b.now().Sub(prev.builtAt) <= b.maxAge {
		cacheIDs, cacheUpTo, cacheOK := b.cacheChangedSince(prev.cacheGen)
		bookIDs, bookUpTo, bookOK := b.booksChangedSince(prev.bookGen)
		if cacheOK && bookOK {
			affected := make(map[string]struct{}, len(cacheIDs)+len(bookIDs))
			for _, id := range cacheIDs {
				affected[id] = struct{}{}
			}
			for _, id := range bookIDs {
				// A book write matters only to a row or an orphan: a book with
				// no cache row gets one only through a cache write, which the
				// cache log names.
				if _, isRow := prev.books[id]; isRow {
					affected[id] = struct{}{}
				} else if _, isOrphan := prev.orphanIDs[id]; isOrphan {
					affected[id] = struct{}{}
				}
			}
			if float64(len(affected)) <= reviewSnapshotIncrementalMax*float64(len(prev.rows)) {
				return b.buildIncremental(ctx, prev, affected, cacheUpTo, bookUpTo)
			}
		}
	}
	return b.buildFull(ctx)
}

// buildFull reads every cache row.
func (b *reviewSnapshotBuilder) buildFull(ctx context.Context) (*reviewSnapshot, error) {
	cacheGen, bookGen := readGen(b.cacheGen), readGen(b.bookGen)
	set, err := loadCacheRows(ctx, b.store, b.svc)
	if err != nil {
		return nil, err
	}
	snap := &reviewSnapshot{
		builtAt:   b.now(),
		rows:      make([]snapshotRow, len(set.rows)),
		books:     make(map[string]*database.Book, len(set.rows)),
		orphanIDs: make(map[string]struct{}, len(set.orphanIDs)),
		cacheGen:  cacheGen, bookGen: bookGen,
	}
	for _, id := range set.orphanIDs {
		snap.orphanIDs[id] = struct{}{}
	}
	var dec decodeTally
	for i, r := range set.rows {
		snap.books[r.sum.BookID] = r.book
		snap.rows[i] = dec.row(r)
	}
	dec.log()
	return snap, nil
}

// buildIncremental re-reads only the affected ids and carries every other row
// of prev over. A re-read row whose entry is gone is dropped; one whose book
// is gone becomes an orphan; an orphan whose book is back becomes a row. A
// read that FAILED (entry or book) makes the whole build a full one: the
// generations would move past the id while its row is missing or wrongly
// orphaned until the next full build, and a snapshot that guesses lives for
// up to maxAge. The rows are re-sorted into ListMetadataCacheKeys' order.
// builtAt is prev's: the age net measures from the last full build.
func (b *reviewSnapshotBuilder) buildIncremental(ctx context.Context, prev *reviewSnapshot, affected map[string]struct{}, cacheGen, bookGen uint64) (*reviewSnapshot, error) {
	ids := make([]string, 0, len(affected))
	for id := range affected {
		ids = append(ids, id)
	}
	sort.Strings(ids) // deterministic read order
	load, err := loadCacheRowsByID(ctx, b.store, b.svc, ids)
	if err != nil {
		return nil, err
	}
	if len(load.failedIDs) > 0 {
		metadataCacheLog.Warn("review snapshot: %d of %d re-read rows failed to read (first: bookID=%s); building in full instead", len(load.failedIDs), len(ids), load.failedIDs[0])
		return b.buildFull(ctx)
	}
	snap := &reviewSnapshot{
		builtAt:     prev.builtAt,
		rows:        make([]snapshotRow, 0, len(prev.rows)+len(load.rows)),
		books:       make(map[string]*database.Book, len(prev.books)+len(load.rows)),
		orphanIDs:   make(map[string]struct{}, len(prev.orphanIDs)+len(load.orphanIDs)),
		cacheGen:    cacheGen,
		bookGen:     bookGen,
		incremental: true,
	}
	keep := func(id string) bool {
		_, hit := affected[id]
		return !hit
	}
	for _, r := range prev.rows {
		if keep(r.sum.BookID) {
			snap.rows = append(snap.rows, r)
			snap.books[r.sum.BookID] = prev.books[r.sum.BookID]
		}
	}
	for id := range prev.orphanIDs {
		if keep(id) {
			snap.orphanIDs[id] = struct{}{}
		}
	}
	for _, id := range load.orphanIDs {
		snap.orphanIDs[id] = struct{}{}
	}
	var dec decodeTally
	for _, r := range load.rows {
		snap.books[r.sum.BookID] = r.book
		snap.rows = append(snap.rows, dec.row(r))
	}
	dec.log()
	sort.SliceStable(snap.rows, func(i, j int) bool {
		a, c := snap.rows[i].sum, snap.rows[j].sum
		if !a.FetchedAt.Equal(c.FetchedAt) {
			return a.FetchedAt.After(c.FetchedAt)
		}
		return a.BookID < c.BookID
	})
	return snap, nil
}

// decodeTally turns loaded rows into snapshot rows (the first candidate
// decoded and hashed) and counts the ones that will not decode, for one log
// line per build rather than one per row: the same rows fail on every
// rebuild, and the review summary already counts them (`errors`).
type decodeTally struct {
	undecodable int
	first       string
}

func (d *decodeTally) row(r loadedCacheRow) snapshotRow {
	sr := snapshotRow{loadedCacheRow: r}
	if r.first != nil {
		var cand metafetch.MetadataCandidate
		if derr := json.Unmarshal(r.first, &cand); derr != nil {
			sr.decodeErr = derr
			d.undecodable++
			if d.first == "" {
				d.first = r.sum.BookID
			}
		} else {
			sr.cand = &cand
			sr.hash = metafetch.CandidateHash(cand)
		}
	}
	if r.book != nil {
		sr.title = r.book.Title
		sr.titleFold = strings.ToLower(r.book.Title)
	}
	// Neither is read after this point: the decoded candidate replaces the
	// raw one, and the snapshot's books map (then the overlay) the book.
	sr.first = nil
	sr.book = nil
	return sr
}

func (d *decodeTally) log() {
	if d.undecodable > 0 {
		metadataCacheLog.Warn("review snapshot: %d stored candidates will not decode (first: bookID=%s); counted under errors", d.undecodable, d.first)
	}
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
	// build returns the next snapshot; prev is the one being replaced (nil on
	// a cold build), which an incremental build carries rows over from.
	build func(ctx context.Context, prev *reviewSnapshot) (*reviewSnapshot, error)
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

func newReviewSnapshotCache(build func(ctx context.Context, prev *reviewSnapshot) (*reviewSnapshot, error), gen func() uint64) *reviewSnapshotCache {
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
	// The build reads the cache and library generations itself, at its
	// begin; the invalidation counter is this cache's own, read here.
	startInval, prev := c.invalGen, c.snap
	run := func() {
		began := time.Now()
		snap, err := c.build(ctx, prev)
		c.mu.Lock()
		c.inflight = nil
		if err != nil {
			c.failedAt, c.failErr = c.now(), err
		} else {
			c.failErr = nil
			snap.invalGen = startInval
			c.snap = snap
		}
		c.mu.Unlock()
		if err != nil {
			if ctx.Err() == nil {
				metadataCacheLog.Warn("review snapshot build failed: %v", err)
			}
		} else {
			kind := "full"
			if snap.incremental {
				kind = "incremental"
			}
			metadataCacheLog.Info("review snapshot built: kind=%s rows=%d orphaned=%d took=%s", kind, len(snap.rows), snap.orphaned(), time.Since(began).Round(time.Millisecond))
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

// requestRebuild marks the snapshot out of date AND starts the rebuild now,
// under get's rules (not during a failure's backoff, not within minInterval
// of the last start). An overlay that found more changed books than it will
// read per request calls it: waiting for the next request to start the
// rebuild would make that request pay the same cost again.
func (c *reviewSnapshotCache) requestRebuild() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.invalGen++
	if c.snap != nil && !c.inBackoff() && c.now().Sub(c.lastStart) >= c.minInterval {
		c.startBuild()
	}
}

// reviewOverlay is a snapshot with the books written since its build read
// NOW: review status, "no match", applied and the book's own fields come from
// the live row. A book that has gone since the snapshot was built is counted
// orphaned; a book whose read FAILED is counted in readErrors -- it may well
// still exist, and calling it orphaned would point an operator at the reaper
// for a store fault.
type reviewOverlay struct {
	rows       []snapshotRow
	orphaned   int
	readErrors int
	// rebuild is set when the overlay could not bound its reads to the books
	// that changed (no change log, or the log could not name them), had to
	// read more than overlayRebuildThreshold of the snapshot's books, or had
	// a book read fail: the handler asks the snapshot cache for a rebuild so
	// the next request does not pay (or miss) this again.
	rebuild bool
	snap    *reviewSnapshot
	// live is every book read for this request, keyed by id; a nil value is a
	// book that no longer exists.
	live map[string]*database.Book
}

// book returns the live book when this request read it, else the snapshot's.
func (o reviewOverlay) book(id string) *database.Book {
	if b, read := o.live[id]; read {
		return b
	}
	return o.snap.books[id]
}

// errOverlayAllReadsFailed: not one of the overlay's book reads succeeded.
var errOverlayAllReadsFailed = errors.New("every review book read failed")

// overlayLiveBooks re-reads the books of snap's rows that were written since
// the snapshot's build began (booksChangedSince over snap.bookGen) and serves
// every other row the book the snapshot holds. With want non-nil (an ids=
// lookup) only those rows are kept and only their changed books are read: a
// page's detail fetch must cost the page, not the library. The summary counts
// over such an overlay cover only the asked rows.
//
// Without a change log, or when it cannot name the change, every row's book
// is read as before and a rebuild is requested; so is one when more than
// overlayRebuildThreshold of the snapshot's OWN books changed (a library scan
// writes every book, most with no cache row; those cost this request
// nothing and must not keep asking for rebuilds), after reading them.
//
// The books are read in one batch. A failed batch is logged and the books are
// read one by one instead; a book the batch missed is confirmed by a point
// read too. A failed point read is a readErrors row, not an orphan, and asks
// for a rebuild. Only when every read failed is the overlay an error.
func overlayLiveBooks(snap *reviewSnapshot, store cacheRowBookReader, want map[string]bool, booksChangedSince changedSinceFunc) (reviewOverlay, error) {
	out := reviewOverlay{orphaned: snap.orphaned(), snap: snap}
	keepRow := func(id string) bool { return want == nil || want[id] }

	var toRead []string
	if booksChangedSince == nil {
		out.rebuild = true
	} else if changed, _, ok := booksChangedSince(snap.bookGen); !ok {
		out.rebuild = true
	} else {
		toRead = make([]string, 0, len(changed))
		for _, id := range changed {
			if _, isRow := snap.books[id]; isRow && keepRow(id) {
				toRead = append(toRead, id)
			}
		}
		if len(toRead) > overlayRebuildThreshold {
			out.rebuild = true
		}
	}
	if toRead == nil {
		// Every row's book, as before the change log.
		toRead = make([]string, 0, len(snap.rows))
		for _, r := range snap.rows {
			if keepRow(r.sum.BookID) {
				toRead = append(toRead, r.sum.BookID)
			}
		}
	}

	out.live = make(map[string]*database.Book, len(toRead))
	failed := map[string]struct{}{}
	reads := 0
	if len(toRead) > 0 {
		if fetched, err := store.GetBooksByIDs(toRead); err != nil {
			metadataCacheLog.Warn("review overlay: batch book read failed; reading %d books one by one: %v", len(toRead), err)
		} else {
			reads++
			for i := range fetched {
				out.live[fetched[i].ID] = &fetched[i]
			}
		}
		for _, id := range toRead {
			if _, hit := out.live[id]; hit {
				continue
			}
			pb, perr := store.GetBookByID(id)
			if perr != nil {
				out.readErrors++
				failed[id] = struct{}{}
				continue
			}
			reads++
			out.live[id] = pb // nil: the book is gone
		}
	}

	out.rows = make([]snapshotRow, 0, len(snap.rows))
	for _, r := range snap.rows {
		id := r.sum.BookID
		if !keepRow(id) {
			continue
		}
		if _, f := failed[id]; f {
			continue
		}
		b := out.book(id)
		if b == nil {
			out.orphaned++
			continue
		}
		r.book = b
		out.rows = append(out.rows, r)
	}
	if len(toRead) > 0 && reads == 0 {
		return reviewOverlay{}, errOverlayAllReadsFailed
	}
	if out.readErrors > 0 {
		out.rebuild = true
		metadataCacheLog.Warn("review overlay: %d book reads failed; those rows are counted under errors, not orphaned, and a rebuild is requested", out.readErrors)
	}
	return out, nil
}
