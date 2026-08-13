// file: internal/database/memdb_store.go
// version: 1.3.0
// guid: a1b2c3d4-mema-aaaa-aaaa-000000000003
// last-edited: 2026-08-13

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
	// lastWarmBytes records how many bytes of Pebble VALUES each prefix scan
	// read, and lastWarmDiscarded how many of those bytes belonged to fields
	// the memdb projection throws away immediately after decoding them. See
	// LastWarmupBytes for why the pair is worth carrying.
	lastWarmBytes     map[string]int64
	lastWarmDiscarded map[string]int64
	// lastWarmDiscardedByField breaks lastWarmDiscarded down by which group of
	// fields the bytes belonged to, keyed by the DiscardField* constants.
	lastWarmDiscardedByField map[string]int64
}

// Keys for the per-field discarded-byte breakdown. These are field GROUPS, not
// individual struct fields: the unit of the decision they inform is "should
// this group move out of the row", and a group either moves together or not at
// all. AcoustIDSeg0..6 in particular are seven fields with one lifecycle.
//
// They are deliberately not the JSON tag names. A JSON tag is a wire-format
// detail that can change without the grouping changing, and these strings end
// up in production logs that get compared across releases.
const (
	DiscardFieldAcoustIDFingerprint    = "acoustid_fingerprint"
	DiscardFieldAcoustIDSegments       = "acoustid_seg0_6"
	DiscardFieldIntroTranscription     = "intro_transcription"
	DiscardFieldFingerprintDiagnostics = "fingerprint_diagnostics"
	DiscardFieldDescription            = "description_and_notes"
	DiscardFieldBookSignature          = "book_sig_v1_and_mask"
)

// LastWarmupDiscardedByField returns the discarded-byte totals split by field
// group, keyed by the DiscardField* constants.
//
// LastWarmupBytes answers "is a fix worth doing" (production 2026-08-13: 76% of
// the book_files phase). This answers "which fix", and the candidates are not
// equivalent in risk: moving AcoustIDFingerprint out of the row retires the
// write-back preserve-guards that exist because a bare memdb round-trip once
// wiped fingerprints in production, while moving IntroTranscription does not.
// Choosing the expensive option off an aggregate that might be dominated by the
// cheap one would repeat the error the aggregate just caught.
func (m *MemStore) LastWarmupDiscardedByField() map[string]int64 {
	m.warmCountsMu.RLock()
	defer m.warmCountsMu.RUnlock()
	out := make(map[string]int64, len(m.lastWarmDiscardedByField))
	for k, v := range m.lastWarmDiscardedByField {
		out[k] = v
	}
	return out
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

// LastWarmupBytes returns, per table, how many bytes of Pebble values the most
// recent WarmFromPebble read (`scanned`) and how many of those bytes were spent
// decoding fields that the memdb projection then discards (`discarded`).
//
// Why this pair and not just a duration: the per-phase timings shipped earlier
// established that book_files is 82% of a ~109 s warmup, and a CPU profile
// showed 61% of the phase inside pebble.Iterator.Next — almost all of it in
// loadDataBlock, meaning Pebble pulls a fresh sstable data block off disk for
// nearly every row. That is the signature of rows so large a block holds one or
// two of them, and BookFile.AcoustIDFingerprint is a []byte held INLINE in the
// row (~230 KB raw, ~307 KB once encoding/json base64-encodes it) that
// stripBookFileForMemdb nils out the instant it has been decoded.
//
// If that blob really is most of the bytes, the fix is to stop storing it in
// the row — which pays out in five places at once (pread, cgo block
// decompression, block alloc, JSON, base64) — and NOT to parallelize the scan,
// which divides the work without shrinking it. That is a large, data-loss-
// sensitive schema change, so the premise gets measured rather than inferred
// from a call graph. This repo has already shipped one extrapolated
// data-structure cost estimate that was off by several times.
//
// `discarded` is accounted in ENCODED bytes (base64 length for []byte fields),
// because `scanned` counts raw JSON value bytes; comparing a decoded length
// against an encoded total would understate the ratio by 4/3 and produce a
// number that looks precise and is wrong.
func (m *MemStore) LastWarmupBytes() (scanned, discarded map[string]int64) {
	m.warmCountsMu.RLock()
	defer m.warmCountsMu.RUnlock()
	scanned = make(map[string]int64, len(m.lastWarmBytes))
	for k, v := range m.lastWarmBytes {
		scanned[k] = v
	}
	discarded = make(map[string]int64, len(m.lastWarmDiscarded))
	for k, v := range m.lastWarmDiscarded {
		discarded[k] = v
	}
	return scanned, discarded
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
