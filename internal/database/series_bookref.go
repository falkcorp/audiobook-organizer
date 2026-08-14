// file: internal/database/series_bookref.go
// version: 1.0.0
// guid: 3b9d7c41-5e02-4a86-9f13-6c8ad20b47e5
// last-edited: 2026-08-14

package database

import (
	"encoding/json"
	"fmt"
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
func (m *MemStore) GetAllSeriesBookRefCounts() (map[int]int, error) {
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
func (p *PebbleStore) GetAllSeriesBookRefCounts() (map[int]int, error) {
	if p.UseMemDB && p.mem() != nil {
		return p.mem().GetAllSeriesBookRefCounts()
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
	defer iter.Close()

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
			continue
		}
		if b.SeriesID == nil {
			continue
		}
		counts[*b.SeriesID]++
	}
	return counts, nil
}
