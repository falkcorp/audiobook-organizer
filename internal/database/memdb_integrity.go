// file: internal/database/memdb_integrity.go
// version: 1.2.0
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
// For almost every read that is the right trade. For the unfiltered series
// reference counter (series_bookref.go) it is not, because that counter answers
// "is anything still pointing at this row?" and a delete guard acts on the
// answer. A dropped row makes the counter answer "referenced by nothing" when
// the truth is "could not read" — the permissive answer, handed to a caller
// that deletes on the strength of it. That is fail-OPEN, and it is the exact
// bug that file was written to close on the Pebble path: PR #2782 hardened the
// Pebble scan to abort on an undecodable row, but UseMemDB is hardcoded true
// (pebble_store.go), so in production the hardened scan never ran and the memdb
// branch answered from a short map with a nil error.
//
// (An author-side twin of that counter is in flight on PR #2787 and will use
// this same mechanism. It does not exist in this package yet — do not go
// looking for author_bookref.go.)
//
// So MemStore carries a per-table "I know I am missing rows" flag, set at every
// place a row can go missing. There are THREE, and they are not all in warmup:
//
//  1. warmup cannot decode a Pebble value,
//  2. warmup's insert is rejected by a schema/index rule,
//  3. applyMemSync's transaction aborts at RUNTIME — a write that succeeded in
//     Pebble and was then rejected by a memdb index rule. No warmup involved,
//     and it leaves exactly the same divergence.
//
// (3) is the one that keeps the delete guard fail-open in steady state, and it
// is recorded against memTableUnknown, because applyMemSync is handed an opaque
// `fn func(txn memTxn) error` and genuinely cannot know which table the failed
// insert was for. See memTableUnknown for why that is the safe direction.
//
// The signal is deliberately coarse — "this table is known-incomplete" — not a
// list of which rows were lost. Knowing WHICH rows were lost would require
// keeping the rows we could not read, which is the thing we could not do.

// memTableUnknown records a lost row whose table could not be determined.
//
// It taints EVERY table, not none. applyMemSync runs a caller-supplied closure
// against the transaction, so when that closure fails there is no way to
// attribute the loss; the only two options are "assume nothing was lost" and
// "assume anything could have been". The first is the fail-open bug this file
// exists to close, so it is the second.
//
// The consequence of over-refusing here is bounded and cheap: PebbleStore falls
// through to the authoritative Pebble scan, so the answer stays CORRECT and
// only gets slower. The consequence of under-refusing is a deleted row.
//
// It also means a future op author cannot silently reopen the hole by adding a
// memSync op nobody remembered to map to a table — an unattributable failure
// is conservative by construction rather than by anyone's diligence.
//
// The angle brackets keep it from colliding with a real memdb table name.
const memTableUnknown = "<unknown>"

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

// publishLostRows installs the losses a warmup accumulated, replacing whatever
// was there before.
//
// Called immediately after the warmup's txn.Commit(), NOT at the start of the
// warmup, so the flag and the data it describes become visible together. A
// warmup rebuilds every table from the authoritative source, so replacing is
// correct: any divergence recorded earlier has just been resolved by the
// rebuild.
//
// Clearing at the START instead would look equivalent and is not. go-memdb is
// MVCC, so during an in-place re-warm readers keep seeing the OLD committed
// rows — which are still short — while the flag has already been cleared. That
// window is exactly the fail-open this file exists to close, reintroduced.
// `WarmFromPebble` advertises itself as "safe to re-run", so the window has to
// be closed by construction rather than by no caller currently re-running it.
// `unknownAtStart` is the memTableUnknown count observed when the warmup began.
// Unattributable RUNTIME losses that arrived while the scan was running are
// carried forward, because the scan reads an iterator snapshot and therefore
// did NOT rebuild whatever those losses dropped. Replacing the map wholesale
// would erase exactly the flag that case needs — the same "safe by
// construction, not by nobody currently doing it" standard this function's
// move-to-commit was made for.
func (m *MemStore) publishLostRows(lost map[string]int, unknownAtStart int) {
	m.lostMu.Lock()
	defer m.lostMu.Unlock()

	carried := 0
	if n := m.lostRows[memTableUnknown]; n > unknownAtStart {
		carried = n - unknownAtStart
	}

	if len(lost) == 0 && carried == 0 {
		m.lostRows = nil
		return
	}
	m.lostRows = maps.Clone(lost)
	if m.lostRows == nil {
		m.lostRows = make(map[string]int, 1)
	}
	if carried > 0 {
		m.lostRows[memTableUnknown] += carried
	}
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
	// An unattributable loss taints every table — see memTableUnknown. Checked
	// first so it reports even when none of the named tables has its own count.
	if n := m.lostRows[memTableUnknown]; n > 0 {
		hits = append(hits, fmt.Sprintf("%s=%d", memTableUnknown, n))
	}
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
