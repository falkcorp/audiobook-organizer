// file: internal/books/store/diagnostics_types.go
// version: 1.0.1
// guid: 7f8a9b0c-1d2e-3f4a-5b6c-7d8e9f0a1b2d
// last-edited: 2026-11-09

// Package database — types previously defined in sqlite_store_util.go that
// are referenced by the diagnostics handler. Kept here for API compatibility
// even though the SQLite backend was removed in fable5 TASK-022.
// SQLiteTableStat will be replaced with a PebbleDB equivalent in a future PR.

package store

// SQLiteTableStat held row counts per SQLite table.
//
// Deprecated: The SQLite backend no longer exists. This type is kept for
// JSON/API compatibility in the db-health endpoint response. It will be
// replaced by a PebbleDB key-count table in a future cleanup PR.
type SQLiteTableStat struct {
	Name     string `json:"name"`
	RowCount int64  `json:"row_count"`
}

// BookPathPrefix holds a top-level path prefix and the count of books under it.
// Used by the DB health diagnostics endpoint.
type BookPathPrefix struct {
	Prefix    string `json:"prefix"`
	BookCount int64  `json:"book_count"`
}
