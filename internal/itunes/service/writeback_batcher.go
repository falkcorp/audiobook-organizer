// file: internal/itunes/service/writeback_batcher.go
// version: 6.0.0
// guid: c3d4e5f6-a7b8-9c0d-1e2f-3a4b5c6d7e90
// last-edited: 2026-10-07
//
// Combined write-back batcher: handles location updates, track additions,
// and track removals in a single ITL read-modify-write cycle.
//
// SAFETY RAILS (v5.0.0):
//   - MaxRemovesPerFlush hard-caps the number of removes that can land
//     in a single flush. A flush exceeding the cap applies NONE of its
//     removes: they are moved to a durable held list that only the owner
//     can release (v6.0.0; before, the whole batch was dropped).
//   - DryRun mode (env ITUNES_WRITEBACK_DRYRUN=true) logs every flush
//     in detail but performs NO write to disk. Use it to diagnose
//     a suspicious enqueue pattern without risk.
//
// DURABILITY (v6.0.0, 2026-10-07): nothing leaves the queue except through a
// verified successful write.
//   - Every enqueued item is persisted to the store's raw KV
//     (itunes_writeback:q:*) at enqueue time and deleted only after the write
//     that carried it succeeded. The queue is reloaded on construction, so a
//     restart or a failed final drain loses nothing.
//   - A failed flush keeps the whole batch and retries with exponential
//     backoff (1 min doubling to 1 h). The old code dropped the batch after 3
//     consecutive failures; the counter was shared by all batches and never
//     reset without a success, so from the 4th failure on every batch was
//     dropped on its FIRST failure. Prod logged 662 dropped batches (4,293 book
//     updates, 1 remove) from 2026-09-23 to 2026-10-07, all rejected by the
//     location-form guard; see docs/plans/2026-10-07-itunes-writeback-drops.md.
//   - Removes are tombstoned in the external-id map only after the write lands.
//   - Status (pending, held, failures, last error, next retry) is exposed via
//     Status() and GET /api/v1/itunes/writeback/status.

package itunesservice

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
)

// MaxRemovesPerFlush is the hard cap on iTunes track removals applied
// in a single batcher flush. Any flush whose pendingRemoves set
// exceeds this count applies NONE of them: the removes move to the
// durable held list (HeldRemoves in Status) and wait for the owner to
// release them (ReleaseHeldRemoves), at most this many at a time.
//
// Rationale: bulk-remove paths historically wiped legitimate primary
// tracks when the DB was inconsistent (see the ~90 K-track shrink
// incident, May 2026). With targeted-only removes (one PID per
// explicit user delete), no legitimate flush should ever hit this
// cap — it's purely a circuit breaker.
const MaxRemovesPerFlush = 50

// Retry backoff after a failed flush: retryBackoffBase doubled per
// consecutive failure, capped at retryBackoffMax. The cap keeps a fixed fault
// retried at least hourly; the growth keeps a persistent rejection from
// re-encoding a 30 MB library every debounce tick.
const (
	retryBackoffBase = time.Minute
	retryBackoffMax  = time.Hour
)

// Durable queue keys in the store's raw KV. One key per item, so an enqueue is
// one small write and a success deletes exactly the items it carried.
const (
	wbQueuePrefix      = "itunes_writeback:q:"
	wbKeyBook          = wbQueuePrefix + "book:"
	wbKeyRemove        = wbQueuePrefix + "remove:"
	wbKeyAdd           = wbQueuePrefix + "add:"
	wbHeldRemovePrefix = "itunes_writeback:held:remove:"
	wbStatusKey        = "itunes_writeback:status"
)

// WriteBackBatcherConfig is the tiny config surface the batcher needs.
// Deliberately not using config.AppConfig directly — this makes the
// batcher movable to a package that doesn't import internal/config
// (see iTunes service extraction, spec 2026-04-18). Populated at
// construction and mutable via UpdateConfig for hot-reload support.
type WriteBackBatcherConfig struct {
	AutoWriteBack       bool
	ITLWriteBackEnabled bool
	LibraryWritePath    string
	// WriteBackDryRun logs every flush in detail but performs no write to
	// disk. Mirrors config.AppConfig.ITunes.WriteBackDryRun (env
	// ITUNES_WRITEBACK_DRYRUN); toggled via UpdateConfig like the other
	// fields here, so a restart applies a changed env value same as before.
	WriteBackDryRun bool
}

// WriteBackStore is what the ITL write-back batcher reads and marks.
//
// The first five were measured with an empty-interface compiler probe. The
// raw-KV four hold the durable queue (v6.0.0); they are already on
// database.Store via RawKVStore, so every production store satisfies this.
type WriteBackStore interface {
	GetBookByID(id string) (*database.Book, error)
	MarkITunesSynced(bookIDs []string) (int64, error)
	GetAuthorByID(id int) (*database.Author, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	MarkExternalIDRemoved(source, externalID string) error

	SetRaw(key string, value []byte) error
	GetRaw(key string) ([]byte, error)
	DeleteRaw(key string) error
	ScanPrefix(prefix string) ([]database.KVPair, error)
}

// WriteBackBatcher collects ITL operations and flushes them in a single batch
// after a debounce delay. Supports location updates, track additions, and
// track removals — all applied in one read-modify-write cycle.
//
// Adaptive debounce: the initial delay is 5s, but if new enqueues arrive
// within the window, the timer extends up to maxDelay (30s). This batches
// rapid-fire applies into a single ITL write instead of multiple. After a
// failed flush the next attempt also waits for retryAt (backoff).
type WriteBackBatcher struct {
	mu             sync.Mutex
	pendingBooks   map[string]bool      // book IDs for location updates
	pendingAdds    []itunes.ITLNewTrack // tracks to add
	pendingAddKeys []string             // store key per pendingAdds entry ("" = not persisted)
	pendingRemoves map[string]bool      // PIDs to remove (lowercase hex)
	timer          *time.Timer
	delay          time.Duration
	maxDelay       time.Duration
	firstEnqueue   time.Time // when the first enqueue in this batch happened
	// stopCh is closed by Stop and gates FLUSH SCHEDULING: a goroutine that
	// was queued to flush but has not started yet returns without doing any
	// work. b.stopped (under b.mu) gates ENQUEUEING and timer re-arming.
	// Both are set in the same Stop critical section. May be nil on a
	// batcher built as a struct literal (tests) — every read nil-guards.
	stopCh  chan struct{}
	stopped bool

	// wg tracks every goroutine the batcher starts: resetTimer's
	// past-maxDelay `go b.flush()` and the debounce/retry timer's callback.
	// Stop joins it so shutdown never returns with a flush still inside the
	// ITL writer. Every Add happens under b.mu and after the b.stopped check,
	// so no Add can race Wait.
	wg sync.WaitGroup

	// flushMu serializes the whole flush read-modify-write cycle so only one
	// goroutine is ever inside SafeWriteITL for this batcher. It is
	// deliberately separate from mu: mu is released before the disk I/O
	// precisely so Enqueue* callers never block behind an ITL re-encode.
	// Lock order is always flushMu → mu, never the reverse.
	flushMu sync.Mutex

	// Config fields — populated at construction, mutable via UpdateConfig.
	// Reads use cfgMu (separate from mu so config-reload doesn't block the
	// main pending-ops critical section).
	cfgMu               sync.RWMutex
	autoWriteBack       bool
	itlWriteBackEnabled bool
	libraryWritePath    string
	writeBackDryRun     bool

	// store is the narrow database surface used by the flush goroutine and
	// the durable queue. May be nil (pre-wiring path in older test
	// fixtures): the queue is then memory-only and a flush cannot build
	// updates, so it fails and keeps the batch.
	store WriteBackStore

	// libraryNotInUse is an optional pre-flight check wired to the
	// iTunes sync-service running-state signal. When non-nil, flush()
	// calls it before writing; a non-nil error aborts the write and
	// the batch is re-queued on the next timer tick rather than lost.
	// Set via SetLibraryNotInUse. Safe to call after construction.
	libraryNotInUse func() error

	// flushFailures counts CONSECUTIVE failed flush attempts (parse or
	// write errors). It drives the retry backoff and resets on success. It
	// no longer drops anything: the batch always stays queued.
	flushFailures int
	// retryAt is the earliest time the next flush may run after a failure;
	// zero when there is no backoff in force. resetTimer honors it, so an
	// enqueue during backoff cannot trigger an early retry.
	retryAt       time.Time
	lastError     string
	lastFailureAt time.Time
	lastSuccessAt time.Time

	// held is the durable held-remove list (PID → when it was held): removes
	// refused by MaxRemovesPerFlush, waiting for the owner.
	held map[string]time.Time

	// addSeq disambiguates add keys enqueued in the same nanosecond.
	addSeq uint64

	// loadOnce guards the one-time reload of the durable queue in Start.
	loadOnce sync.Once

	// retryBase / retryMax override the backoff constants (tests). Zero means
	// the package defaults.
	retryBase time.Duration
	retryMax  time.Duration
}

// NewWriteBackBatcher creates a batcher with the given debounce delay. It
// does not touch the store: Start reloads the durable queue (the container
// calls Start during server startup).
func NewWriteBackBatcher(delay time.Duration, cfg WriteBackBatcherConfig, store WriteBackStore) *WriteBackBatcher {
	b := &WriteBackBatcher{
		pendingBooks:        make(map[string]bool),
		pendingRemoves:      make(map[string]bool),
		held:                make(map[string]time.Time),
		delay:               delay,
		maxDelay:            30 * time.Second,
		stopCh:              make(chan struct{}),
		autoWriteBack:       cfg.AutoWriteBack,
		itlWriteBackEnabled: cfg.ITLWriteBackEnabled,
		libraryWritePath:    cfg.LibraryWritePath,
		writeBackDryRun:     cfg.WriteBackDryRun,
		store:               store,
	}
	return b
}

// storeOrNil returns b.store (nil when no store is wired: the queue is then
// memory-only).
func (b *WriteBackBatcher) storeOrNil() WriteBackStore {
	if b.store == nil {
		return nil
	}
	return b.store
}

// loadQueue restores the durable queue, the held list and the failure status
// from the store. Called once, from Start. Items enqueued before Start are
// merged with what it loads, never replaced. A restart gets a prompt first attempt (retryAt is not
// restored: it is likely a deploy with new code or config), but the failure
// count carries on so a still-broken write resumes its backoff.
func (b *WriteBackBatcher) loadQueue() {
	store := b.storeOrNil()
	if store == nil {
		return
	}
	pairs, err := store.ScanPrefix(wbQueuePrefix)
	if err != nil {
		slog.Error("iTunes write-back could not reload its durable queue; queued items stay in the store and load on the next start", "err", err)
		return
	}
	held, herr := store.ScanPrefix(wbHeldRemovePrefix)
	if herr != nil {
		slog.Error("iTunes write-back could not reload held removes", "err", herr)
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.held == nil {
		b.held = make(map[string]time.Time)
	}
	knownAdd := make(map[string]bool, len(b.pendingAddKeys))
	for _, k := range b.pendingAddKeys {
		knownAdd[k] = true
	}
	var books, adds, removes int
	for _, kv := range pairs {
		switch {
		case strings.HasPrefix(kv.Key, wbKeyBook):
			b.pendingBooks[strings.TrimPrefix(kv.Key, wbKeyBook)] = true
			books++
		case strings.HasPrefix(kv.Key, wbKeyRemove):
			b.pendingRemoves[strings.TrimPrefix(kv.Key, wbKeyRemove)] = true
			removes++
		case strings.HasPrefix(kv.Key, wbKeyAdd):
			if knownAdd[kv.Key] {
				continue // enqueued by this process before Start
			}
			var tr itunes.ITLNewTrack
			if err := json.Unmarshal(kv.Value, &tr); err != nil {
				// Left in the store for inspection; never silently deleted.
				slog.Error("iTunes write-back queued add is unreadable; left in the store", "key", kv.Key, "err", err)
				continue
			}
			b.pendingAdds = append(b.pendingAdds, tr)
			b.pendingAddKeys = append(b.pendingAddKeys, kv.Key)
			adds++
		}
	}
	for _, kv := range held {
		var rec heldRecord
		_ = json.Unmarshal(kv.Value, &rec)
		b.held[strings.TrimPrefix(kv.Key, wbHeldRemovePrefix)] = rec.HeldAt
	}
	if raw, gerr := store.GetRaw(wbStatusKey); gerr == nil && len(raw) > 0 {
		var st persistedStatus
		if json.Unmarshal(raw, &st) == nil {
			b.flushFailures = st.ConsecutiveFailures
			b.lastError = st.LastError
			b.lastFailureAt = st.LastFailureAt
			b.lastSuccessAt = st.LastSuccessAt
		}
	}
	if books+adds+removes > 0 || len(b.held) > 0 {
		slog.Warn("iTunes write-back restored its durable queue from the store",
			"books", books, "adds", adds, "removes", removes, "held_removes", len(b.held),
			"consecutive_failures", b.flushFailures, "last_error", b.lastError)
	}
	if b.hasPending() {
		b.resetTimer()
	}
}

// heldRecord is the stored value of a held remove.
type heldRecord struct {
	HeldAt time.Time `json:"held_at"`
	Reason string    `json:"reason"`
}

// persistedStatus is the stored failure/success status, so the owner sees it
// across restarts.
type persistedStatus struct {
	ConsecutiveFailures int       `json:"consecutive_failures"`
	LastError           string    `json:"last_error,omitempty"`
	LastFailureAt       time.Time `json:"last_failure_at,omitzero"`
	LastSuccessAt       time.Time `json:"last_success_at,omitzero"`
	NextRetryAt         time.Time `json:"next_retry_at,omitzero"`
}

// persistLocked writes one queue key. A failed write is logged at ERROR: the
// item is still queued in memory and will be written if the flush succeeds,
// but it would not survive a restart. Callers hold b.mu.
func (b *WriteBackBatcher) persistLocked(key string, value []byte) {
	store := b.storeOrNil()
	if store == nil || key == "" {
		return
	}
	if err := store.SetRaw(key, value); err != nil {
		slog.Error("iTunes write-back could not persist a queued item; it is queued in memory only until the next successful flush", "key", key, "err", err)
	}
}

// unpersistLocked deletes one queue key. Callers hold b.mu.
func (b *WriteBackBatcher) unpersistLocked(key string) {
	store := b.storeOrNil()
	if store == nil || key == "" {
		return
	}
	if err := store.DeleteRaw(key); err != nil {
		slog.Warn("iTunes write-back could not delete a flushed queue key; it will be re-applied (diffed, so harmless) on the next start", "key", key, "err", err)
	}
}

// persistStatusLocked stores the current failure/success status. Callers hold b.mu.
func (b *WriteBackBatcher) persistStatusLocked() {
	store := b.storeOrNil()
	if store == nil {
		return
	}
	data, err := json.Marshal(persistedStatus{
		ConsecutiveFailures: b.flushFailures,
		LastError:           b.lastError,
		LastFailureAt:       b.lastFailureAt,
		LastSuccessAt:       b.lastSuccessAt,
		NextRetryAt:         b.retryAt,
	})
	if err != nil {
		return
	}
	if err := store.SetRaw(wbStatusKey, data); err != nil {
		slog.Warn("iTunes write-back could not persist its status", "err", err)
	}
}

// UpdateConfig is safe to call while the flush goroutine is running.
// Use from the server's config-reload path when/if one is wired up. A queue
// kept while write-back was off is scheduled again here.
func (b *WriteBackBatcher) UpdateConfig(cfg WriteBackBatcherConfig) {
	b.cfgMu.Lock()
	b.autoWriteBack = cfg.AutoWriteBack
	b.itlWriteBackEnabled = cfg.ITLWriteBackEnabled
	b.libraryWritePath = cfg.LibraryWritePath
	b.writeBackDryRun = cfg.WriteBackDryRun
	b.cfgMu.Unlock()

	if cfg.ITLWriteBackEnabled && cfg.LibraryWritePath != "" && !cfg.WriteBackDryRun {
		b.mu.Lock()
		if b.hasPending() {
			b.resetTimer()
		}
		b.mu.Unlock()
	}
}

// SetLibraryNotInUse wires the iTunes-running signal into the batcher.
// The check function is called immediately before each ITL write; if it
// returns a non-nil error the write is skipped this cycle and the pending
// state is preserved for the next timer tick. Safe to call after Start.
func (b *WriteBackBatcher) SetLibraryNotInUse(check func() error) {
	b.mu.Lock()
	b.libraryNotInUse = check
	b.mu.Unlock()
}

// autoWriteBackEnabled returns the current AutoWriteBack value under RLock.
func (b *WriteBackBatcher) autoWriteBackEnabled() bool {
	b.cfgMu.RLock()
	defer b.cfgMu.RUnlock()
	return b.autoWriteBack
}

// flushEnabled returns the current ITLWriteBackEnabled + LibraryWritePath
// pair under RLock, for use at flush time.
func (b *WriteBackBatcher) flushEnabled() (bool, string) {
	b.cfgMu.RLock()
	defer b.cfgMu.RUnlock()
	return b.itlWriteBackEnabled, b.libraryWritePath
}

// dryRunEnabled returns the current WriteBackDryRun value under RLock.
func (b *WriteBackBatcher) dryRunEnabled() bool {
	b.cfgMu.RLock()
	defer b.cfgMu.RUnlock()
	return b.writeBackDryRun
}

// Enqueue adds a book ID to the pending location-update batch.
func (b *WriteBackBatcher) Enqueue(bookID string) {
	if !b.autoWriteBackEnabled() {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopped {
		return
	}
	if !b.pendingBooks[bookID] {
		b.persistLocked(wbKeyBook+bookID, []byte("1"))
	}
	b.pendingBooks[bookID] = true
	b.resetTimer()
}

// EnqueueAdd queues a new track for insertion into the ITL.
func (b *WriteBackBatcher) EnqueueAdd(track itunes.ITLNewTrack) {
	if !b.autoWriteBackEnabled() {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopped {
		return
	}
	b.addSeq++
	key := ""
	if b.storeOrNil() != nil {
		if data, err := json.Marshal(track); err == nil {
			key = wbKeyAdd + strconv.FormatInt(time.Now().UnixNano(), 10) + "-" + strconv.FormatUint(b.addSeq, 10)
			b.persistLocked(key, data)
		}
	}
	b.pendingAdds = append(b.pendingAdds, track)
	b.pendingAddKeys = append(b.pendingAddKeys, key)
	b.resetTimer()
}

// EnqueueRemove queues a track PID for removal from the ITL.
//
// It does NOT touch the external-id map. Before v6.0.0 it marked the PID
// removed here, at enqueue time, so a remove that was later dropped, refused
// by the cap, or only logged in dry-run left the DB saying "removed" while the
// track stayed in iTunes. The tombstone is now written after the flush that
// removes the track succeeds (see markRemovesApplied).
//
// A PID on the held list stays held: the owner releases held removes.
func (b *WriteBackBatcher) EnqueueRemove(pid string) {
	if !b.autoWriteBackEnabled() {
		return
	}
	key := strings.ToLower(pid)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopped {
		return
	}
	if _, isHeld := b.held[key]; isHeld {
		slog.Warn("iTunes write-back remove is on the held list; release it to apply", "pid", key)
		return
	}
	if !b.pendingRemoves[key] {
		b.persistLocked(wbKeyRemove+key, []byte("1"))
	}
	b.pendingRemoves[key] = true
	b.resetTimer()
}

// stopTimerLocked cancels the pending debounce timer, if any, and releases the
// b.wg slot resetTimer reserved for its callback when the cancel wins the
// race. time.Timer.Stop reports true only when the callback had not yet fired;
// a false return means timerFlush is already running and will call Done
// itself. Callers must hold b.mu.
func (b *WriteBackBatcher) stopTimerLocked() {
	if b.timer == nil {
		return
	}
	if b.timer.Stop() {
		b.wg.Done()
	}
	b.timer = nil
}

// timerFlush is the debounce timer's callback. It owns the b.wg slot that
// resetTimer reserved when it armed the timer, and returns without work if
// Stop closed stopCh while the timer was in flight.
func (b *WriteBackBatcher) timerFlush() {
	defer b.wg.Done()
	select {
	case <-b.stopCh:
		return
	default:
	}
	b.flush()
}

// resetTimer (re)arms the debounce window. Callers must hold b.mu.
//
// Every goroutine scheduled here is registered on b.wg first so Stop can join
// it. The b.stopped guard is load-bearing: the requeue paths call resetTimer
// from inside a flush, including Stop's own final drain, and without the
// guard that drain would arm fresh work after Stop had already joined — both a
// goroutine that outlives shutdown and a WaitGroup Add after Wait.
//
// While a retry backoff is in force (retryAt in the future) the timer is armed
// for retryAt instead: an enqueue during backoff must not trigger an early
// retry of a write that is failing.
func (b *WriteBackBatcher) resetTimer() {
	b.stopTimerLocked()
	if b.stopped {
		return
	}
	if wait := time.Until(b.retryAt); !b.retryAt.IsZero() && wait > 0 {
		b.wg.Add(1)
		b.timer = time.AfterFunc(wait, b.timerFlush)
		return
	}
	// Track when the first enqueue in this batch happened
	if b.firstEnqueue.IsZero() {
		b.firstEnqueue = time.Now()
	}
	// If we've been accumulating for longer than maxDelay, flush now
	elapsed := time.Since(b.firstEnqueue)
	if elapsed >= b.maxDelay {
		b.wg.Add(1)
		go func() {
			defer b.wg.Done()
			select {
			case <-b.stopCh:
				return
			default:
			}
			b.flush()
		}()
		return
	}
	// Otherwise, extend the timer (but don't exceed maxDelay from first enqueue)
	remaining := b.maxDelay - elapsed
	delay := min(b.delay, remaining)
	b.wg.Add(1)
	b.timer = time.AfterFunc(delay, b.timerFlush)
}

// HasPendingBook reports whether bookID is currently queued for a
// location update. Intended for test assertions in other packages that
// can no longer reach b.pendingBooks directly after the M1 step 2 move.
// Safe to call concurrently with Enqueue.
func (b *WriteBackBatcher) HasPendingBook(bookID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.pendingBooks[bookID]
}

func (b *WriteBackBatcher) hasPending() bool {
	return len(b.pendingBooks) > 0 || len(b.pendingAdds) > 0 || len(b.pendingRemoves) > 0
}

// flushBatch is one snapshot of the pending queue taken by a flush.
type flushBatch struct {
	bookIDs []string
	adds    []itunes.ITLNewTrack
	addKeys []string
	removes map[string]bool
}

func (fb *flushBatch) empty() bool {
	return len(fb.bookIDs) == 0 && len(fb.adds) == 0 && len(fb.removes) == 0
}

// takePendingLocked snapshots and clears the in-memory queue. The store keys
// stay: they are deleted only when the write carrying them succeeds. Callers
// hold b.mu.
func (b *WriteBackBatcher) takePendingLocked() flushBatch {
	fb := flushBatch{
		bookIDs: make([]string, 0, len(b.pendingBooks)),
		adds:    b.pendingAdds,
		addKeys: b.pendingAddKeys,
		removes: make(map[string]bool, len(b.pendingRemoves)),
	}
	for id := range b.pendingBooks {
		fb.bookIDs = append(fb.bookIDs, id)
	}
	for pid := range b.pendingRemoves {
		fb.removes[pid] = true
	}
	b.pendingBooks = make(map[string]bool)
	b.pendingAdds = nil
	b.pendingAddKeys = nil
	b.pendingRemoves = make(map[string]bool)
	b.firstEnqueue = time.Time{} // reset for next batch
	return fb
}

// requeueLocked merges a batch back into the in-memory queue. Its store keys
// were never deleted, so the batch stays durable. Callers hold b.mu.
func (b *WriteBackBatcher) requeueLocked(fb flushBatch) {
	for _, id := range fb.bookIDs {
		b.pendingBooks[id] = true
	}
	b.pendingAdds = append(b.pendingAdds, fb.adds...)
	keys := fb.addKeys
	if len(keys) != len(fb.adds) {
		// Struct-literal batches (tests) may carry adds without keys.
		keys = make([]string, len(fb.adds))
		copy(keys, fb.addKeys)
	}
	b.pendingAddKeys = append(b.pendingAddKeys, keys...)
	for pid := range fb.removes {
		b.pendingRemoves[pid] = true
	}
	if b.firstEnqueue.IsZero() && !fb.empty() {
		b.firstEnqueue = time.Now()
	}
}

// reEnqueue restores a batch and re-arms the timer (the library-in-use
// deferral: not a failure, retried on the next debounce tick).
func (b *WriteBackBatcher) reEnqueue(fb flushBatch) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.requeueLocked(fb)
	b.resetTimer()
}

// keepUnscheduled restores a batch WITHOUT arming a timer: write-back is off
// or in dry-run, so retrying on a timer would only spin. The batch stays
// queued (and durable) until write-back is enabled (UpdateConfig) or the next
// enqueue triggers a flush.
func (b *WriteBackBatcher) keepUnscheduled(fb flushBatch) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.requeueLocked(fb)
}

// retryDelay is the backoff before attempt n+1 after n consecutive failures.
func (b *WriteBackBatcher) retryDelay(n int) time.Duration {
	base, maxD := b.retryBase, b.retryMax
	if base <= 0 {
		base = retryBackoffBase
	}
	if maxD <= 0 {
		maxD = retryBackoffMax
	}
	d := base
	for i := 1; i < n && d < maxD; i++ {
		d *= 2
	}
	return min(d, maxD)
}

// failBatch keeps a failed batch and schedules its retry with backoff. It
// never drops: the batch goes back into the queue (its store keys were never
// deleted), the failure is logged at ERROR with the next retry time, and the
// status is persisted for GET /itunes/writeback/status.
func (b *WriteBackBatcher) failBatch(fb flushBatch, err error) {
	now := time.Now()
	b.mu.Lock()
	b.flushFailures++
	n := b.flushFailures
	delay := b.retryDelay(n)
	b.retryAt = now.Add(delay)
	b.lastError = err.Error()
	b.lastFailureAt = now
	b.requeueLocked(fb)
	pendingBooks, pendingAdds, pendingRemoves := len(b.pendingBooks), len(b.pendingAdds), len(b.pendingRemoves)
	b.persistStatusLocked()
	b.resetTimer()
	b.mu.Unlock()

	slog.Error("iTunes write-back FAILED; batch kept and will be retried (nothing dropped)",
		"err", err, "consecutive_failures", n, "retry_in", delay.String(), "next_retry_at", b.retryAtString(),
		"batch_updates", len(fb.bookIDs), "batch_adds", len(fb.adds), "batch_removes", len(fb.removes),
		"queued_updates", pendingBooks, "queued_adds", pendingAdds, "queued_removes", pendingRemoves)
}

func (b *WriteBackBatcher) retryAtString() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.retryAt.IsZero() {
		return ""
	}
	return b.retryAt.Format(time.RFC3339)
}

// completeBatch removes a batch's items from the durable queue after its write
// landed (or after the diff showed nothing to write). An item re-enqueued in
// memory while the flush ran keeps its key. wrote=true also clears the
// failure status: only a real write proves the write path works.
func (b *WriteBackBatcher) completeBatch(fb flushBatch, wrote bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, id := range fb.bookIDs {
		if !b.pendingBooks[id] {
			b.unpersistLocked(wbKeyBook + id)
		}
	}
	for pid := range fb.removes {
		if !b.pendingRemoves[pid] {
			b.unpersistLocked(wbKeyRemove + pid)
		}
	}
	for _, k := range fb.addKeys {
		b.unpersistLocked(k)
	}
	if wrote {
		b.flushFailures = 0
		b.retryAt = time.Time{}
		b.lastError = ""
		b.lastSuccessAt = time.Now()
		b.persistStatusLocked()
	}
}

// holdRemoves moves an over-cap set of removes to the durable held list. None
// of them is applied; the owner releases them with ReleaseHeldRemoves.
func (b *WriteBackBatcher) holdRemoves(removes map[string]bool) {
	now := time.Now()
	rec, _ := json.Marshal(heldRecord{HeldAt: now, Reason: fmt.Sprintf("flush held %d removes, over MaxRemovesPerFlush=%d", len(removes), MaxRemovesPerFlush)})
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.held == nil {
		b.held = make(map[string]time.Time) // struct-literal batchers (tests)
	}
	for pid := range removes {
		b.held[pid] = now
		b.persistLocked(wbHeldRemovePrefix+pid, rec)
		if !b.pendingRemoves[pid] {
			b.unpersistLocked(wbKeyRemove + pid)
		}
	}
}

// ReleaseHeldRemoves moves up to limit held removes (at most
// MaxRemovesPerFlush, oldest first) back into the queue, so the next flush
// applies them. It is the owner's explicit action on the circuit breaker.
// Returns how many were released and how many stay held.
func (b *WriteBackBatcher) ReleaseHeldRemoves(limit int) (released, stillHeld int) {
	if limit <= 0 || limit > MaxRemovesPerFlush {
		limit = MaxRemovesPerFlush
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	pids := make([]string, 0, len(b.held))
	for pid := range b.held {
		pids = append(pids, pid)
	}
	sort.Slice(pids, func(i, j int) bool {
		if !b.held[pids[i]].Equal(b.held[pids[j]]) {
			return b.held[pids[i]].Before(b.held[pids[j]])
		}
		return pids[i] < pids[j]
	})
	room := limit - len(b.pendingRemoves)
	for _, pid := range pids {
		if released >= room {
			break
		}
		delete(b.held, pid)
		b.unpersistLocked(wbHeldRemovePrefix + pid)
		b.persistLocked(wbKeyRemove+pid, []byte("1"))
		b.pendingRemoves[pid] = true
		released++
	}
	if released > 0 {
		slog.Warn("iTunes write-back released held removes into the queue", "released", released, "still_held", len(b.held))
		b.resetTimer()
	}
	return released, len(b.held)
}

// WriteBackQueueStatus is the owner-visible state of the write-back queue.
type WriteBackQueueStatus struct {
	Enabled             bool       `json:"enabled"`
	DryRun              bool       `json:"dry_run"`
	LibraryWritePath    string     `json:"library_write_path"`
	Durable             bool       `json:"durable"`
	PendingUpdates      int        `json:"pending_updates"`
	PendingAdds         int        `json:"pending_adds"`
	PendingRemoves      int        `json:"pending_removes"`
	HeldRemoves         int        `json:"held_removes"`
	HeldRemovePIDs      []string   `json:"held_remove_pids,omitempty"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	LastError           string     `json:"last_error,omitempty"`
	LastFailureAt       *time.Time `json:"last_failure_at,omitempty"`
	LastSuccessAt       *time.Time `json:"last_success_at,omitempty"`
	NextRetryAt         *time.Time `json:"next_retry_at,omitempty"`
	// Healthy is false while the last flush failed or removes are held.
	Healthy bool `json:"healthy"`
}

// maxHeldPIDsInStatus bounds the held-PID list in a status response.
const maxHeldPIDsInStatus = 200

// Status returns the current queue state. Safe for concurrent use.
func (b *WriteBackBatcher) Status() WriteBackQueueStatus {
	if b == nil {
		return WriteBackQueueStatus{Healthy: true}
	}
	enabled, path := b.flushEnabled()
	st := WriteBackQueueStatus{
		Enabled:          enabled && path != "",
		DryRun:           b.dryRunEnabled(),
		LibraryWritePath: path,
		Durable:          b.storeOrNil() != nil,
	}
	tp := func(t time.Time) *time.Time {
		if t.IsZero() {
			return nil
		}
		return &t
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	st.PendingUpdates = len(b.pendingBooks)
	st.PendingAdds = len(b.pendingAdds)
	st.PendingRemoves = len(b.pendingRemoves)
	st.HeldRemoves = len(b.held)
	for pid := range b.held {
		st.HeldRemovePIDs = append(st.HeldRemovePIDs, pid)
	}
	sort.Strings(st.HeldRemovePIDs)
	if len(st.HeldRemovePIDs) > maxHeldPIDsInStatus {
		st.HeldRemovePIDs = st.HeldRemovePIDs[:maxHeldPIDsInStatus]
	}
	st.ConsecutiveFailures = b.flushFailures
	st.LastError = b.lastError
	st.LastFailureAt = tp(b.lastFailureAt)
	st.LastSuccessAt = tp(b.lastSuccessAt)
	st.NextRetryAt = tp(b.retryAt)
	st.Healthy = b.flushFailures == 0 && len(b.held) == 0
	return st
}

// flush is the entry point for every SCHEDULED flush (debounce timer, the
// past-maxDelay fast path). It refuses to run once Stop has been called: Stop
// performs the single final drain itself, after joining the workers, so
// shutdown can never interleave two write cycles.
func (b *WriteBackBatcher) flush() {
	b.mu.Lock()
	stopped := b.stopped
	b.mu.Unlock()
	if stopped {
		return
	}
	b.drainFlush()
}

// drainFlush writes all pending operations to the iTunes ITL binary in one
// pass. Call it directly only from Stop's final drain — every other caller
// goes through flush, which honors b.stopped.
//
// Every exit keeps or completes the batch; none drops it.
func (b *WriteBackBatcher) drainFlush() {
	// SINGLE WRITER: the entire read-modify-write cycle (ParseITL → diff →
	// SafeWriteITL, which backs up, writes a shared `.tmp` and renames it
	// over the live library) is serialized on flushMu. Before this, b.mu was
	// released after the snapshot and two flushes could sit inside
	// SafeWriteITL at once, racing through the same `.tmp`.
	//
	// flushMu rather than b.mu on purpose: b.mu is released before the disk
	// I/O so Enqueue* callers never block behind an ITL re-encode, and
	// holding it across the write would transfer that stall to every
	// enqueuer. Lock order is always flushMu → b.mu, never the reverse.
	//
	// Scope of the guarantee: flushMu is per-batcher, so it serializes THIS
	// batcher's flushes. Two batcher instances aimed at the same ITL path,
	// or the other writers of that path, are outside it.
	b.flushMu.Lock()
	defer b.flushMu.Unlock()

	b.mu.Lock()
	if !b.hasPending() {
		b.mu.Unlock()
		return
	}
	fb := b.takePendingLocked()
	b.mu.Unlock()

	// SAFETY RAIL: cap removes per flush. Over the cap, apply NONE of them
	// (a partial subset is how we got in trouble before): they move to the
	// durable held list for the owner. The batch's adds and updates go on.
	if len(fb.removes) > MaxRemovesPerFlush {
		slog.Error("iTunes write-back HOLDING removes over the safety cap; none applied. Investigate what enqueued them, then release via POST /api/v1/itunes/writeback/held/release.",
			"removes_count", len(fb.removes), "maxRemovesPerFlush", MaxRemovesPerFlush,
			"other_updates_continue", len(fb.bookIDs), "other_adds_continue", len(fb.adds))
		b.holdRemoves(fb.removes)
		fb.removes = map[string]bool{}
		if fb.empty() {
			return
		}
	}

	dryRun := b.dryRunEnabled()

	store := b.storeOrNil()
	if store == nil {
		b.failBatch(fb, errors.New("no database store wired"))
		return
	}

	itlEnabled, writePath := b.flushEnabled()
	if !itlEnabled || writePath == "" {
		slog.Warn("iTunes write-back ITL write-back not configured; batch kept queued until it is",
			"queued_updates", len(fb.bookIDs), "queued_adds", len(fb.adds), "queued_removes", len(fb.removes))
		b.keepUnscheduled(fb)
		return
	}

	// Check whether iTunes is currently using the library file. When
	// the check function is wired, a non-nil error means "in use":
	// skip this cycle so we don't race a concurrent iTunes write.
	b.mu.Lock()
	notInUse := b.libraryNotInUse
	b.mu.Unlock()
	if notInUse != nil {
		if err := notInUse(); err != nil {
			slog.Warn("iTunes write-back deferred: library in use", "reason", err)
			b.reEnqueue(fb)
			return
		}
	}

	bookIDs, adds, removes := fb.bookIDs, fb.adds, fb.removes

	// Parse the current ITL library to enable diff-before-write.
	// We build a PID → track index once per flush, then compare each
	// desired update against the current values. This prevents writing
	// ~90K mhoh blocks every sync when only a handful of tracks have
	// actually changed (HIGH-3 / T008 fix).
	//
	// Parse failure DEFERS the flush (SPEC 3 §4): an unreadable library
	// means we cannot know what we would be writing over. It is a failure
	// like any other: the batch is kept and retried with backoff.
	var tracksByPID map[string]*itunes.ITLTrack
	if lib, err := parseITLFn(writePath); err != nil {
		b.failBatch(fb, fmt.Errorf("library unparseable: %w", err))
		return
	} else {
		tracksByPID = make(map[string]*itunes.ITLTrack, len(lib.Tracks))
		for i := range lib.Tracks {
			t := &lib.Tracks[i]
			pid := strings.ToLower(hex.EncodeToString(t.PersistentID[:]))
			tracksByPID[pid] = t
		}
	}

	// Build location and metadata updates from book IDs
	var locationUpdates []itunes.ITLLocationUpdate
	var metadataUpdates []itunes.ITLMetadataUpdate
	var skippedMetadata, changedMetadata int
	// H4 (2026-07 error-correction sweep): GetBookByID error and "book
	// deleted between enqueue and flush" (nil, no error) used to be treated
	// identically as a silent skip. Track them separately — a store error is
	// worth investigating, a nil book is expected/benign churn.
	var bookLookupErrs, bookNilSkips int
	var lookupErrIDs []string
	for _, id := range bookIDs {
		book, err := store.GetBookByID(id)
		if err != nil {
			bookLookupErrs++
			lookupErrIDs = append(lookupErrIDs, id)
			continue
		}
		if book == nil {
			bookNilSkips++
			continue
		}
		// Only primary versions are written to iTunes. A non-primary
		// version of a book that was somehow enqueued (e.g. via a
		// metadata edit on the alternate format) must NOT push to
		// iTunes — its PID, if any, was created in error and the
		// orphan-cleanup pass will remove it.
		if book.IsPrimaryVersion != nil && !*book.IsPrimaryVersion {
			continue
		}

		// Get author name for metadata
		authorName := ""
		if book.AuthorID != nil {
			if author, err := store.GetAuthorByID(*book.AuthorID); err == nil && author != nil {
				authorName = author.Name
			}
		}
		// Narrator ends up in the Composer field for audiobooks —
		// Apple Music shows it there and most audiobook workflows
		// (scanners, converters, players) key on that mapping.
		narrator := ""
		if book.Narrator != nil {
			narrator = *book.Narrator
		}
		// Genre: prefer the book's own genre when set, fall back to
		// "Audiobook" so iTunes classifies correctly. Previously
		// every write hardcoded "Audiobook" even when the user had
		// set a more specific value.
		genre := "Audiobook"
		if book.Genre != nil && *book.Genre != "" {
			genre = *book.Genre
		}

		files, _ := store.GetBookFiles(id)
		if len(files) > 0 {
			for _, f := range files {
				if f.ITunesPersistentID == "" {
					continue
				}
				// Diff-before-write (T008 / HIGH-3): only enqueue an
				// ITLMetadataUpdate (and/or location update) when at
				// least one field differs from the current library value.
				// ITLTrack.Composer is not stored in the parsed struct
				// (0x0C not read), so it is always included in the update
				// when other fields change.
				pidKey := strings.ToLower(f.ITunesPersistentID)
				desiredLoc := ""
				if f.ITunesPath != "" {
					// SPEC §1b / TASK-006: normalize f.ITunesPath (which has
					// historically held BOTH native paths and file:// URLs)
					// into the canonical WinPath. Unmappable values are NOT
					// written — per-item WARN + metric, never a raw value into
					// 0x0D (the CRIT-2 corruption).
					if winPath, ok := normalizeITunesLocation(f.ITunesPersistentID, f.ITunesPath); ok {
						desiredLoc = winPath
					}
				}

				if cur, ok := tracksByPID[pidKey]; ok {
					// We have current library state: compare field by field.
					// Location update is suppressed when the desired path
					// matches the current 0x0D value (or is unmappable/empty).
					locationChanged := desiredLoc != "" && cur.Location != desiredLoc
					metadataChanged := cur.Name != f.Title ||
						cur.Album != book.Title ||
						cur.Artist != authorName ||
						cur.Genre != genre

					if !locationChanged && !metadataChanged {
						skippedMetadata++
						continue
					}
					if locationChanged {
						locationUpdates = append(locationUpdates, itunes.ITLLocationUpdate{
							PersistentID: f.ITunesPersistentID,
							NewLocation:  desiredLoc,
						})
					}
					if metadataChanged {
						changedMetadata++
						metadataUpdates = append(metadataUpdates, itunes.ITLMetadataUpdate{
							PersistentID: f.ITunesPersistentID,
							Name:         f.Title,
							Album:        book.Title,
							Artist:       authorName,
							Composer:     narrator,
							Genre:        genre,
						})
					}
				} else {
					// No current library state for this PID (track is new or
					// library parse failed) — emit both unconditionally.
					if desiredLoc != "" {
						locationUpdates = append(locationUpdates, itunes.ITLLocationUpdate{
							PersistentID: f.ITunesPersistentID,
							NewLocation:  desiredLoc,
						})
					}
					changedMetadata++
					metadataUpdates = append(metadataUpdates, itunes.ITLMetadataUpdate{
						PersistentID: f.ITunesPersistentID,
						Name:         f.Title,
						Album:        book.Title,
						Artist:       authorName,
						Composer:     narrator,
						Genre:        genre,
					})
				}
			}
		} else if book.ITunesPersistentID != nil && *book.ITunesPersistentID != "" {
			pidKey := strings.ToLower(*book.ITunesPersistentID)
			if cur, ok := tracksByPID[pidKey]; ok {
				if cur.Name == book.Title &&
					cur.Album == book.Title &&
					cur.Artist == authorName &&
					cur.Genre == genre {
					skippedMetadata++
					continue
				}
			}
			changedMetadata++
			metadataUpdates = append(metadataUpdates, itunes.ITLMetadataUpdate{
				PersistentID: *book.ITunesPersistentID,
				Name:         book.Title,
				Album:        book.Title,
				Artist:       authorName,
				Composer:     narrator,
				Genre:        genre,
			})
		}
	}

	// Emitted before the ops.IsEmpty() early return below so lookup errors
	// are never lost even when every enqueued book ended up producing no
	// update (e.g. all bookIDs errored or were skipped).
	if bookLookupErrs > 0 || bookNilSkips > 0 {
		slog.Warn("iTunes write-back: book lookups skipped",
			"lookup_errors", bookLookupErrs,
			"nil_book_skips", bookNilSkips,
			"total_bookIDs", len(bookIDs),
		)
	}

	// Books whose lookup FAILED (a store error, not a deleted book) were not
	// written; they are split out and kept queued with backoff instead of
	// being completed with the rest.
	var retryBooks flushBatch
	if len(lookupErrIDs) > 0 {
		retryBooks.bookIDs = lookupErrIDs
		failed := make(map[string]bool, len(lookupErrIDs))
		for _, id := range lookupErrIDs {
			failed[id] = true
		}
		kept := fb.bookIDs[:0:0]
		for _, id := range fb.bookIDs {
			if !failed[id] {
				kept = append(kept, id)
			}
		}
		fb.bookIDs = kept
	}
	defer func() {
		if !retryBooks.empty() {
			b.failBatch(retryBooks, fmt.Errorf("book lookup failed for %d book(s)", len(retryBooks.bookIDs)))
		}
	}()

	ops := itunes.ITLOperationSet{
		Removes:         removes,
		Adds:            adds,
		LocationUpdates: locationUpdates,
		MetadataUpdates: metadataUpdates,
	}

	if ops.IsEmpty() {
		// Nothing differs from the library: the queued items are done.
		b.completeBatch(fb, false)
		return
	}

	slog.Info("iTunes write-back flushing",
		"locationUpdates", len(locationUpdates),
		"metadataUpdates_changed", changedMetadata,
		"metadataUpdates_skipped", skippedMetadata,
		"adds", len(adds),
		"removes", len(removes))

	if dryRun {
		slog.Info("iTunes write-back DRY-RUN active — no file written; batch stays queued",
			"locationUpdates", len(locationUpdates), "metadataUpdates", len(metadataUpdates), "adds", len(adds), "removes", len(removes), "path", writePath)
		for pid := range removes {
			slog.Info("iTunes write-back DRY-RUN would remove PID", "pid", pid)
		}
		// Kept, not consumed: dry-run writes nothing, so nothing is done.
		// Not re-armed on a timer, so dry-run does not spin.
		fb.bookIDs = append(fb.bookIDs, retryBooks.bookIDs...)
		retryBooks = flushBatch{}
		b.keepUnscheduled(fb)
		return
	}

	itlPath := writePath // from b.flushEnabled() above
	if err := SafeWriteITL(itlPath, ops); err != nil {
		// One failure, one backoff step: the lookup-failed books ride along.
		fb.bookIDs = append(fb.bookIDs, retryBooks.bookIDs...)
		retryBooks = flushBatch{}
		b.failBatch(fb, err)
		return
	}

	// Success: the batch is done. Clear its queue keys and the failure
	// status, then pin this state as the last-known-good anchor. .bak-lkg is
	// exempt from rotation, so even after keep-N churns through timestamped
	// backups a validated copy of a successfully-written library survives.
	b.completeBatch(fb, true)
	if err := itlPinLKGFn(itlPath); err != nil {
		slog.Warn("iTunes write-back could not pin last-known-good", "err", err, "path", itlPath)
	}

	// Tombstone the removed PIDs only now that the write has landed.
	b.markRemovesApplied(store, removes)

	// Mark the books we wrote as iTunes-synced so downstream UI /
	// filters reflect current state.
	if len(fb.bookIDs) > 0 {
		if n, markErr := store.MarkITunesSynced(fb.bookIDs); markErr != nil {
			slog.Warn("iTunes write-back MarkITunesSynced failed", "markErr", markErr)
		} else if n > 0 {
			slog.Info("iTunes write-back marked books as iTunes-synced", "n", n)
		}
	}
}

// markRemovesApplied marks each removed PID as removed in the external-id map.
// Called only after SafeWriteITL succeeded for the batch that carried them. A
// failure leaves a stale active mapping (the track IS gone from iTunes); it is
// logged so the drift is visible.
func (b *WriteBackBatcher) markRemovesApplied(store WriteBackStore, removes map[string]bool) {
	for pid := range removes {
		if err := store.MarkExternalIDRemoved("itunes", pid); err != nil {
			slog.Warn("iTunes write-back MarkExternalIDRemoved failed after the track was removed; external-id map now stale", "pid", pid, "err", err)
		}
	}
}

// Test hooks. Production code wires these to the real itunes
// package functions at package init. Tests override them so the
// safe-write cycle and diff-before-write logic can be unit-tested
// without needing a valid ITL fixture on disk — the fixture itself
// is fragile, format changes have broken it before, and mocking the
// external calls lets us test the logic in isolation.
var (
	itlValidateFn        = itunes.ValidateITL
	itlApplyOperationsFn = itunes.ApplyITLOperations
	parseITLFn           = itunes.ParseITL
	itlPinLKGFn          = itunes.PinLastKnownGood
	itlAuditFileFn       = auditITLFile
)

// auditITLFile re-reads a written ITL and runs the full ITLSafetyContract on
// it under cfg — the service wrapper's equivalent of the hardened path's
// step-5 re-read validation. It must use the SAME config the write used: the
// strict AuditITL it called before 2026-10-07 rejected the AO library's own
// ".itunes-writeback/" media locations on re-read.
func auditITLFile(path string, cfg itunes.ContractConfig) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("re-reading %s: %w", path, err)
	}
	if v := itunes.AuditITLWithConfig(data, cfg); !v.Pass {
		return fmt.Errorf("%s", v.Error())
	}
	return nil
}

// SafeWriteITL performs a validate → write-temp → validate-temp → backup →
// rename → validate-final cycle for ITL write-back. At every failure point the
// original ITL is either untouched or restored from the backup, so a
// corrupted write can never leave the user with an unreadable library file.
//
// Every step runs under itunes.WritebackContractConfig(itlPath): when itlPath
// is the AO writeback library, the location-form guard is scoped to that
// library's own media root (2026-10-07: without it every write to the prod
// library was rejected for its ~46k legitimate ".itunes-writeback/" locations).
//
// Sequence:
//  1. Ensure the source ITL currently parses (pre-condition check).
//     If it doesn't, abort — we won't compound an existing problem.
//  2. Run ApplyITLOperations to produce itlPath+".tmp".
//  3. Validate the .tmp file. If invalid, remove .tmp and abort — the
//     original itlPath is still intact.
//  4. Run the full contract on the re-read .tmp.
//  5. Copy itlPath → itlPath+".bak-<ts>" as the rollback anchor and prune
//     older backups to itlBackupRetention. Taken only now, once the write is
//     known to pass: a backup before each REJECTED attempt changed nothing
//     worth rolling back and pushed real history out of the rotation (prod
//     held 5 identical copies of an unchanged library on 2026-10-07).
//  6. Rename .tmp over itlPath.
//  7. Validate the renamed itlPath; on failure copy the backup back.
func SafeWriteITL(itlPath string, ops itunes.ITLOperationSet) error {
	cfg := itunes.WritebackContractConfig(itlPath)

	// Step 1: sanity-check the source. If the ITL we're about to
	// write over is ALREADY corrupted, the write has nothing to
	// validate against and a rollback wouldn't help anyway.
	if err := itlValidateFn(itlPath); err != nil {
		return fmt.Errorf("source ITL validation failed (refusing to write to a broken file): %w", err)
	}

	// Step 2: write the updated ITL to a temp file.
	tmpPath := itlPath + ".tmp"
	result, err := itlApplyOperationsFn(itlPath, tmpPath, ops, cfg)
	if err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("ApplyITLOperations: %w", err)
	}

	// Step 3: validate the temp BEFORE renaming. If the write
	// produced a file iTunes can't read, the original is still
	// intact at itlPath and we abort cleanly.
	if err := itlValidateFn(tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("validation of temp ITL failed (original preserved): %w", err)
	}

	// Step 4: run the FULL safety contract on the re-read temp bytes —
	// the same step-5 re-read audit the hardened itunes.SafeWriteITL
	// performs. ValidateITL only proves the header decodes and a track
	// exists; the audit catches encode/encrypt/deflate-path corruption
	// (the historic risk area) before the rename lands it.
	if err := itlAuditFileFn(tmpPath, cfg); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("contract audit of temp ITL failed (original preserved): %w", err)
	}

	// Step 5: backup-before-replace.
	backupPath, backupErr := writeITLBackup(itlPath)
	if backupErr != nil {
		// A failing backup isn't fatal for the write — the user can
		// still recover from the next successful run — but we log
		// prominently so they know the safety net was missing.
		slog.Warn("iTunes write-back backup failed; proceeding without a rollback anchor", "backupErr", backupErr)
		backupPath = ""
	} else if pruneErr := pruneITLBackups(itlPath, itlBackupRetention); pruneErr != nil {
		slog.Warn("iTunes write-back backup prune failed", "pruneErr", pruneErr)
	}

	// Step 6: rename .tmp over the original.
	if err := renameFile(tmpPath, itlPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename .tmp → itl: %w", err)
	}

	// Step 7: validate the final file. On paranoid-filesystem
	// failures or weird permission issues the rename can land but
	// the result is still corrupt. Catch that and roll back.
	if err := itlValidateFn(itlPath); err != nil {
		slog.Error("iTunes write-back post-rename validation failed", "err", err)
		if backupPath != "" {
			// CopyFileAtomic: this restores over the LIVE library after a failed
			// validation. The previous os.WriteFile was not atomic at all — a
			// crash partway through the rollback left neither a good library nor
			// a good original, the exact outcome the rollback exists to prevent.
			if rbErr := fileops.CopyFileAtomic(backupPath, itlPath); rbErr != nil {
				return fmt.Errorf("post-rename validation failed AND backup restore failed: validation=%v restore=%v", err, rbErr)
			}
			slog.Info("iTunes write-back restored from backup after corrupted write", "backupPath", backupPath)
			return fmt.Errorf("post-rename validation failed (restored from backup): %w", err)
		}
		return fmt.Errorf("post-rename validation failed (no backup available): %w", err)
	}

	slog.Info("iTunes write-back operations applied and validated", "result", result.UpdatedCount)
	return nil
}

// itlBackupRetention is how many rotating .bak-<ts> files to keep per ITL
// file. itunes.RotateBackups enforces it across every timestamped layout
// sharing the directory; .bak-lkg and hand-named snapshots are never touched.
// Checked on prod 2026-10-07: exactly 5 .bak-<ts> files, so the rotation was
// already bounded.
const itlBackupRetention = 5

// writeITLBackup copies itlPath to a timestamped sibling and
// returns the new path.
func writeITLBackup(itlPath string) (string, error) {
	// itunes.BackupName, not a local time.Now().Format: this writer's compact
	// LOCAL-time stamp was one of three formats sharing the directory, and the
	// lexical rotators could not order them against each other.
	backupPath := itunes.BackupName(itlPath, time.Now())
	// fileops.CopyFile: the old local helper slurped the whole ITL into RAM
	// via os.ReadFile/os.WriteFile, wrote it at a hardcoded 0644 regardless
	// of the library's own mode, and never fsynced.
	if err := fileops.CopyFile(itlPath, backupPath); err != nil {
		return "", err
	}
	return backupPath, nil
}

// pruneITLBackups deletes rotating backups beyond the keep limit.
//
// It delegates to itunes.RotateBackups, which orders by PARSED timestamp. See
// internal/itunes/backupname.go.
func pruneITLBackups(itlPath string, keep int) error {
	return itunes.RotateBackups(itlPath, keep)
}

// renameFile is a helper for os.Rename.
var renameFile = os.Rename

// Start reloads the durable queue a previous process left in the store (once)
// and schedules it; otherwise the batcher begins processing on the first
// Enqueue. Matches the serviceregistry.Starter signature, so Container.Start
// (Server.Start) drives it directly. Loading here rather than in the
// constructor keeps construction free of store I/O, which test servers built
// on strict mocks rely on.
func (b *WriteBackBatcher) Start(_ context.Context) error {
	if b == nil {
		return nil
	}
	b.loadOnce.Do(b.loadQueue)
	return nil
}

// Stop quiesces the batcher, joins every goroutine it started, and then
// flushes any pending writes exactly once. Signature matches
// serviceregistry.Stopper so Container.Stop can drive shutdown directly.
//
// The context is still unused: the wg.Wait below is unbounded by design.
// If the final drain fails, the batch stays in the durable queue and is
// reloaded by the next process.
func (b *WriteBackBatcher) Stop(_ context.Context) error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	if b.stopped {
		// Idempotent: never close stopCh twice or run a second drain.
		b.mu.Unlock()
		return nil
	}
	b.stopped = true
	b.stopTimerLocked()
	if b.stopCh != nil {
		// Releases any flush goroutine that was scheduled but has not
		// started. Nil on a struct-literal batcher (tests).
		close(b.stopCh)
	}
	b.mu.Unlock()

	// Join before draining. b.mu MUST be released first — a spawned flush
	// needs it to snapshot, and holding it here would deadlock shutdown.
	// b.stopped is already set, so no new goroutine can be scheduled.
	b.wg.Wait()

	// Exactly one final drain, with every worker joined, so nothing else can
	// be inside the writer. drainFlush, not flush: flush now refuses to run
	// after stop.
	b.drainFlush()
	return nil
}
