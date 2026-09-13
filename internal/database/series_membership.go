// file: internal/database/series_membership.go
// version: 1.0.1
// guid: 5d0f3b8e-2a71-4c96-b4e3-8f1a6c29d7b0
// last-edited: 2026-09-13

package database

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// Bulk series membership, for repoint-then-delete loops (SERIES-MERGE-PERSERIES-SCAN-COST).
//
// GetBooksBySeriesIDAllVersions is the per-series getter every series merge and
// unlink path reads before deleting a series. Four maintenance loops called it
// once PER SERIES. Once the memdb is tainted (lostRows is sticky until restart)
// each of those calls is a full "book:" prefix Pebble scan, so the loops were
// O(series x books) on the nightly window. This file lets an operation read the
// same answer for every series it will touch in ONE scan, the way the ref-count
// callers already hoist GetAllSeriesBookRefCounts.
//
// PARITY IS THE CONTRACT. For every requested ID, the bulk answer is exactly
// what GetBooksBySeriesIDAllVersions(id) returns:
//   - non-primary versions INCLUDED (a merge must repoint them);
//   - soft-deleted books EXCLUDED. This is load-bearing, not a filter to "fix":
//     every caller guards its DeleteSeries with
//     refCounts[id] - moved > 0, where refCounts (SeriesRefCounts) counts trashed
//     rows and this set does not. That gap IS the guard. Returning trashed rows
//     here would get them repointed and counted in moved, zero the gap, and
//     delete a series trashed books still hold;
//   - each bucket in sortBooksInSeriesOrder order;
//   - the same memdb completeness guard and Pebble fall-through, fail-CLOSED on
//     a scan error.
// Pinned by TestGetBooksBySeriesIDsAllVersions_MatchesPerSeriesGetter.

// SeriesMembershipStore is a narrow capability interface, kept OUT of
// database.Store for the same reason as SeriesBookRefStore. Reach it through
// AsSeriesMembershipStore / SeriesMembershipAllVersions, which look through the
// indexedStore decorator (AsCapability) -- a bare type assertion misses in prod.
type SeriesMembershipStore interface {
	// GetBooksBySeriesIDsAllVersions returns seriesID -> the books
	// GetBooksBySeriesIDAllVersions(seriesID) would return, for every ID in
	// seriesIDs. An ID with no live books maps to an empty (possibly absent)
	// bucket. IDs not requested are not returned.
	GetBooksBySeriesIDsAllVersions(seriesIDs []int) (map[int][]BookCore, error)
}

// Compile-time: both concrete stores keep the bulk method's exact signature.
var (
	_ SeriesMembershipStore = (*MemStore)(nil)
	_ SeriesMembershipStore = (*PebbleStore)(nil)
)

// AsSeriesMembershipStore returns s as a SeriesMembershipStore, or nil.
func AsSeriesMembershipStore(s any) SeriesMembershipStore {
	if s == nil {
		return nil
	}
	if ms, ok := AsCapability[SeriesMembershipStore](s); ok {
		return ms
	}
	return nil
}

// SeriesMembershipAllVersions loads the complete membership of seriesIDs in one
// pass. It fails CLOSED: a store without the capability, or a failed scan, is
// an error the caller must abort on. An empty map on error would read as "every
// series is empty", and every caller deletes on the strength of that.
func SeriesMembershipAllVersions(store any, seriesIDs []int) (SeriesBooksMap, error) {
	ms := AsSeriesMembershipStore(store)
	if ms == nil {
		return nil, fmt.Errorf("store cannot load bulk series membership (got %T); "+
			"refusing to run a series merge without it", store)
	}
	m, err := ms.GetBooksBySeriesIDsAllVersions(seriesIDs)
	if err != nil {
		return nil, fmt.Errorf("bulk series membership: %w", err)
	}
	if m == nil {
		m = make(map[int][]BookCore)
	}
	return SeriesBooksMap(m), nil
}

// SeriesBooksMap is a hoisted seriesID -> books snapshot. It goes stale the
// moment a loop moves a book, so every loop that repoints or unlinks a book
// MUST call Move after the write succeeds. A later read of either series then
// sees what a fresh per-series read would have returned.
type SeriesBooksMap map[int][]BookCore

// Move records that bookID, previously in series from, now belongs to *to (or
// to no series when to is nil). Call it only after the write landed: a failed
// write leaves the book where it was, and the map must say so.
func (m SeriesBooksMap) Move(bookID string, from int, to *int) {
	if m == nil {
		return
	}
	books := m[from]
	for i := range books {
		if books[i].ID != bookID {
			continue
		}
		moved := books[i]
		// Copy, don't alias: the slice may share a backing array with a caller.
		rest := make([]BookCore, 0, len(books)-1)
		rest = append(rest, books[:i]...)
		rest = append(rest, books[i+1:]...)
		m[from] = rest
		if to != nil {
			id := *to
			moved.SeriesID = &id
			m[id] = append(m[id], moved)
		}
		return
	}
}

// seriesIDSet turns a request list into a lookup set, deduplicated.
func seriesIDSet(ids []int) map[int]struct{} {
	set := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}

// bucketBooksBySeries groups already-filtered books by series, sorts each
// bucket with the per-series ordering and projects to BookCore.
func bucketBooksBySeries(books []Book) map[int][]BookCore {
	grouped := make(map[int][]Book)
	for i := range books {
		grouped[*books[i].SeriesID] = append(grouped[*books[i].SeriesID], books[i])
	}
	out := make(map[int][]BookCore, len(grouped))
	for sid, bs := range grouped {
		sortBooksInSeriesOrder(bs)
		cores := make([]BookCore, len(bs))
		for i := range bs {
			cores[i] = bs[i].Core()
		}
		out[sid] = cores
	}
	return out
}

// GetBooksBySeriesIDsAllVersions is the bulk twin of
// MemStore.GetBooksBySeriesIDAllVersions, and refuses on the same condition.
// It walks the series index once per requested ID (an index seek, not a table
// scan), with the same filters as getBooksBySeriesID(id, 0, 0, false).
func (m *MemStore) GetBooksBySeriesIDsAllVersions(seriesIDs []int) (map[int][]BookCore, error) {
	if err := m.requireTablesComplete("books by series (merge path, bulk)", memTableBooks); err != nil {
		return nil, err
	}
	out := make(map[int][]BookCore, len(seriesIDs))
	for id := range seriesIDSet(seriesIDs) {
		cores, err := m.getBooksBySeriesID(id, 0, 0, false)
		if err != nil {
			return nil, err
		}
		out[id] = cores
	}
	return out, nil
}

var seriesMembershipLog = logger.New("series-membership")

// GetBooksBySeriesIDsAllVersions prefers the memdb and, when it refuses as
// incomplete, falls through to ONE authoritative Pebble scan for all requested
// series -- instead of the one-scan-per-series the per-series getter costs.
// Any other memdb error propagates unchanged.
func (p *PebbleStore) GetBooksBySeriesIDsAllVersions(seriesIDs []int) (map[int][]BookCore, error) {
	if m := p.mem(); p.UseMemDB && m != nil {
		out, err := m.GetBooksBySeriesIDsAllVersions(seriesIDs)
		if err == nil {
			return out, nil
		}
		if !errors.Is(err, ErrMemdbIncomplete) {
			return nil, err
		}
		seriesMembershipLog.Error("books by series (merge path, bulk): memdb is missing rows and will stay short until restart; "+
			"falling through to one authoritative Pebble scan for %d series (lost_rows=%v): %v",
			len(seriesIDs), m.LostRows(), err)
	}
	return p.getBooksBySeriesIDsFull(seriesIDs)
}

// getBooksBySeriesIDsFull is getBooksBySeriesIDFull(id, false) for a set of
// IDs in one pass. Same predicate, same fail-closed decode and scan errors.
func (p *PebbleStore) getBooksBySeriesIDsFull(seriesIDs []int) (map[int][]BookCore, error) {
	want := seriesIDSet(seriesIDs)
	var books []Book
	if err := forEachBookRow(p.db, func(rowID string, rowValue []byte) error {
		var book Book
		if err := json.Unmarshal(rowValue, &book); err != nil {
			return fmt.Errorf("bulk books by series scan: undecodable book row %q: %w", bookRowPrefix+rowID, err)
		}
		if book.SeriesID == nil {
			return nil
		}
		if _, ok := want[*book.SeriesID]; !ok {
			return nil
		}
		if bookIsSoftDeleted(&book) {
			return nil
		}
		books = append(books, book)
		return nil
	}); err != nil {
		return nil, err
	}
	out := bucketBooksBySeries(books)
	for id := range want {
		if _, ok := out[id]; !ok {
			out[id] = []BookCore{}
		}
	}
	return out, nil
}
