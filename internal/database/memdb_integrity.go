// file: internal/database/memdb_integrity.go
// version: 1.0.0
// guid: 7c1e4b90-2d63-4a85-9f27-e0b3a5c48d61
// last-edited: 2026-08-23

package database

import (
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"
)

// Known-incomplete tracking for the in-memory query layer.
//
// WHY THIS EXISTS
//
// MemStore is a LOSSY projection of Pebble. Warmup admits rows one at a time
// and, by design, does not abort the whole library load because one row will
// not decode or one insert violates an index rule — a single bad row must not
// leave the service with no memdb at all. The rows it drops are dropped
// SILENTLY: they are logged, and then the memdb is published as if complete.
//
// For almost every read that is the right trade. For the unfiltered reference
// counters (series_bookref.go, author_bookref.go) it is not, because those
// counters answer "is anything still pointing at this row?" and a delete guard
// acts on the answer. A dropped row makes the counter answer "referenced by
// nothing" when the truth is "could not read" — the permissive answer, handed
// to a caller that deletes on the strength of it. That is fail-OPEN, and it is
// the exact bug those two files were written to close on the Pebble path:
// PR #2782 hardened the Pebble scan to abort on an undecodable row, but
// UseMemDB is hardcoded true (pebble_store.go), so in production the hardened
// scan never ran and the memdb branch answered from a short map with a nil
// error.
//
// So MemStore carries a per-table "I know I am missing rows" flag. Warmup sets
// it at the two places a row can be lost (an undecodable Pebble value, and an
// insert the schema rejects). It is deliberately settable at RUNTIME, not just
// during warmup, because warmup is not the only way memdb loses a row:
// applyMemSync aborts its transaction when an index rule rejects a write and
// the write still succeeds in Pebble, which leaves the same divergence with no
// warmup involved. That path records here too rather than growing a second,
// parallel mechanism.
//
// The signal is deliberately coarse — "this table is known-incomplete" — not a
// list of which rows were lost. Knowing WHICH rows were lost would require
// keeping the rows we could not read, which is the thing we could not do.

// ErrMemdbIncomplete reports that a read was refused because the memdb table it
// would have scanned is known to be missing rows, so any count derived from it
// would be an undercount.
//
// Callers that can reach an authoritative source should check for this with
// errors.Is and fall through to it. Callers that cannot MUST fail closed —
// treating an undercount as "referenced by nothing" is what this error exists
// to prevent.
var ErrMemdbIncomplete = errors.New("memdb is known to be missing rows")

// recordLostRows notes that n rows destined for `table` never made it into
// memdb.
//
// For the list-valued phases (book_authors, book_narrators) one Pebble key
// holds a JSON array, so an undecodable value loses an unknown number of rows
// and warmup records 1. That undercounts the rows but not the CONDITION, which
// is all any caller reads: the question asked of this map is only ever "is it
// zero", so a conservative count can never turn a known-incomplete table into
// an apparently-complete one.
func (m *MemStore) recordLostRows(table string, n int) {
	if n <= 0 {
		return
	}
	m.lostMu.Lock()
	defer m.lostMu.Unlock()
	if m.lostRows == nil {
		m.lostRows = make(map[string]int)
	}
	m.lostRows[table] += n
}

// resetLostRows clears the known-incomplete state.
//
// Called at the START of a warmup, because a warmup rebuilds every table from
// Pebble — the authoritative source — and so supersedes whatever was known to
// be missing before it ran. Clearing at the END would instead erase the losses
// the warmup itself just recorded.
func (m *MemStore) resetLostRows() {
	m.lostMu.Lock()
	defer m.lostMu.Unlock()
	m.lostRows = nil
}

// LostRows returns a copy of the per-table count of rows known to be missing
// from this memdb. An empty map means no loss has been recorded — which is the
// normal case, and the one the reference counters require.
func (m *MemStore) LostRows() map[string]int {
	m.lostMu.RLock()
	defer m.lostMu.RUnlock()
	if len(m.lostRows) == 0 {
		return map[string]int{}
	}
	return maps.Clone(m.lostRows)
}

// requireTablesComplete returns ErrMemdbIncomplete if any of the named tables
// is known to be missing rows, naming `what` (the read being refused) and the
// affected tables so the log says which count could not be trusted and why.
//
// It returns nil when every named table is intact, which is the overwhelmingly
// common case — this must not become a guard that refuses everything, because a
// guard that always refuses turns the calling job into a no-op while looking
// like safety.
func (m *MemStore) requireTablesComplete(what string, tables ...string) error {
	m.lostMu.RLock()
	defer m.lostMu.RUnlock()
	if len(m.lostRows) == 0 {
		return nil
	}
	var hits []string
	for _, t := range tables {
		if n := m.lostRows[t]; n > 0 {
			hits = append(hits, fmt.Sprintf("%s=%d", t, n))
		}
	}
	if len(hits) == 0 {
		return nil
	}
	sort.Strings(hits)
	return fmt.Errorf("%s: refusing to answer from memdb, %w (%s); "+
		"a short count reads as \"referenced by nothing\" and would authorize a delete",
		what, ErrMemdbIncomplete, strings.Join(hits, " "))
}
