// file: internal/applycap/applycap.go
// version: 1.3.0
// guid: 4c1e8f2a-9b7d-4e63-a5c0-2f8d6b1e9a37
// last-edited: 2026-10-19
//
// Package applycap is the fail-safe ceiling on how many books one bulk APPLY
// may touch. It exists because several apply surfaces (metadata review, review
// queue, auto-match, batch update, reconcile) have at one time or another been
// triggered with "the whole list" as their target — a filter that matched
// everything, a queued-merge union, an empty scope that meant "all" — and
// nothing between the request and the writes ever asked "is that a plausible
// amount of work for a human to have meant?".
//
// The cap is a REFUSAL, never a truncation: an over-cap request is answered
// with an *ExceededError and ZERO writes happen. Truncating to the first N
// would silently apply a partial batch, which is exactly the class of surprise
// the cap is there to prevent. A caller that really wants more than the cap
// raises `bulk_apply_max_items` in config and re-issues the request.
//
// This package deliberately does not import internal/config. Callers pass the
// configured value in; a leaf package can be used from the review handler,
// which is intentionally decoupled from the global config (see
// handlers/review.New's applyEnabled gate for the pattern this follows).
package applycap

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Default is the ceiling used when the configured value is unset or invalid.
// 5,000 is roughly a tenth of the production library: large enough that any
// deliberately-selected batch fits, small enough that "everything" never does.
const Default = 5000

// Effective returns the cap to enforce for a configured value. Zero or a
// negative value means "not configured" and yields Default — it never means
// "unlimited". A zero written by a full-struct config save (see
// feedback: a `0` config value can become a silent kill switch) must not be
// able to disable the fail-safe.
func Effective(configured int) int {
	if configured <= 0 {
		return Default
	}
	return configured
}

// ExceededError is returned when a bulk apply would touch more items than the
// cap allows. It is a refusal: the caller must not have written anything.
type ExceededError struct {
	// Op names the surface that refused, for logs and the HTTP body.
	Op string
	// Requested is how many items the caller wanted to apply to.
	Requested int
	// Cap is the ceiling that was in force.
	Cap int
}

// Error renders the refusal in one line that names the config key, so the
// operator reading it knows both what happened and how to lift it on purpose.
func (e *ExceededError) Error() string {
	return fmt.Sprintf("%s: refusing to apply to %d items — the bulk apply cap is %d "+
		"(raise bulk_apply_max_items to allow more); nothing was applied", e.Op, e.Requested, e.Cap)
}

// Refuse returns the refusal for a batch of `requested` items when it exceeds
// the effective cap for `configured`, or nil when the whole batch may proceed.
// It returns the concrete pointer (not `error`) so HTTP handlers can hand it
// straight to httputil.RespondWithApplyCapExceeded without a type assertion.
func Refuse(op string, requested, configured int) *ExceededError {
	limit := Effective(configured)
	if requested > limit {
		return &ExceededError{Op: op, Requested: requested, Cap: limit}
	}
	return nil
}

// Check is Refuse for callers that propagate an `error` (op Run functions).
// It never returns a typed-nil interface: a passing check is a plain nil.
func Check(op string, requested, configured int) error {
	if ex := Refuse(op, requested, configured); ex != nil {
		return ex
	}
	return nil
}

// Fits reports whether `requested` items are within the effective cap. Used by
// queued-parameter merge functions, which must DECLINE a union that would
// exceed the cap (the registry then queues a second run) rather than error.
func Fits(requested, configured int) bool {
	return requested <= Effective(configured)
}

// Counter admits applies one at a time for streaming ops whose target set is
// not known up front (auto-match walks the whole library and decides per book).
// The (cap+1)th Admit returns *ExceededError; the caller must stop the op there.
// It is safe for concurrent use.
type Counter struct {
	op       string
	limit    int
	admitted atomic.Int64
}

// NewCounter builds a Counter enforcing the effective cap for `configured`.
func NewCounter(op string, configured int) *Counter {
	return &Counter{op: op, limit: Effective(configured)}
}

// Admit records one more apply and refuses it when it would be the (cap+1)th.
// On refusal the count is not advanced, so the count stays equal to the number
// of applies that were actually allowed.
func (c *Counter) Admit() error {
	n := c.admitted.Add(1)
	if n > int64(c.limit) {
		c.admitted.Add(-1)
		return &ExceededError{Op: c.op, Requested: int(n), Cap: c.limit}
	}
	return nil
}

// Cap returns the ceiling this counter enforces.
func (c *Counter) Cap() int { return c.limit }

// BatchLedger is the cross-request half of the cap, for a client that splits
// one logical bulk apply into several HTTP requests (the review lane applies
// 500 ids per chunk). Each chunk passes the per-request cap on its own, so a
// cooperating client that declares one batch total gets that total enforced
// across the chunks.
//
// What it guarantees, and what it does not. The ledger enforces the TOTAL a
// cooperating client DECLARES: a chunk of a named batch is refused when the
// declared total is over the cap, differs from the first chunk's, or the
// chunks add up past it. It is an accident guard, not an access control: a
// client that omits the batch id, or rotates it per request, is checked only
// per request (the caller applies Refuse to such a request), and there is no
// per-user rolling window. Because it binds only cooperating clients, losing
// an entry (an eviction, a restart, the TTL) loosens nothing that was not
// already loose, which is why a full ledger evicts instead of refusing.
// Precisely: an eviction mid-batch forgets that batch's running total, so a
// client that keeps sending past its declared total after its entry was
// evicted starts a fresh entry and is admitted again (fail-open for an
// over-sending client). That is accepted: evicting is preferred to refusing
// every other user once the ledger is full.
//
// A batch is keyed by owner (the authenticated user) AND batch id, so one
// user's id can neither collide with nor poison another's. Each owner may hold
// at most maxPerOwner live batches and the ledger maxBatches in all; past a
// limit the least recently touched batch is evicted (the owner's own first,
// then the oldest overall), never the new request refused. Entries are
// forgotten after ttl without a chunk. A chunk carries an index within its
// batch and a digest of its ids: a resent index with the same digest is
// reported as a resend (Admission.Resent), neither counted twice nor to be
// enqueued again, and the op id recorded for it (RecordOp) is handed back; a
// resent index with other ids is refused.
//
// Nothing is recorded for a refused request. It is safe for concurrent use.
type BatchLedger struct {
	mu          sync.Mutex
	ttl         time.Duration
	maxBatches  int
	maxPerOwner int
	now         func() time.Time
	batches     map[string]*batchEntry
}

type batchEntry struct {
	owner    string
	total    int
	admitted int
	chunks   map[int]*chunkRecord
	touched  time.Time
}

// chunkRecord is what the ledger remembers of one admitted chunk.
type chunkRecord struct {
	size   int
	digest string // of the chunk's ids; a resend must match it
	opID   string // the op the chunk was queued as; empty until RecordOp
}

// Admission is the result of an accepted Admit.
type Admission struct {
	// Resent: this index was admitted before with the same ids. Nothing was
	// counted; the caller must not enqueue it again (nor Release it) and
	// answers with OpID.
	Resent bool
	// OpID is the op the first send was queued as; empty when the first send
	// has not recorded one yet (it is still being queued).
	OpID string
}

// BatchErrorKind says why the ledger refused a chunk.
type BatchErrorKind int

const (
	// BatchTotalMismatch: the chunk declares a different total than the first
	// chunk of its batch (or resends an index with a different size).
	BatchTotalMismatch BatchErrorKind = iota + 1
	// BatchExceeded: the chunks of the batch would add up past its declared
	// total or past the cap.
	BatchExceeded
	// BatchChunkMismatch: a chunk index that was admitted before is resent
	// with different ids.
	BatchChunkMismatch
)

// BatchError is a ledger refusal of a chunk that is not a plain cap breach.
// Earlier chunks of the batch may already have been queued; the message says
// so rather than claiming nothing was applied.
type BatchError struct {
	Kind      BatchErrorKind
	Op        string
	Requested int
	Cap       int
	// Admitted is how many books of the batch were admitted before this chunk
	// (zero: no earlier chunk is recorded, so the message must not claim any
	// was queued).
	Admitted int
}

// Code is the stable error code the HTTP body carries.
func (e *BatchError) Code() string {
	switch e.Kind {
	case BatchTotalMismatch:
		return "BATCH_TOTAL_MISMATCH"
	case BatchChunkMismatch:
		return "BATCH_CHUNK_MISMATCH"
	}
	return "BATCH_EXCEEDED"
}

func (e *BatchError) Error() string {
	if e.Kind == BatchChunkMismatch {
		return fmt.Sprintf("%s: this chunk index was already sent with different books; a resent chunk must carry "+
			"the same books. Nothing was queued by this request", e.Op)
	}
	if e.Kind == BatchExceeded && e.Admitted == 0 {
		return fmt.Sprintf("%s: this chunk carries %d books, past the %d the batch declared or the bulk apply cap "+
			"allows; nothing from this batch was queued", e.Op, e.Requested, e.Cap)
	}
	if e.Kind == BatchTotalMismatch {
		return fmt.Sprintf("%s: this chunk does not match the batch it belongs to (a different declared total, or a "+
			"resent chunk of another size); chunks of the batch sent before this one were already queued, this one was not",
			e.Op)
	}
	return fmt.Sprintf("%s: the chunks of this batch add up to %d books, past the %d it declared or the bulk apply cap "+
		"allows; chunks sent before this one were already queued, this one was not", e.Op, e.Requested, e.Cap)
}

// NewBatchLedger builds a ledger holding at most maxBatches live batches, each
// forgotten after ttl without a chunk, and at most maxPerOwner per owner.
// Non-positive arguments get safe defaults (30 minutes, 1024, 16).
func NewBatchLedger(ttl time.Duration, maxBatches, maxPerOwner int) *BatchLedger {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	if maxBatches <= 0 {
		maxBatches = 1024
	}
	if maxPerOwner <= 0 {
		maxPerOwner = 16
	}
	return &BatchLedger{ttl: ttl, maxBatches: maxBatches, maxPerOwner: maxPerOwner, now: time.Now,
		batches: map[string]*batchEntry{}}
}

func batchKey(owner, id string) string { return owner + "\x00" + id }

// Admit records chunk `index` of `n` items (whose ids hash to `digest`) for
// batch `id` of `owner`, which declared `total`, or refuses it and records
// nothing. The error is nil, *ExceededError (the declared total is over the
// cap) or *BatchError. A resend of an admitted index with the same digest is
// accepted with Admission.Resent set and counts nothing.
func (l *BatchLedger) Admit(op, owner, id string, index, n, total, configured int, digest string) (Admission, error) {
	limit := Effective(configured)
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweepLocked(now)
	key := batchKey(owner, id)
	e := l.batches[key]
	if e != nil {
		if e.total != total {
			return Admission{}, &BatchError{Kind: BatchTotalMismatch, Op: op, Requested: total, Cap: e.total, Admitted: e.admitted}
		}
		if rec, seen := e.chunks[index]; seen {
			if rec.size != n {
				return Admission{}, &BatchError{Kind: BatchTotalMismatch, Op: op, Requested: n, Cap: rec.size, Admitted: e.admitted}
			}
			if rec.digest != digest {
				return Admission{}, &BatchError{Kind: BatchChunkMismatch, Op: op, Requested: n, Cap: rec.size, Admitted: e.admitted}
			}
			e.touched = now // a resend: already counted, already queued
			return Admission{Resent: true, OpID: rec.opID}, nil
		}
		if e.admitted+n > e.total || e.admitted+n > limit {
			return Admission{}, &BatchError{Kind: BatchExceeded, Op: op, Requested: e.admitted + n, Cap: min(e.total, limit), Admitted: e.admitted}
		}
		e.admitted += n
		e.chunks[index] = &chunkRecord{size: n, digest: digest}
		e.touched = now
		return Admission{}, nil
	}
	// A new batch is validated BEFORE it is recorded, so a refused request can
	// never occupy a slot.
	if total > limit {
		return Admission{}, &ExceededError{Op: op, Requested: total, Cap: limit}
	}
	if n <= 0 || n > total {
		return Admission{}, &BatchError{Kind: BatchExceeded, Op: op, Requested: n, Cap: total}
	}
	l.makeRoomLocked(owner)
	l.batches[key] = &batchEntry{owner: owner, total: total, admitted: n,
		chunks: map[int]*chunkRecord{index: {size: n, digest: digest}}, touched: now}
	return Admission{}, nil
}

// RecordOp remembers the op chunk `index` was queued as, so a resend of it is
// answered with that op instead of queueing the books again. A no-op for an
// entry that was evicted or released meanwhile.
func (l *BatchLedger) RecordOp(owner, id string, index int, opID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.batches[batchKey(owner, id)]; e != nil {
		if rec := e.chunks[index]; rec != nil {
			rec.opID = opID
		}
	}
}

// Release gives back chunk `index` of batch `id` after a FIRST-time chunk that
// was admitted but failed before anything was queued, so the client can retry
// it. Never call it for a resend: that chunk is already queued.
func (l *BatchLedger) Release(owner, id string, index int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	key := batchKey(owner, id)
	e := l.batches[key]
	if e == nil {
		return
	}
	if rec, ok := e.chunks[index]; ok {
		e.admitted -= rec.size
		delete(e.chunks, index)
	}
	if e.admitted <= 0 {
		delete(l.batches, key)
	}
}

// makeRoomLocked evicts so that one more batch of `owner` fits: the owner's
// least recently touched batch when they are at their limit, and the least
// recently touched overall when the ledger is full.
func (l *BatchLedger) makeRoomLocked(owner string) {
	mine := 0
	for _, e := range l.batches {
		if e.owner == owner {
			mine++
		}
	}
	if mine >= l.maxPerOwner {
		l.evictOldestLocked(func(e *batchEntry) bool { return e.owner == owner })
	}
	if len(l.batches) >= l.maxBatches {
		l.evictOldestLocked(func(*batchEntry) bool { return true })
	}
}

func (l *BatchLedger) evictOldestLocked(match func(*batchEntry) bool) {
	var oldestKey string
	var oldest *batchEntry
	for k, e := range l.batches {
		if match(e) && (oldest == nil || e.touched.Before(oldest.touched)) {
			oldestKey, oldest = k, e
		}
	}
	if oldest != nil {
		delete(l.batches, oldestKey)
	}
}

func (l *BatchLedger) sweepLocked(now time.Time) {
	for k, e := range l.batches {
		if now.Sub(e.touched) > l.ttl {
			delete(l.batches, k)
		}
	}
}
