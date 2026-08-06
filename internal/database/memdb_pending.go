// file: internal/database/memdb_pending.go
// version: 1.0.0
// guid: 8c4f2a71-6d93-4e05-b1a8-2f7e9c6d0b34
// last-edited: 2026-08-06

package database

import (
	"log/slog"
	"sync"
)

// Pending-write buffer for the async memdb warmup window.
//
// ── THE INVARIANT ───────────────────────────────────────────────────────────
//
//	No write that succeeds in Pebble may be absent from the published memdb.
//
// Every memdb-backed read (library listings, dedup scans, maintenance jobs)
// trusts the published snapshot completely — it does not fall back to Pebble on
// a miss. A snapshot that is quietly missing rows is therefore worse than
// having no memdb at all, and there is no self-healing path: memdb is published
// exactly once per process and nothing re-warms it.
//
// ── WHY THE WINDOW EXISTED ──────────────────────────────────────────────────
//
// NewPebbleStore returns immediately and runs WarmFromPebble in a goroutine, so
// the HTTP server starts serving (and writing) while warmup is still scanning —
// roughly two minutes for a ~50K-book library. During that time memPtr is nil,
// and memSync's old first line was:
//
//	if p.mem() == nil { return }   // ← silently dropped the write-through
//
// WarmFromPebble scans through Pebble iterators whose view is fixed when they
// are created. So a write splits into two cases:
//
//   - lands before its table's iterator exists → the scan picks it up. Fine.
//   - lands after that iterator exists but before memPtr.Store → the scan
//     cannot see it AND its write-through was dropped. The row is in Pebble and
//     permanently invisible to memdb for the rest of the process lifetime.
//
// This is NOT a data race. memPtr is an atomic.Pointer used correctly and -race
// will never flag it; it is a lost update, and a mutex around the pointer would
// change nothing.
//
// ── THE FIX: BUFFER AND REPLAY ──────────────────────────────────────────────
//
// While warmup is in flight, memSync records each write-through instead of
// dropping it. Immediately before memPtr is published — in the SAME critical
// section, so no write can slip between replay and publish — the recorded
// write-throughs are replayed into the fresh MemStore in FIFO order.
//
// Replaying the closures (rather than, say, re-scanning changed keys) means the
// replay performs the exact same mutations the live path would have performed,
// through the same already-tested code. It is also idempotent against the scan:
// inserts are upserts, and the delete helpers look a row up before removing it,
// so a write that BOTH the scan captured and the buffer recorded converges to
// the same state either way.
//
// Ordering is what makes this safe. A writer either takes the buffer lock
// before the publisher (→ recorded, then replayed) or after it (→ sees the
// published memPtr and applies directly). There is no third case, so no write
// is ever dropped.

// memPendingOpCap bounds how many write-throughs may be buffered during warmup.
//
// A buffered op retains whatever its closure captured — for UpsertBookToMemDB
// that is the caller's *Book, roughly 1–3KB with Description and friends still
// attached. 50,000 ops is therefore on the order of 50–150MB of transient heap,
// which is the ceiling we are willing to pay to keep memdb correct.
//
// Reaching this cap means a sustained burst of >400 writes/sec for the entire
// warmup, which is a bulk import racing startup rather than normal traffic.
// When it happens we refuse to publish memdb at all (see the overflow branch in
// memSync) rather than publish a snapshot we know is incomplete.
//
// A var, not a const, so tests can lower it and exercise the overflow path.
var memPendingOpCap = 50000

// memPendingState tracks what memSync should do with a write-through.
type memPendingState int

const (
	// memPendingInactive: no warmup is pending. memSync applies directly to the
	// published memdb, or drops the write when memdb is disabled/failed (Pebble
	// stays authoritative and every read falls back to it).
	memPendingInactive memPendingState = iota
	// memPendingBuffering: warmup is in flight and WILL publish. memSync records
	// write-throughs for replay. Implies memPtr == nil.
	memPendingBuffering
	// memPendingAbandoned: buffering gave up (overflow, or Reset wiped the
	// keyspace out from under the in-flight scan). The warmup goroutine must NOT
	// publish its MemStore.
	memPendingAbandoned
)

// memPendingBuffer holds write-throughs recorded during the warmup window.
// mu guards every field AND serialises the publish, which is what closes the
// window — see the ordering argument above.
type memPendingBuffer struct {
	mu       sync.Mutex
	state    memPendingState
	ops      []memPendingOp
	// overflow distinguishes the two reasons a warmup gets abandoned: buffer
	// overflow (a fault — the snapshot provably lacks writes) versus Reset
	// superseding the snapshot (routine). publishWarmMemStore logs accordingly.
	overflow bool
}

// memPendingOp is one deferred write-through: the same closure memSync would
// have run against a live memdb, plus its op name for logging.
type memPendingOp struct {
	op string
	fn func(txn memTxn) error
}

// beginMemWarmupBuffering arms the buffer.
//
// MUST be called synchronously from NewPebbleStore BEFORE the warmup goroutine
// is launched. NewPebbleStore hands the store to its caller the moment the
// goroutine starts, so if arming happened inside the goroutine, a write that
// arrived first would see memPendingInactive + a nil memPtr and be dropped —
// the original bug, reintroduced with extra steps.
func (p *PebbleStore) beginMemWarmupBuffering() {
	p.memPending.mu.Lock()
	defer p.memPending.mu.Unlock()
	p.memPending.state = memPendingBuffering
	p.memPending.ops = nil
	p.memPending.overflow = false
}

// endMemWarmupBuffering disarms the buffer without publishing anything.
//
// Deferred by the warmup goroutine so it runs on EVERY exit path — warmup
// error, Close() cancelling the context mid-scan, or a successful publish
// (where it is a no-op because publishWarmMemStore already disarmed). Without
// it, a cancelled warmup would leave the store buffering forever into a slice
// nobody ever drains.
//
// Afterwards memSync goes back to apply-or-drop. When nothing was published
// memPtr is still nil, so write-throughs are dropped again — which is correct:
// with no memdb published, every read goes to Pebble.
func (p *PebbleStore) endMemWarmupBuffering() {
	p.memPending.mu.Lock()
	defer p.memPending.mu.Unlock()
	if p.memPending.state == memPendingBuffering && len(p.memPending.ops) > 0 {
		slog.Warn("memdb warmup: discarding buffered write-throughs, memdb will not be published (reads stay on Pebble)",
			"buffered_ops", len(p.memPending.ops))
	}
	p.memPending.state = memPendingInactive
	p.memPending.ops = nil
}

// replaceMemStoreAfterReset installs m (which may be nil) as the live memdb and
// cancels any warmup still in flight, as one atomic step.
//
// Used by Reset(), which wipes the Pebble keyspace. A warmup that started before
// the wipe holds a snapshot of the deleted data; letting it publish would put
// rows back that no longer exist in Pebble — the same "memdb disagrees with
// Pebble" failure the pending buffer prevents, from the other direction.
//
// The abandon and the pointer swap share one critical section so a write-through
// racing Reset cannot observe the in-between state (buffering already cancelled
// but the new MemStore not yet installed) and be dropped.
func (p *PebbleStore) replaceMemStoreAfterReset(m *MemStore) {
	p.memPending.mu.Lock()
	defer p.memPending.mu.Unlock()
	if p.memPending.state == memPendingBuffering {
		slog.Info("memdb warmup: abandoning in-flight warmup, its snapshot predates the reset",
			"buffered_ops", len(p.memPending.ops))
		p.memPending.state = memPendingAbandoned
	}
	p.memPending.ops = nil
	p.memPtr.Store(m)
}

// publishWarmMemStore replays every buffered write-through into m and publishes
// m as the live memdb. Reports whether it published.
//
// The replay and the memPtr.Store happen in ONE critical section on purpose.
// Any writer racing this either recorded its write before we took the lock (so
// it is in the replay) or blocks until after memPtr is visible (so it applies
// directly). Splitting the lock would reopen exactly the window this fixes.
//
// Holding the lock across the replay is a bounded exclusive step: at most
// memPendingOpCap in-memory mutations plus the Pebble point-reads a few of the
// closures do. None of those closures re-enter memSync — they only call
// p.db.Get / prefix iterators (GetBookAuthors, GetBookNarrators,
// loadBookFilesForBookID) — so there is no self-deadlock.
func (p *PebbleStore) publishWarmMemStore(m *MemStore) bool {
	p.memPending.mu.Lock()
	defer p.memPending.mu.Unlock()

	if p.memPending.state == memPendingAbandoned {
		if p.memPending.overflow {
			// The buffer overflowed, so this snapshot provably lacks writes that
			// succeeded in Pebble. Publishing it would be the original bug.
			slog.Error("memdb warmup: refusing to publish an incomplete memdb; reads stay on Pebble (slower but correct) until the service is restarted")
		} else {
			// Reset superseded this snapshot and already installed a fresh empty
			// MemStore. Routine, not a fault.
			slog.Info("memdb warmup: discarding warmup snapshot superseded by Reset")
		}
		p.memPending.state = memPendingInactive
		p.memPending.ops = nil
		return false
	}

	replayed := 0
	for _, po := range p.memPending.ops {
		applyMemSync(m, po.op, po.fn)
		replayed++
	}

	// Publish, then leave the buffering state — atomically with respect to any
	// writer, because both happen under mu.
	p.memPtr.Store(m)
	p.memPending.state = memPendingInactive
	p.memPending.ops = nil

	if replayed > 0 {
		slog.Info("memdb warmup: replayed write-throughs that arrived during warmup",
			"replayed_ops", replayed)
	}
	return true
}
