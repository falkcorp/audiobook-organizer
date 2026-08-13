// file: internal/database/memdb_store.go
// version: 1.1.0
// guid: a1b2c3d4-mema-aaaa-aaaa-000000000003
// last-edited: 2026-08-11

package database

import (
	"fmt"
	"sync"
	"time"

	"github.com/hashicorp/go-memdb"
)

// MemStore is an in-memory query/index layer over PebbleDB. PebbleDB remains
// the source of truth and durable store; MemStore is rebuilt from Pebble on
// startup and kept in sync via write-through.
//
// Reads use snapshot transactions (no locking, MVCC via immutable radix
// trees). Writes are serialized by go-memdb's single-writer model but never
// block readers.
type MemStore struct {
	db *memdb.MemDB

	// lastWarmCounts records the per-table row counts published by the most
	// recent WarmFromPebble. These are ROW counts (rows actually inserted into
	// memdb), not Pebble key counts — see warmIter for why the distinction
	// matters and what it cost when the two were conflated.
	warmCountsMu    sync.RWMutex
	lastWarmCounts  map[string]int
	lastWarmScanned map[string]int
	// lastWarmDurations records how long each per-table prefix scan took in
	// the most recent WarmFromPebble, plus a "commit" entry for txn.Commit.
	// Retained rather than only logged so the split is assertable in a test
	// and readable from a running process, not just recoverable by grepping
	// startup logs after the fact.
	lastWarmDurations map[string]time.Duration
}

// WarmupPhaseKeyCommit is the lastWarmDurations key holding the txn.Commit
// cost. It is not a table, so it cannot collide with one.
const WarmupPhaseKeyCommit = "commit"

// LastWarmupCounts returns the per-table row counts from the most recent
// WarmFromPebble, and the per-table count of Pebble keys scanned to produce
// them. For prefixes shared with secondary indexes (book:, author:,
// book_file:) the two differ by a large factor: on production, the book:
// prefix holds ~7.5 keys per book row.
func (m *MemStore) LastWarmupCounts() (rows, scanned map[string]int) {
	m.warmCountsMu.RLock()
	defer m.warmCountsMu.RUnlock()
	rows = make(map[string]int, len(m.lastWarmCounts))
	for k, v := range m.lastWarmCounts {
		rows[k] = v
	}
	scanned = make(map[string]int, len(m.lastWarmScanned))
	for k, v := range m.lastWarmScanned {
		scanned[k] = v
	}
	return rows, scanned
}

// LastWarmupDurations returns how long each phase of the most recent
// WarmFromPebble took, keyed by table name, plus WarmupPhaseKeyCommit for the
// transaction commit.
//
// The sum of these is less than the warmup's reported total; the remainder is
// setup and teardown either side. A commit entry that dominates would mean the
// ten scans are not where the time goes, and that the fix is to stop holding
// one write transaction across all of them rather than to make the scans
// faster.
func (m *MemStore) LastWarmupDurations() map[string]time.Duration {
	m.warmCountsMu.RLock()
	defer m.warmCountsMu.RUnlock()
	out := make(map[string]time.Duration, len(m.lastWarmDurations))
	for k, v := range m.lastWarmDurations {
		out[k] = v
	}
	return out
}

// NewMemStore allocates an empty MemStore with the full schema applied.
// Call WarmFromPebble after construction to populate it from a PebbleStore.
func NewMemStore() (*MemStore, error) {
	db, err := memdb.NewMemDB(memdbSchema())
	if err != nil {
		return nil, fmt.Errorf("memdb: failed to build schema: %w", err)
	}
	return &MemStore{db: db}, nil
}

// Txn begins a transaction. Pass write=true for mutations.
// Always defer Abort(); call Commit() to publish writes.
func (m *MemStore) Txn(write bool) *memdb.Txn {
	return m.db.Txn(write)
}

// Snapshot returns a point-in-time snapshot view. Useful for long-running
// reads that should see a consistent state without holding back writers.
func (m *MemStore) Snapshot() *MemStore {
	return &MemStore{db: m.db.Snapshot()}
}
