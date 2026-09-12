// file: internal/database/pebble_store_published_years.go
// version: 1.1.0
// guid: 25de50ea-ea39-4e25-a304-1b95b87e1919
// last-edited: 2026-09-12

package database

import (
	"encoding/json"
	"sort"
)

// bookPublishedYear is the one place the "published year" of a book is defined:
// AudiobookReleaseYear when set, else PrintYear, and a zero year counts as
// unknown. The ABS /filterdata decade facet applied exactly this coalesce
// inline since it was written; both store paths below must agree with it, so
// it lives here rather than being re-derived in each.
func bookPublishedYear(b *Book) (int, bool) {
	year := b.AudiobookReleaseYear
	if year == nil {
		year = b.PrintYear
	}
	if year == nil || *year == 0 {
		return 0, false
	}
	return *year, true
}

// sortedYears flattens a distinct-year set into the sorted slice the interface
// promises. Never nil: an empty library is an empty facet, not a missing one.
func sortedYears(seen map[int]struct{}) []int {
	out := make([]int, 0, len(seen))
	for y := range seen {
		out = append(out, y)
	}
	sort.Ints(out)
	return out
}

// GetDistinctPublishedYears returns the sorted distinct non-zero published
// years across all non-deleted books (see BookSearchReader for the coalesce).
//
// Gated on UseMemDB AND publication exactly like GetAllBooksCore, so turning
// the flag off reaches the Pebble path. Cost: the memdb path is one pointer
// walk (see MemStore.GetDistinctPublishedYears); the Pebble fallback is the
// same whole-keyspace scan with a per-row json.Unmarshal that
// GetDistinctLanguages pays — ~5s at 68K rows measured 2026-08-25 — and is
// only ever taken while memdb is not yet warm.
func (p *PebbleStore) GetDistinctPublishedYears() ([]int, error) {
	if p.UseMemDB && p.mem() != nil {
		return p.mem().GetDistinctPublishedYears()
	}

	iter, err := newBookRowIter(p.db)
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	seen := map[int]struct{}{}
	for iter.First(); iter.Valid(); iter.Next() {
		var b Book
		if err := json.Unmarshal(iter.Value(), &b); err != nil {
			continue
		}
		if bookIsSoftDeleted(&b) {
			continue
		}
		if y, ok := bookPublishedYear(&b); ok {
			seen[y] = struct{}{}
		}
	}
	return sortedYears(seen), nil
}
