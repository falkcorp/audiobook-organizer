// file: internal/database/book_summary_filter_match.go
// version: 1.0.0
// guid: 9f26c031-89cd-43cf-893a-9631397d83da
// last-edited: 2026-09-19

package database

import "strings"

// bookMatchesSummaryFilter reports whether one book row satisfies every
// predicate on f: primary version, deletion state, quarantine, library state,
// review status, the id restriction and the caller-supplied Predicate. It does
// NOT look at SortBy/SortAscending; ordering belongs to the caller.
//
// It is the Pebble summary walk's predicate (walkFilteredBooksPebble), extracted
// so the filtered search (SearchBooksFiltered / MemStore.SearchBookIDsFiltered)
// applies EXACTLY the visibility rule the library list applies. A search that
// filtered differently from /items is how the ABS search surface came to serve
// merge losers and hidden copies whose ids the item route then redirected to a
// different book (item-6 B6, 2026-09-19).
//
// Predicates must only read fields memdb does not strip (see
// stripBookForMemdb); the memdb search passes its stripped rows here.
func bookMatchesSummaryFilter(book *Book, f BookSummaryFilter) bool {
	if f.RestrictToIDs != nil {
		if _, ok := f.RestrictToIDs[book.ID]; !ok {
			return false
		}
	}
	if f.IsPrimaryVersion != nil {
		// A nil IsPrimaryVersion on the row counts as primary, matching memdb
		// and the historical service behavior.
		eff := book.IsPrimaryVersion == nil || *book.IsPrimaryVersion
		if eff != *f.IsPrimaryVersion {
			return false
		}
	}
	// By default marked-for-deletion rows are dropped; an explicit
	// f.MarkedForDeletion turns this into an equality test.
	if !includeByDeletionState(bookIsSoftDeleted(book), f.MarkedForDeletion) {
		return false
	}
	if f.ExcludeQuarantined && book.QuarantinedAt != nil {
		return false
	}
	if f.LibraryState != "" {
		ls := ""
		if book.LibraryState != nil {
			ls = *book.LibraryState
		}
		if ls != f.LibraryState {
			return false
		}
	}
	if f.ReviewStatus != "" {
		rs := ""
		if book.MetadataReviewStatus != nil {
			rs = *book.MetadataReviewStatus
		}
		if !strings.EqualFold(rs, f.ReviewStatus) {
			return false
		}
	}
	if f.Predicate != nil && !f.Predicate(book) {
		return false
	}
	return true
}
