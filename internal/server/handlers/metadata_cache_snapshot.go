// file: internal/server/handlers/metadata_cache_snapshot.go
// version: 1.1.0
// guid: 9d3c7a51-2e6b-4f08-b4a9-7c1e5f2d8a36
// last-edited: 2026-10-02

package handlers

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// reviewSnapshotMaxAge is how old the review snapshot may get before a request
// triggers a background rebuild. The request that notices is still served the
// current snapshot (stale-while-revalidate), so no review request ever waits on
// a rebuild except the very first one after a restart.
//
// What the age bounds is narrow: every request re-reads the books themselves
// (reviewOverlay), so review status, "no match", applied, and orphaned books
// are always live. Only what the cache rows hold can lag: a book whose
// candidates a fetch wrote, refetched or removed in the last minute (plus one
// rebuild) shows its previous candidates until the rebuild lands.
const reviewSnapshotMaxAge = 60 * time.Second

// snapshotRow is one loaded cache row plus the per-row work every review
// request used to redo: decoding the first stored candidate and hashing it.
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

// reviewSnapshot is the expensive, book-independent part of the review
// listing: the cache rows, their candidates after the legacy filter, their
// searchability and their file facts. It is built by loadCacheRows -- the
// one loader -- and never mutated after it is published.
type reviewSnapshot struct {
	builtAt  time.Time
	rows     []snapshotRow
	orphaned int
}

func buildReviewSnapshot(ctx context.Context, store cacheRowBookReader, svc cacheRowCandidateReader) (*reviewSnapshot, error) {
	set, err := loadCacheRows(ctx, store, svc)
	if err != nil {
		return nil, err
	}
	snap := &reviewSnapshot{builtAt: time.Now(), rows: make([]snapshotRow, len(set.rows)), orphaned: set.orphaned}
	for i, r := range set.rows {
		sr := snapshotRow{loadedCacheRow: r}
		if r.entry != nil && len(r.entry.Candidates) > 1 {
			// The review path reads only Candidates[0] (and the entry's
			// timestamps), so the snapshot keeps only that one: ten stored
			// candidates with descriptions per row, for ~40k rows, held for
			// the server's lifetime (twice during a rebuild), is hundreds of
			// MB for nothing. A NEW one-element slice, not [:1], so the other
			// nine are not kept alive by the backing array.
			trimmed := *r.entry
			trimmed.Candidates = []json.RawMessage{r.entry.Candidates[0]}
			sr.entry = &trimmed
		}
		if r.entry != nil && len(r.entry.Candidates) > 0 {
			var cand metafetch.MetadataCandidate
			if derr := json.Unmarshal(r.entry.Candidates[0], &cand); derr != nil {
				sr.decodeErr = derr
				metadataCacheLog.Warn("review snapshot: stored candidate will not decode: bookID=%s err=%v", r.sum.BookID, derr)
			} else {
				sr.cand = &cand
				sr.hash = metafetch.CandidateHash(cand)
			}
		}
		snap.rows[i] = sr
	}
	return snap, nil
}

// reviewSnapshotCache holds the current snapshot and rebuilds it single-flight.
// Safe for concurrent use.
type reviewSnapshotCache struct {
	build  func(ctx context.Context) (*reviewSnapshot, error)
	maxAge time.Duration
	now    func() time.Time
	// baseCtx is the context background rebuilds run under: the server's
	// lifetime context once warm has been called, so shutdown stops a rebuild
	// instead of letting it read a closing store.
	baseCtx context.Context

	mu   sync.Mutex
	snap *reviewSnapshot
	// gen counts invalidations. A build records it when it starts and clears
	// dirty only if no invalidation landed while it ran: a write that raced the
	// build may not be in it.
	gen   uint64
	dirty bool
	sf    singleflight.Group
}

func newReviewSnapshotCache(build func(ctx context.Context) (*reviewSnapshot, error)) *reviewSnapshotCache {
	return &reviewSnapshotCache{build: build, maxAge: reviewSnapshotMaxAge, now: time.Now, baseCtx: context.Background()}
}

// warm builds the snapshot now, under ctx (the server's lifetime context), and
// makes ctx the parent of every later background rebuild. Called once at
// startup so the first visit after a restart is not the one that waits.
func (c *reviewSnapshotCache) warm(ctx context.Context) error {
	c.mu.Lock()
	c.baseCtx = ctx
	c.mu.Unlock()
	_, err, _ := c.sf.Do("build", func() (any, error) { return c.rebuild(ctx) })
	return err
}

// get returns the current snapshot. With none yet (first request after a
// restart) it builds one and waits for it; a client that disconnects does not
// cancel a build other requests share. With one that is past maxAge or marked
// dirty it returns it at once and starts a background rebuild.
func (c *reviewSnapshotCache) get(ctx context.Context) (*reviewSnapshot, error) {
	c.mu.Lock()
	snap := c.snap
	stale := snap == nil || c.dirty || c.now().Sub(snap.builtAt) > c.maxAge
	c.mu.Unlock()
	if snap == nil {
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
	if stale {
		c.refreshAsync()
	}
	return snap, nil
}

// refreshAsync starts a rebuild unless one is already running.
func (c *reviewSnapshotCache) refreshAsync() {
	c.mu.Lock()
	ctx := c.baseCtx
	c.mu.Unlock()
	c.sf.DoChan("build", func() (any, error) {
		snap, err := c.rebuild(ctx)
		if err != nil {
			metadataCacheLog.Warn("background review snapshot rebuild failed; serving the previous snapshot: %v", err)
		}
		return snap, err
	})
}

func (c *reviewSnapshotCache) rebuild(ctx context.Context) (*reviewSnapshot, error) {
	c.mu.Lock()
	startGen := c.gen
	c.mu.Unlock()
	began := time.Now()
	snap, err := c.build(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.snap = snap
	if c.gen == startGen {
		c.dirty = false
	}
	c.mu.Unlock()
	metadataCacheLog.Info("review snapshot built: rows=%d orphaned=%d took=%s", len(snap.rows), snap.orphaned, time.Since(began).Round(time.Millisecond))
	return snap, nil
}

// invalidate marks the snapshot dirty: the next request is served the current
// one and starts a rebuild.
func (c *reviewSnapshotCache) invalidate() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.gen++
	c.dirty = true
	c.mu.Unlock()
}

// reviewOverlay is a snapshot with every row's book re-read NOW, in one batch
// read: review status, "no match", applied and the book's own fields come
// from the live row, and a book that has gone since the snapshot was built is
// counted orphaned rather than listed.
type reviewOverlay struct {
	rows     []snapshotRow
	orphaned int
	books    map[string]*database.Book
}

func overlayLiveBooks(snap *reviewSnapshot, store cacheRowBookReader) reviewOverlay {
	ids := make([]string, len(snap.rows))
	for i := range snap.rows {
		ids[i] = snap.rows[i].sum.BookID
	}
	books := make(map[string]*database.Book, len(ids))
	fetched, err := store.GetBooksByIDs(ids)
	if err != nil {
		// The snapshot's own book rows are at most one rebuild old; serving
		// them beats failing the page.
		metadataCacheLog.Warn("live book overlay failed; serving the snapshot's book rows: %v", err)
		for i := range snap.rows {
			books[snap.rows[i].sum.BookID] = snap.rows[i].book
		}
	} else {
		for i := range fetched {
			books[fetched[i].ID] = &fetched[i]
		}
	}
	out := reviewOverlay{rows: make([]snapshotRow, 0, len(snap.rows)), orphaned: snap.orphaned, books: books}
	for _, r := range snap.rows {
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
	return out
}
