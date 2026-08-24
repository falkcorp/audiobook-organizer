// file: internal/database/series_bookref.go
// version: 1.5.0
// guid: 3b9d7c41-5e02-4a86-9f13-6c8ad20b47e5
// last-edited: 2026-08-24

package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/cockroachdb/pebble/v2"
)

// Unfiltered series reference counting, for DELETION decisions.
//
// WHY THIS EXISTS SEPARATELY FROM GetAllSeriesBookCounts
//
// GetAllSeriesBookCounts (and GetBooksBySeriesIDCore) skip two categories of
// book:
//
//	if b.MarkedForDeletion != nil && *b.MarkedForDeletion { continue }
//	if b.IsPrimaryVersion  != nil && !*b.IsPrimaryVersion  { continue }
//
// That is CORRECT for a display count — the trash and secondary versions must
// not inflate a badge. It is WRONG as an existence test for deletion, and
// executeSeriesPrune used it as exactly that: a series whose books are all
// trashed, or all non-primary, counted 0 and was deleted while those books
// stayed on disk pointing at a series ID that no longer resolves.
//
// Measured on production 2026-08-14, before this fix: 21,190 distinct series
// IDs were referenced by books but only 14,626 series rows existed — 6,893
// phantom IDs held by 13,322 live books plus 702 trashed ones, all rendering
// with no series at all. The op runs in the NIGHTLY maintenance window
// (scheduler task "series_prune" -> dedup.series-prune), so the damage
// accumulated a night at a time.
//
// These counters therefore apply NO filters. Every book row that names a
// series counts, whatever its deletion or primary-version state. "Is anything
// still pointing at this row?" is a different question from "how many books
// should I show the user", and conflating them is what caused the damage.

// SeriesBookRefStore is a narrow capability interface, deliberately kept OUT of
// database.Store — same rationale as BookSigMigrateStore: widening Store forces
// every implementation and generated mock to grow with it. Reach it through
// AsSeriesBookRefStore, which looks through the indexedStore decorator.
type SeriesBookRefStore interface {
	// GetAllSeriesBookRefCounts returns seriesID -> number of book rows that
	// reference it, counting trashed and non-primary books. A series absent
	// from the map is referenced by NOTHING and is safe to delete.
	GetAllSeriesBookRefCounts() (map[int]int, error)
}

// AsSeriesBookRefStore returns s as a SeriesBookRefStore, or nil if the backing
// store cannot answer the unfiltered question. Callers MUST nil-check and MUST
// fail rather than falling back to the filtered counter — that fallback is the
// bug this file exists to remove, and it would be silent.
func AsSeriesBookRefStore(s any) SeriesBookRefStore {
	if s == nil {
		return nil
	}
	if rs, ok := AsCapability[SeriesBookRefStore](s); ok {
		return rs
	}
	return nil
}

// GetAllSeriesBookRefCounts counts every book in the memdb that names a series,
// with NO deletion or primary-version filtering. Contrast
// GetAllSeriesBookCounts directly above, which scans only the
// memIdxIsPrimaryVersion=true index and skips trashed rows.
// It REFUSES rather than answering when the books table is known to be missing
// rows. memdb is a lossy projection — warmup drops rows it cannot decode or
// cannot insert — and a short scan here returns a map in which a still-
// referenced series is simply absent, which every caller reads as "safe to
// delete". See memdb_integrity.go.
func (m *MemStore) GetAllSeriesBookRefCounts() (map[int]int, error) {
	if err := m.requireTablesComplete("series reference count", memTableBooks); err != nil {
		return nil, err
	}

	txn := m.db.Txn(false)
	defer txn.Abort()

	iter, err := txn.Get(memTableBooks, memIdxID)
	if err != nil {
		return nil, fmt.Errorf("memdb books scan: %w", err)
	}
	out := make(map[int]int)
	for obj := iter.Next(); obj != nil; obj = iter.Next() {
		b := obj.(*Book)
		if b.SeriesID == nil {
			continue
		}
		out[*b.SeriesID]++
	}
	return out, nil
}

// GetAllSeriesBookRefCounts prefers the memdb when it is warm (the prod
// default) and otherwise scans Pebble directly.
//
// When the memdb refuses because its books table is known-incomplete, this
// falls through to the Pebble scan rather than propagating the refusal. Pebble
// is the source of truth and its scan is hardened to abort on an undecodable
// row, so the fall-through yields a CORRECT answer where the refusal would only
// have yielded a safe one — the nightly prune keeps working instead of stalling
// until the next restart.
//
// The cost is NOT once-off. lostRows is sticky for the life of the process --
// only publishLostRows and Reset clear it, and nothing re-warms in steady state
// -- so once anything taints the store, EVERY call takes the full Pebble scan
// until restart. No caller counts inside a loop (both handler sites and all
// three job sites build the map once per operation), but the handler sites are
// per-request. Still the right trade against deleting a referenced series; just
// a standing cost rather than a rare blip.
//
// Also note the trigger is broader than "warmup lost a books row": a runtime
// memSync failure is attributed to memTableUnknown, which taints every table.
//
// Any other error is propagated unchanged. Falling back to a full scan on an
// unrecognized failure would be guessing at its cause.
func (p *PebbleStore) GetAllSeriesBookRefCounts() (map[int]int, error) {
	// Loaded ONCE. Reset can swap memPtr underneath us, and reading it three
	// times could report a refusal from one MemStore next to the (empty) loss
	// map of its freshly-reset replacement -- a log line contradicting itself.
	if m := p.mem(); p.UseMemDB && m != nil {
		counts, err := m.GetAllSeriesBookRefCounts()
		if err == nil {
			return counts, nil
		}
		if !errors.Is(err, ErrMemdbIncomplete) {
			return nil, err
		}
		// Error, not Warn: this does not clear without a restart, and until then
		// every OTHER memdb reader is served from the same known-short projection
		// with no guard at all. This counter is the only reader that notices.
		slog.Error("series ref count: memdb is missing rows and will stay short until restart; falling through to the authoritative Pebble scan",
			"error", err, "lost_rows", m.LostRows())
	}
	return p.getAllSeriesBookRefCountsPebble()
}

// getAllSeriesBookRefCountsPebble mirrors GetAllSeriesBookCounts_Pebble's key
// range and row-shape guard, minus the IsPrimaryVersion filter, and without
// skipping trashed rows.
func (p *PebbleStore) getAllSeriesBookRefCountsPebble() (map[int]int, error) {
	counts := make(map[int]int)
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return nil, err
	}
	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		if !strings.HasPrefix(key, "book:") {
			continue
		}
		// Exactly one colon: skip the secondary indexes (book:path:, book:hash:,
		// book:versiongroup:) that share the prefix.
		if strings.Count(key, ":") != 1 {
			continue
		}
		var b Book
		if err := json.Unmarshal(iter.Value(), &b); err != nil {
			// FATAL, not skippable. A row we cannot decode may well carry a
			// series_id; dropping it undercounts, and undercounting is
			// fail-OPEN for every caller -- the delete proceeds and strands
			// the very row we could not read.
			_ = iter.Close()
			return nil, fmt.Errorf("series ref scan: undecodable book row %q: %w", key, err)
		}
		if b.SeriesID == nil {
			continue
		}
		counts[*b.SeriesID]++
	}

	// The loop above exits on end-of-range OR on an iteration error, and the
	// two are indistinguishable without this check. Returning a truncated map
	// with a nil error would answer "nothing else references anything" -- the
	// permissive answer -- to callers that delete on the strength of it. This
	// is the counter a delete guard consults, so it least of all may skip it.
	//
	// This used to add "every other Pebble scan in this package checks
	// iter.Error()". That was true when written and FALSE within nine days:
	// getBooksBySeriesIDFull became a second delete-guard-consulted scan on
	// 2026-08-24 and checked nothing. A survey of sibling code is a claim with
	// an expiry date, and nothing re-checks it -- so the reason to be strict
	// here is stated on its own terms above, and does not lean on what the
	// neighbours happen to do this week.
	if err := iter.Error(); err != nil {
		_ = iter.Close()
		return nil, fmt.Errorf("series ref scan truncated, refusing to answer from a partial count: %w", err)
	}
	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("series ref scan: closing iterator: %w", err)
	}
	return counts, nil
}

// SeriesRefCounts returns, per series ID, how many books reference it in ANY
// state — including books in the trash and non-primary (duplicate) versions.
// A series ID absent from the map is referenced by nothing and is the only
// thing safe to delete.
//
// It is the exported twin of the entities package's private seriesRefCounts,
// promoted here so the packages that cannot import internal/server (dedup and
// maintenance/jobs) reach the same guard instead of growing a third and fourth
// inline copy of it.
//
// It fails CLOSED. If the store cannot answer the unfiltered question, the
// caller must refuse to delete rather than fall back to the filtered count,
// because that fallback is precisely the bug: it deletes rows while reporting
// success. See the file comment above for the production damage that caused.
//
// Resolution goes through AsSeriesBookRefStore, and therefore through
// AsCapability, so it looks THROUGH the decorator chain. A bare type assertion
// against *PebbleStore is wrong in production, where the Bleve search-index
// decorator always wraps the store.
func SeriesRefCounts(store any) (map[int]int, error) {
	refCounter := AsSeriesBookRefStore(store)
	if refCounter == nil {
		return nil, fmt.Errorf("store cannot count unfiltered series references (got %T); "+
			"refusing to delete from a filtered count, which silently strands "+
			"books whose series is trashed or non-primary", store)
	}
	return refCounter.GetAllSeriesBookRefCounts()
}
