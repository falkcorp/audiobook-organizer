// file: internal/server/handlers/entities/series_refcount.go
// version: 1.0.0
// guid: 8e2f5a19-7d64-4c03-b8a1-5f93c0e64b27
// last-edited: 2026-08-14

package entities

import (
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// seriesRefCounts returns, per series ID, how many books reference it in ANY
// state — including books in the trash and non-primary (duplicate) versions.
// A series ID absent from the map is referenced by nothing and is the only
// thing safe to delete.
//
// This exists because the obvious getter is the wrong question. Both series
// delete handlers used to guard with GetBooksBySeriesIDCore, which is the
// counter behind the number shown next to a series in the UI and therefore
// deliberately skips trashed and non-primary books. Those books still hold the
// series_id, so a series whose books were all trashed, or all alternate
// versions, counted as zero and was deleted out from under them. On production
// 2026-08-14 that had produced 6,893 series IDs referenced by 13,322 live books
// (+702 trashed), each rendering with no series and unrecoverable — the names
// live only in the deleted row.
//
// It fails CLOSED. If the store cannot answer the unfiltered question, the
// caller must refuse to delete rather than fall back to the filtered count,
// because that fallback is precisely the bug: it deletes rows while reporting
// success. Mirrors the guard executeSeriesPrune adopted in #2400.
func seriesRefCounts(store any) (map[int]int, error) {
	refCounter := database.AsSeriesBookRefStore(store)
	if refCounter == nil {
		return nil, fmt.Errorf("store cannot count unfiltered series references (got %T); "+
			"refusing to delete from a filtered count, which silently strands "+
			"books whose series is trashed or non-primary", store)
	}
	return refCounter.GetAllSeriesBookRefCounts()
}
