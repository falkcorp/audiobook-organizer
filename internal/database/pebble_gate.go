// file: internal/database/pebble_gate.go
// version: 1.0.0
// guid: 3f8a1c62-9e4b-4d7a-b5c0-6e2d1f9a8b47
// last-edited: 2026-10-11

package database

import (
	"context"
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
//   - A plain batch holds NO lease while it is only being built: batches are
//     process memory, and an unclosed batch (common) must not stall Close.
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
//   - Close's wait is bounded (PebbleStore.closeDrainTimeout). On timeout the
//     engine is deliberately NOT closed: leaking an open engine at process
//     exit is recoverable (WAL replay on next open), corrupting refcounts is a
//     crash. Close returns an error naming the in-flight count.
//   - A lease already held is never revoked: a held iterator keeps iterating
//     the live engine to the end. Making it report !Valid() once Close begins
//     would silently truncate scans whose loops do not check iter.Error(), and
//     some of those drive deletes. Long scans that should stop early take the
//     store's closing context instead (PebbleStore.closingCtx).
//
// DEADLOCK RULES FOR CALLERS OF beginClose/waitDrained
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

// dbGate counts held leases on one pebble.DB and lets Close wait for them.
//
// acquire increments active, then loads closing; beginClose stores closing,
// then loads active. Go's atomics are sequentially consistent, so at least
// one side sees the other: either beginClose sees the lease (and waits for
// its release) or acquire sees closing (and backs out without touching the
// engine).
type dbGate struct {
	active    atomic.Int64
	isClosing atomic.Bool
	drained   chan struct{}
	drainOnce sync.Once
}

func newDBGate() *dbGate {
	return &dbGate{drained: make(chan struct{})}
}

// acquire takes one lease, or refuses with errStoreClosing once Close began.
// It never blocks.
func (g *dbGate) acquire() error {
	g.active.Add(1)
	if g.isClosing.Load() {
		g.release()
		return errStoreClosing
	}
	return nil
}

// release returns one lease. The last release after Close began signals the
// drain.
func (g *dbGate) release() {
	n := g.active.Add(-1)
	if n < 0 {
		panic("pebble gate: lease released more times than acquired")
	}
	if n == 0 && g.isClosing.Load() {
		g.drainOnce.Do(func() { close(g.drained) })
	}
}

// beginClose refuses every later acquire. Leases already held stay valid.
// Safe to call more than once.
func (g *dbGate) beginClose() {
	g.isClosing.Store(true)
	if g.active.Load() == 0 {
		g.drainOnce.Do(func() { close(g.drained) })
	}
}

// closing reports whether beginClose has run.
func (g *dbGate) closing() bool {
	return g.isClosing.Load()
}

// inFlight reports the number of held leases.
func (g *dbGate) inFlight() int64 {
	return g.active.Load()
}

// waitDrained waits until every lease held when beginClose ran is released,
// or timeout passes. beginClose must have run. A timeout <= 0 waits forever.
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

// gatedReader is what GatedDB, GatedSnapshot and an indexed GatedBatch all
// provide: the gated replacement for pebble.Reader's read half.
type gatedReader interface {
	Get(key []byte) ([]byte, io.Closer, error)
	NewIter(o *pebble.IterOptions) (*GatedIter, error)
}

// GatedDB is the only handle to a store's pebble engine. It exposes the
// subset of *pebble.DB the codebase uses, with every call leased against the
// gate so Close can wait for it. It deliberately does not embed *pebble.DB:
// a new engine method must be added here, with its lease, before it can be
// called. See the file comment for the invariant.
type GatedDB struct {
	db   *pebble.DB
	gate *dbGate
}

// NewGatedDB wraps an engine the caller opened. Stores in this package close
// what they wrap through PebbleStore.Close / closeOwned, which drain first.
// A caller outside the package (a test fixture, an offline tool) that wraps
// its own raw engine still owns that engine's Close and must not call it
// while anything it handed this wrapper to is still running: the gate cannot
// see a Close it is not asked to perform.
func NewGatedDB(db *pebble.DB) *GatedDB {
	return &GatedDB{db: db, gate: newDBGate()}
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

// LogData writes data to the WAL only.
func (d *GatedDB) LogData(data []byte, opts *pebble.WriteOptions) error {
	if err := d.gate.acquire(); err != nil {
		return err
	}
	defer d.gate.release()
	return d.db.LogData(data, opts)
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
	return &GatedIter{Iterator: it, gate: d.gate}, nil
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
	return &GatedBatch{Batch: d.db.NewBatch(opts...), gate: d.gate}
}

// NewIndexedBatch returns a batch that can also be read. Building it holds no
// lease; Commit, Get and each iterator do.
func (d *GatedDB) NewIndexedBatch() *GatedBatch {
	return &GatedBatch{Batch: d.db.NewIndexedBatch(), gate: d.gate}
}

// beginClose refuses every later lease. See dbGate.beginClose.
func (d *GatedDB) beginClose() { d.gate.beginClose() }

// waitDrained waits for held leases. See dbGate.waitDrained.
func (d *GatedDB) waitDrained(timeout time.Duration) bool { return d.gate.waitDrained(timeout) }

// inFlight reports held leases.
func (d *GatedDB) inFlight() int64 { return d.gate.inFlight() }

// closeEngine closes the engine. Only after waitDrained returned true.
func (d *GatedDB) closeEngine() error { return d.db.Close() }

// closeOwned is Close for a store that owns its engine and has no ordering of
// its own to interleave (AIScanStore, an owning EmbeddingStore): refuse new
// leases, wait for held ones, then close. On timeout the engine is left open
// and an error returned, for the reason PebbleStore.Close gives.
func (d *GatedDB) closeOwned(timeout time.Duration) error {
	d.beginClose()
	if !d.waitDrained(timeout) {
		return fmt.Errorf("pebble close: %d engine accesses still in flight after %s: %w", d.inFlight(), timeout, errStoreClosing)
	}
	return d.closeEngine()
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

// GatedIter is a pebble iterator holding a gate lease. Every positioning and
// read method is pebble's, unchanged; Close releases the lease after the
// engine iterator is closed, at most once however many times Close runs.
type GatedIter struct {
	*pebble.Iterator
	gate *dbGate
	done atomic.Bool
}

// Close closes the engine iterator, then releases the lease.
func (it *GatedIter) Close() error {
	if !it.done.CompareAndSwap(false, true) {
		return nil
	}
	err := it.Iterator.Close()
	it.gate.release()
	return err
}

// Clone clones the iterator under a lease of its own (a clone pins its own
// readState).
func (it *GatedIter) Clone(opts pebble.CloneOptions) (*GatedIter, error) {
	if err := it.gate.acquire(); err != nil {
		return nil, err
	}
	c, err := it.Iterator.Clone(opts)
	if err != nil {
		it.gate.release()
		return nil, err
	}
	return &GatedIter{Iterator: c, gate: it.gate}, nil
}

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
	return &GatedIter{Iterator: it, gate: s.gate}, nil
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

// GatedBatch is a pebble batch whose engine-touching methods (Commit, and
// Get / NewIter on an indexed batch) hold a gate lease for their duration.
// The embedded *pebble.Batch's build methods (Set, Delete, DeleteRange, Len,
// Reset, Close, ...) are process memory only and pass through unchanged.
type GatedBatch struct {
	*pebble.Batch
	gate *dbGate
}

// Commit applies the batch to the engine.
func (b *GatedBatch) Commit(o *pebble.WriteOptions) error {
	if err := b.gate.acquire(); err != nil {
		return err
	}
	defer b.gate.release()
	return b.Batch.Commit(o)
}

// Get reads key through an indexed batch: the batch's own writes over the
// engine.
func (b *GatedBatch) Get(key []byte) ([]byte, io.Closer, error) {
	if err := b.gate.acquire(); err != nil {
		return nil, nil, err
	}
	v, c, err := b.Batch.Get(key)
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
	it, err := b.Batch.NewIter(o)
	if err != nil {
		b.gate.release()
		return nil, err
	}
	return &GatedIter{Iterator: it, gate: b.gate}, nil
}

// Apply appends other's operations to b (process memory only, no lease).
func (b *GatedBatch) Apply(other *GatedBatch, opts *pebble.WriteOptions) error {
	return b.Batch.Apply(other.Batch, opts)
}
