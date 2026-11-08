// file: internal/database/pebble_gate.go
// version: 1.4.0
// guid: 3f8a1c62-9e4b-4d7a-b5c0-6e2d1f9a8b47
// last-edited: 2026-11-08

package database

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/mediacore/storage"
)

// THE INVARIANT THIS FILE ENFORCES: pebble's DB.Close must never run while
// anything still holds engine state it tears down.
//
// An iterator (from the DB, a snapshot or an indexed batch), a Get's closer
// and a snapshot each pin a readState: a reference on the current version and
// on every memtable. DB.Close drops the DB's own references and frees the
// memtable queue without waiting for those pins. A reader released after (or
// while) Close runs unrefs memtables Close already let go of, and pebble
// panics "inconsistent reference count: -1" from the reader's Close. That is
// not pebble.ErrClosed, so recoverPebbleClosed correctly re-panics it and the
// process dies. Seen on CI (TestOpsV2Timeline_CloseRacesReconcile) with the
// startup timeline reconcile's snapshot iterator; in prod the same shape is a
// shutdown or restart landing during any long scan. Pebble flags the same
// violation on a clean interleaving as Close returning "leaked iterators" /
// "leaked snapshots" / "leaked memtable reservation".
//
// Every earlier fix for this family (memdb warmup cancel, the stats recompute
// mutex, the embedding backfill and trickle-warmer stops in server_lifecycle,
// recoverPebbleClosed) stopped ONE named goroutine. Any other reader racing
// Close stayed exposed. GatedDB is the general fix: it is the only handle to
// the engine (PebbleStore.db, and what PebbleStore.DB() lends to the borrower
// stores), and every engine access holds a lease on its dbGate for exactly as
// long as it holds engine state. Close refuses new leases, waits for the held
// ones to drain, and only then calls pebble's Close.
//
// LEASE LIFETIMES
//   - Iterator (DB, snapshot, indexed batch, Clone): NewIter → Close.
//   - Get closer: Get → closer.Close (pebble's Get pins an internal iterator
//     until then). A not-found Get holds nothing and releases at once.
//   - Snapshot: NewSnapshot → Close; its iterators and Gets take their own.
//   - Writes and maintenance (Set, Delete, DeleteRange, Merge, LogData,
//     batch Commit, Flush, Compact, Checkpoint, Metrics, SSTables,
//     EstimateDiskUsage): the duration of the call. A commit past pebble's
//     prepare step is still applying to a memtable when Close could run.
//   - SyncWAL (a sync-only WAL record): the duration of the call, and it is
//     the one access still admitted while Close drains. See DURABILITY.
//   - A plain batch holds NO lease while it is only being built: batches are
//     process memory, and an unclosed batch (common) must not stall Close.
//
// NOTHING EMBEDS A PEBBLE TYPE. GatedDB, GatedIter, GatedSnapshot and
// GatedBatch each hold their pebble value in an unexported field and forward
// only the methods the codebase uses. Embedding would promote every pebble
// method, including ones that open engine state (Batch.NewIterWithContext,
// Iterator.CloneWithContext), past the gate; without it, a new engine path
// does not compile until it is added here with its lease.
//
// DURABILITY AT SHUTDOWN
// pebble v2.1.7 keeps a NoSync WAL record in an in-process buffer until a
// later sync, a full WAL block, or engine close (record.LogWriter). Rows
// committed NoSync are made durable either by their writer's own sync
// (syncBookFileWAL) or by the engine close. Two shutdown paths broke that:
// a refused writer sync during the drain, and a timed-out drain that leaves
// the engine open, so the close-time flush never happens. So:
//   - SyncWAL is admitted while closing (still leased, so the drain waits for
//     it) and refused only once the gate is sealed, right before the engine
//     closes. A writer whose commit landed before Close began still gets the
//     sync its already-visible rows depend on.
//   - drainAndSeal syncs the WAL itself: once right after refusing new work
//     (everything committed so far), and again after the drain, on both the
//     success and the timeout path (everything the in-flight writers
//     committed meanwhile). On the timeout path the engine is left open but
//     every row committed before the timeout is on disk.
//   - Every one of those syncs is BOUNDED and LEASED. A WAL fsync can stall
//     for as long as the disk does (a pebble write stall, a hung volume), and
//     Close must still return. Each sync therefore runs in its own goroutine
//     under a sync lease and drainAndSeal stops waiting for it at its
//     deadline. The lease is what keeps that safe: a sync still in flight
//     keeps the lease count above zero, so the drain, seal and the engine's
//     Close all refuse to proceed under it, and the engine stays open. The
//     lease is released by the sync's own goroutine when the disk finally
//     answers. So a disk that stalls BEFORE the gate is sealed costs Close
//     at most the drain timeout plus syncGrace, ending in an error with the
//     engine open, and never a close under a running fsync.
//   - What that bound does NOT cover: pebble's own DB.Close, which runs after
//     seal. It waits for running flushes and compactions and closes the WAL
//     (a final sync), so a disk that stalls after seal, or a long compaction
//     already in progress, can still hold Close beyond the bound. Bounding
//     that would mean abandoning pebble's Close mid-way, which leaves the
//     engine in a state nothing here can reason about; the gate does not try.
//
// WHY A COUNTER AND NOT A LOCK
//   - Acquire never blocks, so nested leases (an op holding an iterator that
//     calls Get, a snapshot then snap.NewIter, an op calling an op) cannot
//     deadlock. A sync.RWMutex would: a nested RLock queues behind a pending
//     writer (Close) that is itself waiting on the outer RLock.
//   - After Close begins, acquire fails with an error wrapping
//     pebble.ErrClosed instead of blocking, so callers unwind through their
//     normal error path (all existing ErrClosed handling keeps working), and
//     the lease they already hold is released by their deferred Close.
//   - Close's wait is bounded (PebbleStore.closeDrainTimeout, plus syncGrace
//     for the WAL syncs that follow it). On timeout the engine is
//     deliberately NOT closed, after the WAL sync is attempted (see
//     DURABILITY): an engine left open at process exit replays its synced WAL
//     on the next open, while closing under a held lease crashes. Close
//     returns an error naming the in-flight count.
//   - A lease already held is never revoked: a held iterator keeps iterating
//     the live engine to the end. Making it report !Valid() once Close begins
//     would silently truncate scans whose loops do not check iter.Error(), and
//     some of those drive deletes. Long scans that should stop early take the
//     store's closing context instead (PebbleStore.closingCtx).
//
// DEADLOCK RULES FOR CALLERS OF drainAndSeal
//   - Never wait for the drain while holding a mutex an in-flight op may take
//     (opsMu, counterMu, metadata stripes, sampleMu): the op cannot finish and
//     release its lease. PebbleStore.Close drains first and only then takes
//     sampleMu.
//   - Close called from inside an op that holds a lease waits on itself until
//     the timeout, then returns an error without closing; it never hangs.

// errStoreClosing is returned by every gated access once Close has begun.
// It wraps pebble.ErrClosed so errors.Is(err, pebble.ErrClosed) callers (the
// registry's log-and-skip paths, recoverPebbleClosed's sentinel) treat it the
// same as a closed engine.
var errStoreClosing = fmt.Errorf("pebble store is closing: %w", pebble.ErrClosed)

// errCloseAttemptAborted is what a joined waiter gets when the close attempt
// it joined ended without producing a result (it panicked). It deliberately
// does not wrap pebble.ErrClosed: the engine is NOT known to be closed, and
// callers must treat it as a failed close.
var errCloseAttemptAborted = errors.New("pebble close: the close attempt did not finish")

// Gate state bits. The lease count and both flags live in ONE atomic word, so
// every transition reads the count and the flags it changes together.
const (
	// gateClosingBit: Close has begun; ordinary acquires are refused.
	gateClosingBit = int64(1) << 62
	// gateSealedBit: the drain finished and the engine is about to close;
	// SyncWAL acquires are refused too.
	gateSealedBit = int64(1) << 61
	// gateCountMask selects the lease count.
	gateCountMask = gateSealedBit - 1
)

// dbGate counts held leases on one pebble.DB and lets Close wait for them.
//
// Why one word: with the count and the closing flag in separate atomics, a
// releaser can decrement to zero BEFORE Close begins, be preempted, and then
// read the flag AFTER Close began. Meanwhile a new lease was taken and Close
// counted it. The releaser then signals the drain on a stale zero and the
// engine closes under the new lease. With one word every transition decides
// from the word it changes: acquire adds a lease only by a CAS from a value
// without the closing bit, beginClose sets the bit by an OR that returns the
// count at that instant, and release signals only when its own decrement
// produced "closing, zero leases". No lease can be added after that value
// exists, and no lease added before it can be missing from it.
type dbGate struct {
	state     atomic.Int64
	drained   chan struct{}
	drainOnce sync.Once
	// releaseHook, unset outside tests, runs inside release between the
	// decrement and the drain check: a preemption point for the
	// interleaving test. Atomic so a test can install or clear it while the
	// store is live (background goroutines release leases) without a race.
	releaseHook atomic.Pointer[func(n int64)]
	// shutdownHook, unset outside tests, runs inside drainAndSeal at the
	// shutdownStage points: where a test lands a commit or takes a sync
	// lease at an exact moment of the shutdown. Atomic for the same reason.
	shutdownHook atomic.Pointer[func(shutdownStage)]
}

// shutdownStage names a point in drainAndSeal where shutdownHook runs.
type shutdownStage int

const (
	// stageRefusedAndSynced: new work is refused and the first WAL sync has
	// returned; the wait for held leases has not started.
	stageRefusedAndSynced shutdownStage = iota + 1
	// stageDrained: the drain finished and the post-drain sync returned; the
	// gate is not sealed yet, so a SyncWAL can still be admitted.
	stageDrained
)

// syncGrace is how long past the drain deadline the WAL syncs that follow the
// wait, and seal's wait for a sync lease, may take. A WAL fsync is
// milliseconds on a healthy disk; this only bounds a stalled one.
const syncGrace = time.Second

func newDBGate() *dbGate {
	return &dbGate{drained: make(chan struct{})}
}

// acquire takes one ordinary lease, or refuses with errStoreClosing once
// Close began. It never blocks.
//
// A refused acquire never touches the count. An earlier form incremented
// first and backed out on seeing the flag; callers retrying in a tight loop
// after Close began then kept the count above zero between them, so the
// release of the last real lease never saw zero and the drain waited out its
// whole timeout (measured: TestDBGate_ConcurrentAcquireRelease under -cpu 8).
func (g *dbGate) acquire() error {
	return g.acquireUnless(gateClosingBit | gateSealedBit)
}

// acquireSync takes a lease for a sync-only WAL write. It is admitted while
// Close drains and refused only once the gate is sealed.
func (g *dbGate) acquireSync() error {
	return g.acquireUnless(gateSealedBit)
}

// acquireUnless adds one lease unless any of refuse is set, deciding on the
// same word it updates.
func (g *dbGate) acquireUnless(refuse int64) error {
	for {
		s := g.state.Load()
		if s&refuse != 0 {
			return errStoreClosing
		}
		if g.state.CompareAndSwap(s, s+1) {
			return nil
		}
	}
}

// release returns one lease. The release that leaves a closing gate with no
// leases signals the drain.
func (g *dbGate) release() {
	n := g.state.Add(-1)
	if n < 0 || n&gateCountMask == gateCountMask {
		panic("pebble gate: lease released more times than acquired")
	}
	if h := g.releaseHook.Load(); h != nil {
		(*h)(n)
	}
	if n&gateCountMask == 0 && n&gateClosingBit != 0 {
		g.drainOnce.Do(func() { close(g.drained) })
	}
}

// beginClose refuses every later ordinary acquire. Leases already held stay
// valid. Safe to call more than once.
func (g *dbGate) beginClose() {
	if g.state.Or(gateClosingBit)&gateCountMask == 0 {
		g.drainOnce.Do(func() { close(g.drained) })
	}
}

// closing reports whether beginClose has run.
func (g *dbGate) closing() bool {
	return g.state.Load()&gateClosingBit != 0
}

// inFlight reports the number of held leases.
func (g *dbGate) inFlight() int64 {
	return g.state.Load() & gateCountMask
}

// waitDrained waits until every ordinary lease held when beginClose ran is
// released, or timeout passes. beginClose must have run. A timeout <= 0 waits
// forever.
func (g *dbGate) waitDrained(timeout time.Duration) bool {
	if timeout <= 0 {
		<-g.drained
		return true
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-g.drained:
		return true
	case <-t.C:
		return false
	}
}

// waitDrainedUntil is waitDrained with an absolute deadline. A deadline that
// has already passed does not wait: it reports whether the drain had already
// fired.
func (g *dbGate) waitDrainedUntil(deadline time.Time) bool {
	select {
	case <-g.drained:
		return true
	default:
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return false
	}
	return g.waitDrained(remaining)
}

// seal moves a drained gate to sealed, refusing SyncWAL too. A SyncWAL
// admitted after the drain may still hold a lease; seal waits for it (a WAL
// fsync, milliseconds) until deadline. Refused acquires never touch the
// count, so only such a sync can make it wait. It reports whether the gate is
// sealed with no leases, the only state in which the engine may close.
func (g *dbGate) seal(deadline time.Time) bool {
	for {
		if g.state.CompareAndSwap(gateClosingBit, gateClosingBit|gateSealedBit) {
			return true
		}
		if s := g.state.Load(); s&gateSealedBit != 0 && s&gateCountMask == 0 {
			return true // sealed by an earlier Close
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Microsecond)
	}
}

// gatedReader is what GatedDB, GatedSnapshot and an indexed GatedBatch all
// provide: the gated replacement for pebble.Reader's read half.
type gatedReader interface {
	Get(key []byte) ([]byte, io.Closer, error)
	NewIter(o *pebble.IterOptions) (*GatedIter, error)
}

// GatedDB is the only handle to a store's engine. It exposes the subset of
// *pebble.DB the codebase uses, with every call leased against the gate so
// Close can wait for it. See the file comment for the invariant.
//
// It has two backends. The raw backend (db) is an engine its store opened
// itself: the AI scan store and the OpenLibrary cache. The core backend (lr)
// is books' claims on the main store, which core storage opens from release
// R1 (legacyrange.go). Exactly one is set.
type GatedDB struct {
	db   *pebble.DB
	lr   *legacyBackend
	gate *dbGate
}

// NewGatedDB wraps an engine the caller opened, for a caller that keeps the
// engine's lifecycle itself (a test fixture, an offline tool): it still owns
// the raw engine's Close and must not call it while anything it handed this
// wrapper to is running, because the gate cannot see a Close it is not asked
// to perform. A store that owns its engine uses NewOwnedGatedDB instead.
func NewGatedDB(db *pebble.DB) *GatedDB {
	return &GatedDB{db: db, gate: newDBGate()}
}

// NewOwnedGatedDB wraps an engine its caller owns and returns the only
// function that closes it: drain (bounded by timeout), sync, then close. The
// close function is separate from the handle on purpose: borrowers get the
// *GatedDB, and only the owner can close what they borrowed.
func NewOwnedGatedDB(db *pebble.DB) (*GatedDB, func(timeout time.Duration) error) {
	d := NewGatedDB(db)
	return d, d.ownedClose()
}

// ownedClose is the close function of a store that owns d's engine (raw) or
// the core DB behind d (a standalone core open).
//
// A close that times out (an access outlived the drain, or a WAL sync is
// stuck) syncs what it can, leaves the engine OPEN and returns an error. That
// is not final: the gate stays closing, so nothing new gets in, and a later
// call retries from the wait, closing the engine as soon as the straggler has
// released. A close that succeeded is idempotent: later calls return its
// result.
//
// Concurrent calls share ONE attempt. A caller that arrives while an attempt
// is running does not start its own wait: it waits for that attempt and
// returns its result, so N callers against a held straggler cost one bound
// (the first caller's timeout plus syncGrace), not N of them. Its own timeout
// is not used; the attempt in flight is the one that answers it. A call made
// after an attempt has finished is a new attempt, so a failed close is still
// retried by the next call.
func (d *GatedDB) ownedClose() func(timeout time.Duration) error {
	var mu sync.Mutex // guards the three fields below
	var closed bool
	var result error
	var inflight *closeAttempt
	return func(timeout time.Duration) error {
		mu.Lock()
		if closed {
			defer mu.Unlock()
			return result
		}
		if a := inflight; a != nil {
			mu.Unlock()
			<-a.done
			return a.err
		}
		a := &closeAttempt{done: make(chan struct{})}
		inflight = a
		mu.Unlock()

		// Finish the attempt however it ends, a panic included: waiters are
		// released and the next call can start a fresh one. err starts as a
		// failure and is replaced only by a result the attempt actually
		// produced, so a panic leaves the joined waiters an error rather than
		// a nil that reads as "closed": the caller (deleteOLData, factory
		// reset) would then remove the data under an engine that is still
		// open. The panic itself still propagates to the attempting caller.
		err := errCloseAttemptAborted
		sealed := false
		defer func() {
			mu.Lock()
			if sealed {
				closed = true
				result = err
			}
			inflight = nil
			a.err = err
			mu.Unlock()
			close(a.done)
		}()
		var drainErr error
		sealed, drainErr = d.drainAndSeal(timeout)
		if !sealed {
			err = drainErr
			return err
		}
		err = errors.Join(drainErr, d.closeEngine())
		return err
	}
}

// closeAttempt is one run of an owned close, shared by every caller that
// arrived while it ran. err is written before done closes.
type closeAttempt struct {
	done chan struct{}
	err  error
}

// Get reads key. On the raw backend the returned closer holds a lease until
// it is closed; on the core backend the value is a copy and the closer is a
// no-op.
func (d *GatedDB) Get(key []byte) ([]byte, io.Closer, error) {
	if err := d.gate.acquire(); err != nil {
		return nil, nil, err
	}
	if d.lr != nil {
		defer d.gate.release()
		v, err := d.lr.r.Get(context.Background(), key)
		if err != nil {
			return nil, nil, legacyErr(err)
		}
		return v, nopCloser{}, nil
	}
	v, c, err := d.db.Get(key)
	if err != nil {
		d.gate.release()
		return nil, nil, err
	}
	return v, &gatedCloser{c: c, gate: d.gate}, nil
}

// Set writes key=value.
func (d *GatedDB) Set(key, value []byte, opts *pebble.WriteOptions) error {
	if err := d.gate.acquire(); err != nil {
		return err
	}
	defer d.gate.release()
	if d.lr != nil {
		return d.lr.write(opts, func(b *storage.Batch) error { return b.Set(key, value) })
	}
	return d.db.Set(key, value, opts)
}

// Delete removes key.
func (d *GatedDB) Delete(key []byte, opts *pebble.WriteOptions) error {
	if err := d.gate.acquire(); err != nil {
		return err
	}
	defer d.gate.release()
	if d.lr != nil {
		return d.lr.write(opts, func(b *storage.Batch) error { return b.Delete(key) })
	}
	return d.db.Delete(key, opts)
}

// DeleteRange removes [start, end). On the core backend it removes the pieces
// of the range books' claims cover (stageClaimedDeleteRange).
func (d *GatedDB) DeleteRange(start, end []byte, opts *pebble.WriteOptions) error {
	if err := d.gate.acquire(); err != nil {
		return err
	}
	defer d.gate.release()
	if d.lr != nil {
		return d.lr.write(opts, func(b *storage.Batch) error { return stageClaimedDeleteRange(b, start, end) })
	}
	return d.db.DeleteRange(start, end, opts)
}

// SyncWAL makes every record committed so far durable (an fsync'd empty WAL
// record, or core's WAL barrier; the sequential WAL cannot persist it before
// the NoSync records ahead of it). It is admitted while Close drains, so a
// writer whose commit landed before Close began still gets its sync; it is
// refused only once the gate is sealed, right before the engine closes
// (which flushes the WAL itself).
func (d *GatedDB) SyncWAL() error {
	if err := d.gate.acquireSync(); err != nil {
		return err
	}
	defer d.gate.release()
	return d.syncWALLeased()
}

// Barrier makes every record committed so far durable, like SyncWAL, under an
// ordinary lease: refused once Close begins (HEAD's LogData(nil, Sync), which
// writers that committed NoSync use before a marker claims their rows exist).
func (d *GatedDB) Barrier() error {
	if err := d.gate.acquire(); err != nil {
		return err
	}
	defer d.gate.release()
	return d.syncWALLeased()
}

// syncWALLeased is the WAL sync itself, for a caller holding a lease.
func (d *GatedDB) syncWALLeased() error {
	if d.lr != nil {
		return legacyErr(d.lr.r.Barrier(context.Background()))
	}
	return d.db.LogData(nil, pebble.Sync)
}

// Flush flushes the memtable.
func (d *GatedDB) Flush() error {
	if err := d.gate.acquire(); err != nil {
		return err
	}
	defer d.gate.release()
	if d.lr != nil {
		a, err := d.lr.adminOf()
		if err != nil {
			return err
		}
		return legacyErr(a.Flush(context.Background()))
	}
	return d.db.Flush()
}

// Compact compacts [start, end). The core backend always allows parallel
// compactions (storage.Admin.Compact).
func (d *GatedDB) Compact(ctx context.Context, start, end []byte, parallelize bool) error {
	if err := d.gate.acquire(); err != nil {
		return err
	}
	defer d.gate.release()
	if d.lr != nil {
		a, err := d.lr.adminOf()
		if err != nil {
			return err
		}
		return legacyErr(a.Compact(ctx, start, end))
	}
	return d.db.Compact(ctx, start, end, parallelize)
}

// Checkpoint writes a consistent copy of the engine to destDir. The core
// backend takes core's checkpoint (storage.Admin.Checkpoint), which takes no
// pebble options.
func (d *GatedDB) Checkpoint(destDir string, opts ...pebble.CheckpointOption) error {
	if err := d.gate.acquire(); err != nil {
		return err
	}
	defer d.gate.release()
	if d.lr != nil {
		if len(opts) > 0 {
			return errors.New("pebble store: checkpoint options are not supported on core storage")
		}
		a, err := d.lr.adminOf()
		if err != nil {
			return err
		}
		return legacyErr(a.Checkpoint(context.Background(), destDir))
	}
	return d.db.Checkpoint(destDir, opts...)
}

// Metrics returns the raw engine's metrics, or errStoreClosing once Close
// began. The core backend has no pebble.Metrics; its figures come through
// engineSample and compactionStats instead.
func (d *GatedDB) Metrics() (*pebble.Metrics, error) {
	if d.lr != nil {
		return nil, errors.New("pebble store: engine metrics of the store core storage opened come from storage.Admin")
	}
	if err := d.gate.acquire(); err != nil {
		return nil, err
	}
	defer d.gate.release()
	return d.db.Metrics(), nil
}

// coreEngineSample is the core backend's engine counters (storage.Admin),
// for the Pebble collector and the census's flush check. The raw backend
// reads Metrics instead.
func (d *GatedDB) coreEngineSample() (storage.EngineSample, error) {
	if d.lr == nil {
		return storage.EngineSample{}, errors.New("pebble store: coreEngineSample on the raw backend")
	}
	if err := d.gate.acquire(); err != nil {
		return storage.EngineSample{}, err
	}
	defer d.gate.release()
	a, err := d.lr.adminOf()
	if err != nil {
		return storage.EngineSample{}, err
	}
	s, err := a.EngineSample()
	return s, legacyErr(err)
}

// coreCompactionStats is the core backend's compaction counters
// (storage.Admin); Absent when the store is closing or the admin surface is
// not attached.
func (d *GatedDB) coreCompactionStats() storage.CompactionStats {
	if d.lr == nil {
		return storage.CompactionStats{Absent: true}
	}
	if err := d.gate.acquire(); err != nil {
		return storage.CompactionStats{Absent: true}
	}
	defer d.gate.release()
	a, err := d.lr.adminOf()
	if err != nil {
		return storage.CompactionStats{Absent: true}
	}
	return a.CompactionStats()
}

// SSTables lists the engine's sstables (raw backend only: errNoSSTables on
// the core backend).
func (d *GatedDB) SSTables(opts ...pebble.SSTablesOption) ([][]pebble.SSTableInfo, error) {
	if d.lr != nil {
		return nil, errNoSSTables
	}
	if err := d.gate.acquire(); err != nil {
		return nil, err
	}
	defer d.gate.release()
	return d.db.SSTables(opts...)
}

// tableInfo is one sstable for the estimated census, on either backend.
type tableInfo struct {
	fileNum           uint64
	smallest, largest []byte
	size              uint64
	virtual           bool
	hasProps          bool
	entries           uint64
	deletions         uint64
	rawKey, rawVal    uint64
	rangeDels         uint64
	span              uint64
}

// tables lists the engine's sstables: with withProps their properties, and
// with lo/hi only the tables overlapping [lo, hi) and (span) each one's
// approximate bytes inside it. The raw backend asks Pebble; the core backend
// asks core's admin surface (storage.Admin.Tables).
func (d *GatedDB) tables(ctx context.Context, withProps bool, lo, hi []byte, span bool) ([]tableInfo, error) {
	if d.lr != nil {
		// Leased like every other engine call, so Close's drain sees a
		// listing in flight and a store already closing refuses it.
		if err := d.gate.acquire(); err != nil {
			return nil, err
		}
		defer d.gate.release()
		a, err := d.lr.adminOf()
		if err != nil {
			return nil, err
		}
		got, err := a.Tables(ctx, storage.TablesOptions{Properties: withProps, Start: lo, End: hi, SpanBytes: span})
		if err != nil {
			return nil, legacyErr(err)
		}
		out := make([]tableInfo, 0, len(got))
		for _, t := range got {
			out = append(out, tableInfo{fileNum: t.FileNum, smallest: t.Smallest, largest: t.Largest, size: t.Size,
				virtual: t.Virtual, hasProps: t.HasProperties, entries: t.Entries, deletions: t.Deletions,
				rawKey: t.RawKeyBytes, rawVal: t.RawValueBytes, rangeDels: t.RangeDeletions, span: t.SpanBytes})
		}
		return out, nil
	}
	var opts []pebble.SSTablesOption
	if withProps {
		opts = append(opts, pebble.WithProperties())
	}
	if lo != nil {
		opts = append(opts, pebble.WithKeyRangeFilter(lo, hi))
	}
	if span {
		opts = append(opts, pebble.WithApproximateSpanBytes())
	}
	levels, err := d.SSTables(opts...)
	if err != nil {
		return nil, err
	}
	var out []tableInfo
	for _, level := range levels {
		for i := range level {
			info := &level[i]
			t := tableInfo{fileNum: uint64(info.FileNum), smallest: append([]byte(nil), info.Smallest.UserKey...),
				largest: append([]byte(nil), info.Largest.UserKey...), size: info.Size, virtual: info.Virtual,
				span: info.ApproximateSpanBytes}
			if pr := info.Properties; pr != nil {
				t.hasProps = true
				t.entries, t.deletions = pr.NumEntries, pr.NumDeletions
				t.rawKey, t.rawVal, t.rangeDels = pr.RawKeySize, pr.RawValueSize, pr.NumRangeDeletions
			}
			out = append(out, t)
		}
	}
	return out, nil
}

// EstimateDiskUsage estimates the on-disk size of [start, end).
func (d *GatedDB) EstimateDiskUsage(start, end []byte) (uint64, error) {
	if err := d.gate.acquire(); err != nil {
		return 0, err
	}
	defer d.gate.release()
	if d.lr != nil {
		a, err := d.lr.adminOf()
		if err != nil {
			return 0, err
		}
		n, err := a.EstimateDiskUsage(start, end)
		return n, legacyErr(err)
	}
	return d.db.EstimateDiskUsage(start, end)
}

// FormatMajorVersion reports the engine's format version. It reads a field
// set at open, so it takes no lease. Core pins the same format as ABO
// (PebbleFormatMajorVersion) and ratchets the store to it before anything
// reads it, so the core backend reports that.
func (d *GatedDB) FormatMajorVersion() pebble.FormatMajorVersion {
	if d.lr != nil {
		return PebbleFormatMajorVersion
	}
	return d.db.FormatMajorVersion()
}

// NewIter opens an iterator that holds a lease until it is closed.
func (d *GatedDB) NewIter(o *pebble.IterOptions) (*GatedIter, error) {
	return d.newIter(o, false)
}

// newCensusIter is NewIter for the exact census: on the core backend its
// Stats also count the value bytes of every entry it lands on (the raw
// engine counts them itself), at the cost of reading each value.
func (d *GatedDB) newCensusIter(o *pebble.IterOptions) (*GatedIter, error) {
	return d.newIter(o, true)
}

func (d *GatedDB) newIter(o *pebble.IterOptions, countValues bool) (*GatedIter, error) {
	if err := d.gate.acquire(); err != nil {
		return nil, err
	}
	var (
		it  iterBackend
		err error
	)
	if d.lr != nil {
		it, err = legacyIterOver(o, countValues, func(so storage.ScanOptions) (*storage.Iterator, error) {
			return d.lr.r.NewIterator(context.Background(), so)
		})
	} else {
		var pit *pebble.Iterator
		pit, err = d.db.NewIter(o)
		it = pit
	}
	if err != nil {
		d.gate.release()
		return nil, err
	}
	return &GatedIter{it: it, gate: d.gate}, nil
}

// legacyIterOver opens a core iterator with o's bounds through open.
func legacyIterOver(o *pebble.IterOptions, countValues bool, open func(storage.ScanOptions) (*storage.Iterator, error)) (*legacyIter, error) {
	so, err := scanOptions(o)
	if err != nil {
		return nil, err
	}
	return newLegacyIter(func(start, end []byte) (*storage.Iterator, error) {
		return open(storage.ScanOptions{Start: start, End: end})
	}, so.Start, so.End, countValues)
}

// NewSnapshot opens a snapshot that holds a lease until it is closed. Once
// Close has begun (or the core snapshot cannot be taken) it returns a
// snapshot whose reads all fail with that error and whose Close is a no-op,
// so the signature stays error-free like pebble's.
func (d *GatedDB) NewSnapshot() *GatedSnapshot {
	if err := d.gate.acquire(); err != nil {
		return &GatedSnapshot{gate: d.gate, err: err}
	}
	if d.lr != nil {
		ls, err := d.lr.r.Snapshot()
		if err != nil {
			d.gate.release()
			return &GatedSnapshot{gate: d.gate, err: legacyErr(err)}
		}
		return &GatedSnapshot{lsnap: ls, gate: d.gate}
	}
	return &GatedSnapshot{snap: d.db.NewSnapshot(), gate: d.gate}
}

// NewBatch returns a write-only batch. Building it holds no lease; Commit
// does.
func (d *GatedDB) NewBatch(opts ...pebble.BatchOption) *GatedBatch {
	if d.lr != nil {
		return newCoreBatch(d.lr, d.lr.r.NewBatch(), false, d.gate)
	}
	return &GatedBatch{b: d.db.NewBatch(opts...), gate: d.gate}
}

// NewIndexedBatch returns a batch that can also be read. Building it holds no
// lease; Commit and Get do.
func (d *GatedDB) NewIndexedBatch() *GatedBatch {
	if d.lr != nil {
		return newCoreBatch(d.lr, d.lr.r.NewIndexedBatch(), true, d.gate)
	}
	return &GatedBatch{b: d.db.NewIndexedBatch(), indexed: true, gate: d.gate}
}

// newRecordingBatch returns a batch that only records its writes, for
// staging that is replayed into other batches with Apply. It touches no
// engine, so it needs no lease and works the same on both backends.
func (d *GatedDB) newRecordingBatch() *GatedBatch {
	return &GatedBatch{rec: &[]batchOp{}, gate: d.gate}
}

// syncWALForShutdown fsyncs the WAL, waiting no longer than deadline. Only
// drainAndSeal calls it, while the engine is known to be open (it closes the
// engine only after the last such call).
//
// The fsync runs in its own goroutine under a sync lease, so an fsync that
// outlives the deadline (a stalled disk) neither hangs the caller nor lets the
// engine close under it: the lease stays held until the fsync returns, and
// the drain, seal and the engine's Close all wait on the lease count. See
// DURABILITY AT SHUTDOWN.
func (d *GatedDB) syncWALForShutdown(stage string, deadline time.Time) error {
	if err := d.gate.acquireSync(); err != nil {
		return fmt.Errorf("pebble close: WAL sync %s: %w", stage, err)
	}
	res := make(chan error, 1)
	go func() {
		defer d.gate.release()
		res <- d.syncWALLeased()
	}()
	t := time.NewTimer(max(time.Until(deadline), 0))
	defer t.Stop()
	select {
	case err := <-res:
		if err != nil {
			pebbleCloseLog.Error("pebble close: WAL sync %s failed: %s", stage, err.Error())
			return fmt.Errorf("pebble close: WAL sync %s: %w", stage, err)
		}
		return nil
	case <-t.C:
		pebbleCloseLog.Error("pebble close: WAL sync %s still running at its deadline; not waiting for it, and the engine will stay open while it runs", stage)
		return fmt.Errorf("pebble close: WAL sync %s did not return in time: %w", stage, errStoreClosing)
	}
}

// drainAndSeal is every step of an engine close except the close itself:
//
//  1. Refuse new ordinary leases.
//  2. Sync the WAL: every row committed before Close began is durable even
//     if the process is killed during the drain.
//  3. Wait for held leases, up to timeout from the start of the call.
//  4. Sync the WAL again: rows the in-flight writers committed meanwhile.
//     On timeout, return an error here; the engine stays open.
//  5. Seal (refuse SyncWAL too), waiting out any sync admitted after the
//     drain.
//
// Every sync is bounded (syncWALForShutdown); the first by the drain
// deadline, the later ones and seal by that deadline plus syncGrace.
//
// sealed reports whether the engine may close: true only when no lease is
// held and none can be taken. err carries a timeout (sealed false) or a WAL
// sync failure (sealed may be true: the caller still closes the engine,
// which flushes the WAL itself, and reports the error alongside). Nothing
// here takes a store mutex, so in-flight ops can always finish.
//
// It may be called again after a timeout: beginClose is idempotent, the wait
// resumes on the same drain signal and a lease that has since been released
// no longer holds anything up.
func (d *GatedDB) drainAndSeal(timeout time.Duration) (sealed bool, err error) {
	d.gate.beginClose()
	if timeout <= 0 {
		timeout = DefaultCloseDrainTimeout
	}
	deadline := time.Now().Add(timeout)
	limit := deadline.Add(syncGrace)
	syncErr := d.syncWALForShutdown("after refusing new work", deadline)
	d.runShutdownHook(stageRefusedAndSynced)
	if !d.gate.waitDrainedUntil(deadline) {
		n := d.gate.inFlight()
		lateSyncErr := d.syncWALForShutdown("after a timed-out drain", limit)
		pebbleCloseLog.Error("pebble close: %d engine accesses still in flight after %s; leaving the engine open rather than closing it under them", n, timeout)
		return false, errors.Join(
			fmt.Errorf("pebble close: %d engine accesses still in flight after %s: %w", n, timeout, errStoreClosing),
			lateSyncErr)
	}
	if err := d.syncWALForShutdown("after the drain", limit); err != nil && syncErr == nil {
		syncErr = err
	}
	d.runShutdownHook(stageDrained)
	if !d.gate.seal(limit) {
		n := d.gate.inFlight()
		pebbleCloseLog.Error("pebble close: %d WAL syncs still in flight after the drain; leaving the engine open", n)
		return false, errors.Join(
			fmt.Errorf("pebble close: %d WAL syncs still in flight after the drain: %w", n, errStoreClosing),
			syncErr)
	}
	return true, syncErr
}

// runShutdownHook runs the test hook for stage, if one is installed.
func (d *GatedDB) runShutdownHook(stage shutdownStage) {
	if h := d.gate.shutdownHook.Load(); h != nil {
		(*h)(stage)
	}
}

// closeEngine closes the engine. Only after drainAndSeal, and never twice. On
// the core backend it closes the core DB a standalone opener opened, and does
// nothing when the kernel owns the store (the kernel closes it after every
// module has stopped).
func (d *GatedDB) closeEngine() error {
	if d.lr != nil {
		if d.lr.closeStore == nil {
			return nil
		}
		return d.lr.closeStore()
	}
	return d.db.Close()
}

// gatedCloser releases a Get's lease after the engine closer runs.
type gatedCloser struct {
	c    io.Closer
	gate *dbGate
	done atomic.Bool
}

func (g *gatedCloser) Close() error {
	if !g.done.CompareAndSwap(false, true) {
		return nil
	}
	err := g.c.Close()
	g.gate.release()
	return err
}

// GatedIter is an iterator holding a gate lease. Its positioning and read
// methods forward to the backend unchanged; Close releases the lease after
// the backend iterator is closed, at most once however many times Close runs.
type GatedIter struct {
	it   iterBackend
	gate *dbGate
	done atomic.Bool
}

// Close closes the backend iterator, then releases the lease.
func (it *GatedIter) Close() error {
	if !it.done.CompareAndSwap(false, true) {
		return nil
	}
	err := it.it.Close()
	it.gate.release()
	return err
}

// First, Last, Next, Prev, SeekGE, SeekLT, Valid, Key, Value, ValueAndErr,
// Error, SetBounds, Stats and ResetStats forward to the backend.

func (it *GatedIter) First() bool                   { return it.it.First() }
func (it *GatedIter) Last() bool                    { return it.it.Last() }
func (it *GatedIter) Next() bool                    { return it.it.Next() }
func (it *GatedIter) Prev() bool                    { return it.it.Prev() }
func (it *GatedIter) SeekGE(key []byte) bool        { return it.it.SeekGE(key) }
func (it *GatedIter) SeekLT(key []byte) bool        { return it.it.SeekLT(key) }
func (it *GatedIter) Valid() bool                   { return it.it.Valid() }
func (it *GatedIter) Key() []byte                   { return it.it.Key() }
func (it *GatedIter) Value() []byte                 { return it.it.Value() }
func (it *GatedIter) ValueAndErr() ([]byte, error)  { return it.it.ValueAndErr() }
func (it *GatedIter) Error() error                  { return it.it.Error() }
func (it *GatedIter) SetBounds(lower, upper []byte) { it.it.SetBounds(lower, upper) }
func (it *GatedIter) Stats() pebble.IteratorStats   { return it.it.Stats() }
func (it *GatedIter) ResetStats()                   { it.it.ResetStats() }

// GatedSnapshot is a snapshot holding a gate lease until Close. Its
// iterators and Gets take leases of their own.
type GatedSnapshot struct {
	snap  *pebble.Snapshot  // raw backend
	lsnap *storage.Snapshot // core backend
	gate  *dbGate
	err   error // non-nil: opened after Close began; every read fails with it
	done  atomic.Bool
}

// Get reads key as of the snapshot.
func (s *GatedSnapshot) Get(key []byte) ([]byte, io.Closer, error) {
	if s.err != nil {
		return nil, nil, s.err
	}
	if err := s.gate.acquire(); err != nil {
		return nil, nil, err
	}
	if s.lsnap != nil {
		defer s.gate.release()
		v, err := s.lsnap.Get(context.Background(), key)
		if err != nil {
			return nil, nil, legacyErr(err)
		}
		return v, nopCloser{}, nil
	}
	v, c, err := s.snap.Get(key)
	if err != nil {
		s.gate.release()
		return nil, nil, err
	}
	return v, &gatedCloser{c: c, gate: s.gate}, nil
}

// NewIter opens an iterator over the snapshot.
func (s *GatedSnapshot) NewIter(o *pebble.IterOptions) (*GatedIter, error) {
	if s.err != nil {
		return nil, s.err
	}
	if err := s.gate.acquire(); err != nil {
		return nil, err
	}
	var (
		it  iterBackend
		err error
	)
	if s.lsnap != nil {
		it, err = legacyIterOver(o, false, func(so storage.ScanOptions) (*storage.Iterator, error) {
			return s.lsnap.NewIterator(context.Background(), so)
		})
	} else {
		var pit *pebble.Iterator
		pit, err = s.snap.NewIter(o)
		it = pit
	}
	if err != nil {
		s.gate.release()
		return nil, err
	}
	return &GatedIter{it: it, gate: s.gate}, nil
}

// Close closes the snapshot, then releases its lease. Idempotent.
func (s *GatedSnapshot) Close() error {
	if s.err != nil || !s.done.CompareAndSwap(false, true) {
		return nil
	}
	var err error
	if s.lsnap != nil {
		err = legacyErr(s.lsnap.Close())
	} else {
		err = s.snap.Close()
	}
	s.gate.release()
	return err
}

// GatedBatch is a batch. Its engine-touching methods (Commit, and Get on an
// indexed batch) hold a gate lease for their duration; its build methods are
// process memory and forward without one. It has three backends: a pebble
// batch (raw), a core batch over books' claims (core), or a recording of its
// writes (newRecordingBatch), which can only be replayed into another batch
// with Apply.
type GatedBatch struct {
	b       *pebble.Batch
	lb      *storage.Batch
	rec     *[]batchOp
	trace   *[]batchOp     // a core batch's staged writes, only under traceBatchWrites
	commits *atomic.Uint64 // the core backend's commit count (legacyBackend.commits)
	indexed bool
	gate    *dbGate
}

// traceBatchWrites (tests only; a count, so parallel tests can each hold it)
// makes every core batch built while it is above zero keep a copy of each
// write it stages, in GatedBatch.trace. A pebble batch
// can be read back (Batch.Reader); a core batch cannot, and the tests that
// pin which keys a write path stages (a rewrite nobody needed, a delete of a
// key that was never there) need to see them.
var traceBatchWrites atomic.Int32

// newCoreBatch wraps a core batch, traced when traceBatchWrites is set.
func newCoreBatch(l *legacyBackend, lb *storage.Batch, indexed bool, gate *dbGate) *GatedBatch {
	b := &GatedBatch{lb: lb, indexed: indexed, gate: gate, commits: &l.commits}
	if traceBatchWrites.Load() > 0 {
		b.trace = &[]batchOp{}
	}
	return b
}

// errRecordingBatch is returned by an engine call on a recording batch.
var errRecordingBatch = errors.New("pebble store: a recording batch only replays into another batch")

// Commit applies the batch to the engine.
func (b *GatedBatch) Commit(o *pebble.WriteOptions) error {
	if b.rec != nil {
		return errRecordingBatch
	}
	if err := b.gate.acquire(); err != nil {
		return err
	}
	defer b.gate.release()
	if b.lb != nil {
		n := b.lb.Count()
		if err := b.lb.Commit(context.Background(), durability(o)); err != nil {
			return legacyErr(err)
		}
		if n > 0 {
			b.commits.Add(1)
		}
		return nil
	}
	return b.b.Commit(o)
}

// Get reads key through an indexed batch: the batch's own writes over the
// engine.
func (b *GatedBatch) Get(key []byte) ([]byte, io.Closer, error) {
	if b.rec != nil {
		return nil, nil, errRecordingBatch
	}
	if err := b.gate.acquire(); err != nil {
		return nil, nil, err
	}
	if b.lb != nil {
		defer b.gate.release()
		v, err := b.lb.Get(context.Background(), key)
		if err != nil {
			return nil, nil, legacyErr(err)
		}
		return v, nopCloser{}, nil
	}
	v, c, err := b.b.Get(key)
	if err != nil {
		b.gate.release()
		return nil, nil, err
	}
	return v, &gatedCloser{c: c, gate: b.gate}, nil
}

// Apply appends a recording batch's writes to b (process memory only, no
// lease). Only a batch from newRecordingBatch can be applied: a core batch
// cannot be copied into another.
func (b *GatedBatch) Apply(other *GatedBatch, _ *pebble.WriteOptions) error {
	if other.rec == nil {
		return errors.New("pebble store: Apply takes a recording batch (GatedDB.newRecordingBatch)")
	}
	for _, op := range *other.rec {
		var err error
		switch op.kind {
		case batchOpSet:
			err = b.Set(op.key, op.value, nil)
		case batchOpDelete:
			err = b.Delete(op.key, nil)
		case batchOpDeleteRange:
			err = b.DeleteRange(op.key, op.value, nil)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// record appends one write to a recording batch, copying its bytes (pebble
// batches copy too, so callers may reuse their buffers).
func (b *GatedBatch) record(kind uint8, key, value []byte) {
	appendOp(b.rec, kind, key, value)
}

// staged notes a write a core batch accepted, when it is traced.
func (b *GatedBatch) staged(err error, kind uint8, key, value []byte) error {
	if err == nil && b.trace != nil {
		appendOp(b.trace, kind, key, value)
	}
	return legacyErr(err)
}

func appendOp(ops *[]batchOp, kind uint8, key, value []byte) {
	*ops = append(*ops, batchOp{kind: kind, key: append([]byte(nil), key...), value: append([]byte(nil), value...)})
}

// Set, Delete, DeleteRange, Close, Len, Count, Empty and Indexed are process
// memory. On the core backend Set, Delete and DeleteRange are checked against
// books' claims as they are staged.

func (b *GatedBatch) Set(key, value []byte, opts *pebble.WriteOptions) error {
	switch {
	case b.rec != nil:
		b.record(batchOpSet, key, value)
		return nil
	case b.lb != nil:
		return b.staged(b.lb.Set(key, value), batchOpSet, key, value)
	}
	return b.b.Set(key, value, opts)
}

func (b *GatedBatch) Delete(key []byte, opts *pebble.WriteOptions) error {
	switch {
	case b.rec != nil:
		b.record(batchOpDelete, key, nil)
		return nil
	case b.lb != nil:
		return b.staged(b.lb.Delete(key), batchOpDelete, key, nil)
	}
	return b.b.Delete(key, opts)
}

func (b *GatedBatch) DeleteRange(start, end []byte, opts *pebble.WriteOptions) error {
	switch {
	case b.rec != nil:
		b.record(batchOpDeleteRange, start, end)
		return nil
	case b.lb != nil:
		return b.staged(stageClaimedDeleteRange(b.lb, start, end), batchOpDeleteRange, start, end)
	}
	return b.b.DeleteRange(start, end, opts)
}

func (b *GatedBatch) Close() error {
	switch {
	case b.rec != nil:
		*b.rec = nil
		return nil
	case b.lb != nil:
		return legacyErr(b.lb.Close())
	}
	return b.b.Close()
}

// Len is the batch's size in bytes (a recording batch: the bytes it holds).
func (b *GatedBatch) Len() int {
	switch {
	case b.rec != nil:
		n := 0
		for _, op := range *b.rec {
			n += len(op.key) + len(op.value)
		}
		return n
	case b.lb != nil:
		return b.lb.Len()
	}
	return b.b.Len()
}

// Count is the number of writes staged.
func (b *GatedBatch) Count() uint32 {
	switch {
	case b.rec != nil:
		return uint32(len(*b.rec))
	case b.lb != nil:
		return uint32(b.lb.Count())
	}
	return b.b.Count()
}

func (b *GatedBatch) Empty() bool   { return b.Count() == 0 }
func (b *GatedBatch) Indexed() bool { return b.indexed }
