// file: internal/database/collection_perviewer.go
// version: 1.1.1
// guid: 7c2e5a91-3d48-4b06-8f1a-e4b9d0c63a57
// last-edited: 2026-10-26

package database

import "sync/atomic"

// A dynamic collection whose query uses a per-user field (read_status,
// progress_pct, last_played) is evaluated for each viewer and stores NO member
// list: a stored one would be one user's listening state in a row every reader
// (and the ABS surface) can see. The store enforces that itself, in
// CreateCollection and UpdateCollection, so that no writer can put a list back
// by forgetting to clear it (the ABS PATCH handler read the row, edited its name
// and wrote the old list back with a fresh Version).
//
// The store cannot decide "does this query use a per-user field" itself: that is
// the query parser's job and internal/search imports this package, so importing
// it here would be a cycle. internal/search registers its fail-closed detector
// (QueryMayUsePerUserField: text that does not parse counts as per-user) from an
// init function, so every binary that links the search package (the server
// does) has it. A binary or test without it keeps the stored list as given;
// TestProductionStoreScrubsPerViewerMembers in internal/server and
// TestInit_RegistersThePerViewerClassifierWithTheStore in internal/search prove the
// registration, because an unset classifier would fail open silently.

var perViewerClassifier atomic.Pointer[func(query string) bool]

// SetCollectionPerViewerClassifier installs the detector the store uses to decide
// that a dynamic collection's query is per-viewer. It must be fail-closed (true
// for a query it cannot parse). Passing nil removes it.
func SetCollectionPerViewerClassifier(f func(query string) bool) {
	if f == nil {
		perViewerClassifier.Store(nil)
		return
	}
	perViewerClassifier.Store(&f)
}

// scrubPerViewerMembers empties col.MaterializedBookIDs when col is a dynamic
// collection whose query is per-viewer. It reports whether it removed anything.
func scrubPerViewerMembers(col *Collection) bool {
	if col == nil || col.Type != CollectionTypeDynamic || len(col.MaterializedBookIDs) == 0 {
		return false
	}
	f := perViewerClassifier.Load()
	if f == nil || !(*f)(col.Query) {
		return false
	}
	col.MaterializedBookIDs = nil
	col.MaterializedTotal = 0
	return true
}
