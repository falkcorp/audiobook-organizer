// file: internal/metafetch/asin_backfill_queue.go
// version: 1.0.0
// guid: 3d8a5f21-9c47-4e0b-b6d2-71f4e0a9c853
// last-edited: 2026-10-02

package metafetch

// ASINBackfillQueue replaces the per-book ISBN/ASIN enrichment that ran after
// every metadata apply. That path (ISBNService.EnrichBookISBN) searched the
// configured source chain -- Google Books, Open Library -- and wrote ASINs
// from whatever it found. The owner's rule is that an ASIN comes from Audible
// only, so an apply now hands the book to metafetch.asin-backfill (Audible
// only, the strict gate, ASIN and ISBN in one op) instead of writing anything
// itself.
//
// Bursts coalesce. A bulk apply of N books must not enqueue N ops, so Add only
// records the id; one timer, debounce after the first Add, flushes every id
// collected so far into ONE op. And queued runs must not pile up behind a
// long run sharing the op's ConcurrencyKey (a full-library walk): while the
// last op this queue enqueued has not started yet, a flush keeps collecting
// ids and re-arms instead of enqueuing a second one. So at most one run from
// this queue is ever waiting, and every id that arrives meanwhile goes into
// the next one.

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// DefaultASINBackfillDebounce is how long the queue waits after the first
// book of a burst before enqueuing the run.
const DefaultASINBackfillDebounce = 30 * time.Second

// asinBackfillQueuedStatus is the operations_v2 status of a run that has not
// started.
const asinBackfillQueuedStatus = "queued"

var asinQueueLog = logger.New("metafetch.asin-backfill-queue")

// ASINBackfillEnqueueFunc enqueues one metafetch.asin-backfill run, live, for
// exactly these books, and returns its operation id.
type ASINBackfillEnqueueFunc func(ctx context.Context, bookIDs []string) (string, error)

// ASINBackfillStatusFunc returns an operation's status ("queued", "running",
// ...). An error or an unknown id is treated as "not waiting".
type ASINBackfillStatusFunc func(opID string) (string, error)

// ASINBackfillQueue coalesces per-book backfill requests into runs.
type ASINBackfillQueue struct {
	enqueue  ASINBackfillEnqueueFunc
	status   ASINBackfillStatusFunc
	debounce time.Duration
	// afterFunc is time.AfterFunc; tests replace it to fire flushes by hand.
	afterFunc func(time.Duration, func()) *time.Timer

	mu       sync.Mutex
	pending  map[string]struct{}
	armed    bool
	lastOpID string
}

// NewASINBackfillQueue builds a queue. debounce <= 0 uses
// DefaultASINBackfillDebounce. status may be nil (then a new run is enqueued
// on every flush that has ids).
func NewASINBackfillQueue(enqueue ASINBackfillEnqueueFunc, status ASINBackfillStatusFunc, debounce time.Duration) *ASINBackfillQueue {
	if debounce <= 0 {
		debounce = DefaultASINBackfillDebounce
	}
	return &ASINBackfillQueue{
		enqueue: enqueue, status: status, debounce: debounce,
		afterFunc: time.AfterFunc, pending: map[string]struct{}{},
	}
}

// Add records a book for the next run and arms the flush timer if it is not
// already armed.
func (q *ASINBackfillQueue) Add(bookID string) {
	if q == nil || bookID == "" {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pending[bookID] = struct{}{}
	q.armLocked()
}

func (q *ASINBackfillQueue) armLocked() {
	if q.armed {
		return
	}
	q.armed = true
	q.afterFunc(q.debounce, q.flush)
}

// Pending returns the ids waiting for the next run, sorted (tests, metrics).
func (q *ASINBackfillQueue) Pending() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.pendingLocked()
}

func (q *ASINBackfillQueue) pendingLocked() []string {
	ids := make([]string, 0, len(q.pending))
	for id := range q.pending {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// flush enqueues every pending id as one run, unless the previous run from
// this queue is still waiting to start: then the ids stay pending and the
// timer is re-armed.
func (q *ASINBackfillQueue) flush() {
	q.mu.Lock()
	q.armed = false
	if len(q.pending) == 0 {
		q.mu.Unlock()
		return
	}
	if q.lastOpID != "" && q.status != nil {
		if st, err := q.status(q.lastOpID); err == nil && st == asinBackfillQueuedStatus {
			q.armLocked()
			q.mu.Unlock()
			return
		}
	}
	ids := q.pendingLocked()
	q.pending = map[string]struct{}{}
	q.mu.Unlock()

	opID, err := q.enqueue(context.Background(), ids)
	if err != nil {
		// Dropped, not retried: an enqueue that fails (op not registered,
		// registry shutting down) would fail again. The scheduled
		// asin_backfill walk still reaches every book missing an ASIN.
		asinQueueLog.Warn("enqueue metafetch.asin-backfill for %d book(s) failed; they wait for the scheduled walk: %s",
			len(ids), logger.SanitizeLogValue(err.Error()))
		return
	}
	q.mu.Lock()
	q.lastOpID = opID
	q.mu.Unlock()
	asinQueueLog.Info("enqueued metafetch.asin-backfill %s for %d book(s) after metadata apply", opID, len(ids))
}

// needsIdentifierBackfill reports whether a book lacks an ASIN or any ISBN.
func needsIdentifierBackfill(book *database.Book) bool {
	if book == nil {
		return false
	}
	noISBN := (book.ISBN10 == nil || *book.ISBN10 == "") && (book.ISBN13 == nil || *book.ISBN13 == "")
	noASIN := book.ASIN == nil || *book.ASIN == ""
	return noISBN || noASIN
}

// queueIdentifierBackfill hands a just-applied book that lacks an ASIN or ISBN
// to metafetch.asin-backfill. It writes nothing itself.
func (mfs *Service) queueIdentifierBackfill(id string, book *database.Book) {
	if mfs.asinBackfillQueue == nil || !needsIdentifierBackfill(book) {
		return
	}
	mfs.asinBackfillQueue.Add(id)
}
