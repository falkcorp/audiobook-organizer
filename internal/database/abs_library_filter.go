// file: internal/database/abs_library_filter.go
// version: 1.0.0
// guid: b7e8dfea-f143-4f43-86c6-b41fa3542dec
// last-edited: 2026-09-25

package database

// ABSLibraryFilter is the row predicate that defines which books the
// Audiobookshelf surface lists: the primary version of each group, whose
// library_state is "organized", that is neither soft-deleted nor quarantined.
//
// It lives here, not in the ABS handler, so that a maintenance op repairing
// "books ABS shows" selects exactly the rows ABS lists. A second hand-written
// copy of this rule could drift from the handler (for example on how a nil
// IsPrimaryVersion is read, which bookMatchesSummaryFilter treats as primary),
// and the op would then repair a different set from the one the owner sees.
// Presentation fields (sort, paging) are the caller's to add.
func ABSLibraryFilter() BookSummaryFilter {
	primary := true
	return BookSummaryFilter{
		IsPrimaryVersion:   &primary,
		LibraryState:       "organized",
		ExcludeQuarantined: true,
	}
}

// Matches reports whether one book row satisfies every predicate on f. It is
// the same test the summary walk and the filtered search apply, exported for
// callers that already hold the row.
func (f BookSummaryFilter) Matches(b *Book) bool {
	if b == nil {
		return false
	}
	return bookMatchesSummaryFilter(b, f)
}
