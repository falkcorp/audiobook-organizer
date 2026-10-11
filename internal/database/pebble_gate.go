// file: internal/database/pebble_gate.go
// version: 1.3.2
// guid: 3f8a1c62-9e4b-4d7a-b5c0-6e2d1f9a8b47
// last-edited: 2026-10-11

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

// GatedDB is the only handle to a store's pebble engine. It exposes the
// subset of *pebble.DB the codebase uses, with every call leased against the
// gate so Close can wait for it. See the file comment for the invariant.
type GatedDB struct {
	db   *pebble.DB
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
func NewOwnedGatedDB(db *pebble.DB) (*GatedDB, func(timeout time.Duration) error) {
	d := NewGatedDB(db)
	var mu sync.Mutex // guards the three fields below
	var closed bool
	var result error
	var inflight *closeAttempt
	return d, func(timeout time.Duration) error {
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
		// released and the next call can start a fresh one.
		var err error
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
		sealed, err = d.drainAndSeal(timeout)
		if sealed {
			err = errors.Join(err, d.closeEngine())
		}
		return err
	}
}

// closeAttempt is one run of an owned close, shared by every caller that
// arrived while it ran. err is written before done closes.
type closeAttempt struct {
	done chan struct{}
	err  error
}

// Get reads key. The returned closer holds a lease until it is closed.
func (d *GatedDB) Get(key []byte) ([]byte, io.Closer, error) {
	if err := d.gate.acquire(); err != nil {
		return nil, nil, err
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
	return d.db.Set(key, value, opts)
}

// Delete removes key.
func (d *GatedDB) Delete(key []byte, opts *pebble.WriteOptions) error {
	if err := d.gate.acquire(); err != nil {
		return err
	}
	defer d.gate.release()
	return d.db.Delete(key, opts)
}

// DeleteRange removes [start, end).
func (d *GatedDB) DeleteRange(start, end []byte, opts *pebble.WriteOptions) error {
	if err := d.gate.acquire(); err != nil {
		return err
	}
	defer d.gate.release()
	return d.db.DeleteRange(start, end, opts)
}

// Merge merges value into key.
func (d *GatedDB) Merge(key, value []byte, opts *pebble.WriteOptions) error {
	if err := d.gate.acquire(); err != nil {
		return err
	}
	defer d.gate.release()
	return d.db.Merge(key, value, opts)
}

// LogData writes data to the WAL only. For a sync-only record use SyncWAL,
// which is admitted while Close drains.
func (d *GatedDB) LogData(data []byte, opts *pebble.WriteOptions) error {
	if err := d.gate.acquire(); err != nil {
		return err
	}
	defer d.gate.release()
	return d.db.LogData(data, opts)
}

// SyncWAL makes every record committed so far durable (an fsync'd empty WAL
// record; the sequential WAL cannot persist it before the NoSync records
// ahead of it). It is admitted while Close drains, so a writer whose commit
// landed before Close began still gets its sync; it is refused only once the
// gate is sealed, right before the engine closes (which flushes the WAL
// itself).
func (d *GatedDB) SyncWAL() error {
	if err := d.gate.acquireSync(); err != nil {
		return err
	}
	defer d.gate.release()
	return d.db.LogData(nil, pebble.Sync)
}

// Flush flushes the memtable.
func (d *GatedDB) Flush() error {
	if err := d.gate.acquire(); err != nil {
		return err
	}
	defer d.gate.release()
	return d.db.Flush()
}

// Compact compacts [start, end).
func (d *GatedDB) Compact(ctx context.Context, start, end []byte, parallelize bool) error {
	if err := d.gate.acquire(); err != nil {
		return err
	}
	defer d.gate.release()
	return d.db.Compact(ctx, start, end, parallelize)
}

// Checkpoint writes a consistent copy of the engine to destDir.
func (d *GatedDB) Checkpoint(destDir string, opts ...pebble.CheckpointOption) error {
	if err := d.gate.acquire(); err != nil {
		return err
	}
	defer d.gate.release()
	return d.db.Checkpoint(destDir, opts...)
}

// Metrics returns the engine's metrics, or errStoreClosing once Close began.
func (d *GatedDB) Metrics() (*pebble.Metrics, error) {
	if err := d.gate.acquire(); err != nil {
		return nil, err
	}
	defer d.gate.release()
	return d.db.Metrics(), nil
}

// SSTables lists the engine's sstables.
func (d *GatedDB) SSTables(opts ...pebble.SSTablesOption) ([][]pebble.SSTableInfo, error) {
	if err := d.gate.acquire(); err != nil {
		return nil, err
	}
	defer d.gate.release()
	return d.db.SSTables(opts...)
}

// EstimateDiskUsage estimates the on-disk size of [start, end).
func (d *GatedDB) EstimateDiskUsage(start, end []byte) (uint64, error) {
	if err := d.gate.acquire(); err != nil {
		return 0, err
	}
	defer d.gate.release()
	return d.db.EstimateDiskUsage(start, end)
}

// FormatMajorVersion reports the engine's format version. It reads a field
// set at open, so it takes no lease.
func (d *GatedDB) FormatMajorVersion() pebble.FormatMajorVersion {
	return d.db.FormatMajorVersion()
}

// NewIter opens an iterator that holds a lease until it is closed.
func (d *GatedDB) NewIter(o *pebble.IterOptions) (*GatedIter, error) {
	if err := d.gate.acquire(); err != nil {
		return nil, err
	}
	it, err := d.db.NewIter(o)
	if err != nil {
		d.gate.release()
		return nil, err
	}
	return &GatedIter{it: it, gate: d.gate}, nil
}

// NewSnapshot opens a snapshot that holds a lease until it is closed. Once
// Close has begun it returns a snapshot whose reads all fail with
// errStoreClosing and whose Close is a no-op, so the signature stays
// error-free like pebble's.
func (d *GatedDB) NewSnapshot() *GatedSnapshot {
	if err := d.gate.acquire(); err != nil {
		return &GatedSnapshot{gate: d.gate, err: err}
	}
	return &GatedSnapshot{snap: d.db.NewSnapshot(), gate: d.gate}
}

// NewBatch returns a write-only batch. Building it holds no lease; Commit
// does.
func (d *GatedDB) NewBatch(opts ...pebble.BatchOption) *GatedBatch {
	return &GatedBatch{b: d.db.NewBatch(opts...), gate: d.gate}
}

// NewIndexedBatch returns a batch that can also be read. Building it holds no
// lease; Commit, Get and each iterator do.
func (d *GatedDB) NewIndexedBatch() *GatedBatch {
	return &GatedBatch{b: d.db.NewIndexedBatch(), gate: d.gate}
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
		res <- d.db.LogData(nil, pebble.Sync)
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

// closeEngine closes the engine. Only after drainAndSeal, and never twice.
func (d *GatedDB) closeEngine() error { return d.db.Close() }

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

// GatedIter is a pebble iterator holding a gate lease. Its positioning and
// read methods forward to pebble unchanged; Close releases the lease after
// the engine iterator is closed, at most once however many times Close runs.
type GatedIter struct {
	it   *pebble.Iterator
	gate *dbGate
	done atomic.Bool
}

// Close closes the engine iterator, then releases the lease.
func (it *GatedIter) Close() error {
	if !it.done.CompareAndSwap(false, true) {
		return nil
	}
	err := it.it.Close()
	it.gate.release()
	return err
}

// Clone clones the iterator under a lease of its own (a clone pins its own
// readState).
func (it *GatedIter) Clone(opts pebble.CloneOptions) (*GatedIter, error) {
	if err := it.gate.acquire(); err != nil {
		return nil, err
	}
	c, err := it.it.Clone(opts)
	if err != nil {
		it.gate.release()
		return nil, err
	}
	return &GatedIter{it: c, gate: it.gate}, nil
}

// First, Last, Next, Prev, SeekGE, SeekLT, SeekPrefixGE, Valid, Key, Value,
// ValueAndErr, Error, SetBounds, Stats and ResetStats forward to pebble.

func (it *GatedIter) First() bool                   { return it.it.First() }
func (it *GatedIter) Last() bool                    { return it.it.Last() }
func (it *GatedIter) Next() bool                    { return it.it.Next() }
func (it *GatedIter) Prev() bool                    { return it.it.Prev() }
func (it *GatedIter) SeekGE(key []byte) bool        { return it.it.SeekGE(key) }
func (it *GatedIter) SeekLT(key []byte) bool        { return it.it.SeekLT(key) }
func (it *GatedIter) SeekPrefixGE(key []byte) bool  { return it.it.SeekPrefixGE(key) }
func (it *GatedIter) Valid() bool                   { return it.it.Valid() }
func (it *GatedIter) Key() []byte                   { return it.it.Key() }
func (it *GatedIter) Value() []byte                 { return it.it.Value() }
func (it *GatedIter) ValueAndErr() ([]byte, error)  { return it.it.ValueAndErr() }
func (it *GatedIter) Error() error                  { return it.it.Error() }
func (it *GatedIter) SetBounds(lower, upper []byte) { it.it.SetBounds(lower, upper) }
func (it *GatedIter) Stats() pebble.IteratorStats   { return it.it.Stats() }
func (it *GatedIter) ResetStats()                   { it.it.ResetStats() }

// GatedSnapshot is a pebble snapshot holding a gate lease until Close. Its
// iterators and Gets take leases of their own.
type GatedSnapshot struct {
	snap *pebble.Snapshot
	gate *dbGate
	err  error // non-nil: opened after Close began; every read fails with it
	done atomic.Bool
}

// Get reads key as of the snapshot.
func (s *GatedSnapshot) Get(key []byte) ([]byte, io.Closer, error) {
	if s.err != nil {
		return nil, nil, s.err
	}
	if err := s.gate.acquire(); err != nil {
		return nil, nil, err
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
	it, err := s.snap.NewIter(o)
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
	err := s.snap.Close()
	s.gate.release()
	return err
}

// GatedBatch is a pebble batch. Its engine-touching methods (Commit, and Get
// / NewIter on an indexed batch) hold a gate lease for their duration; its
// build methods are process memory and forward to pebble without one.
type GatedBatch struct {
	b    *pebble.Batch
	gate *dbGate
}

// Commit applies the batch to the engine.
func (b *GatedBatch) Commit(o *pebble.WriteOptions) error {
	if err := b.gate.acquire(); err != nil {
		return err
	}
	defer b.gate.release()
	return b.b.Commit(o)
}

// Get reads key through an indexed batch: the batch's own writes over the
// engine.
func (b *GatedBatch) Get(key []byte) ([]byte, io.Closer, error) {
	if err := b.gate.acquire(); err != nil {
		return nil, nil, err
	}
	v, c, err := b.b.Get(key)
	if err != nil {
		b.gate.release()
		return nil, nil, err
	}
	return v, &gatedCloser{c: c, gate: b.gate}, nil
}

// NewIter opens an iterator over an indexed batch merged with the engine.
func (b *GatedBatch) NewIter(o *pebble.IterOptions) (*GatedIter, error) {
	if err := b.gate.acquire(); err != nil {
		return nil, err
	}
	it, err := b.b.NewIter(o)
	if err != nil {
		b.gate.release()
		return nil, err
	}
	return &GatedIter{it: it, gate: b.gate}, nil
}

// Apply appends other's operations to b (process memory only, no lease).
func (b *GatedBatch) Apply(other *GatedBatch, opts *pebble.WriteOptions) error {
	return b.b.Apply(other.b, opts)
}

// Set, Delete, DeleteRange, Merge, Close, Reset, Len, Count, Empty and
// Indexed are process memory and forward to pebble.

func (b *GatedBatch) Set(key, value []byte, opts *pebble.WriteOptions) error {
	return b.b.Set(key, value, opts)
}
func (b *GatedBatch) Delete(key []byte, opts *pebble.WriteOptions) error {
	return b.b.Delete(key, opts)
}
func (b *GatedBatch) DeleteRange(start, end []byte, opts *pebble.WriteOptions) error {
	return b.b.DeleteRange(start, end, opts)
}
func (b *GatedBatch) Merge(key, value []byte, opts *pebble.WriteOptions) error {
	return b.b.Merge(key, value, opts)
}
func (b *GatedBatch) Close() error  { return b.b.Close() }
func (b *GatedBatch) Reset()        { b.b.Reset() }
func (b *GatedBatch) Len() int      { return b.b.Len() }
func (b *GatedBatch) Count() uint32 { return b.b.Count() }
func (b *GatedBatch) Empty() bool   { return b.b.Empty() }
func (b *GatedBatch) Indexed() bool { return b.b.Indexed() }
