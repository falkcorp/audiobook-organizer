// file: internal/database/dataloss_regression_test.go
// version: 1.0.0
// guid: b7c8d9e0-1f2a-3b4c-5d6e-regression0001
// last-edited: 2026-07-13

package database

import (
	"testing"
)

// T6 — explicit named regression tests for data-loss scenarios that did NOT
// already have a dedicated test on main. Scenarios that ARE already covered
// elsewhere are intentionally NOT duplicated here:
//
//	soft-delete work/version-group exclusion .... pebble_store_index_consistency_test.go
//	DeleteBook dangling-row teardown ............ pebble_store_index_consistency_test.go
//	concurrent CreateNarrator id race .......... pebble_store_index_consistency_test.go
//	UpdateBook memdb-stripped/Author-Series wipe  pebble_book_preserve_test.go
//	UpdateBookFile fingerprint wipe ............. pebble_bookfile_preserve_test.go
//	tag/author/series colon collision .......... store_coverage_test.go
//	author-split write-back .................... plugins/maintenance/author_split_writeback_test.go
//	merge serialization ........................ merge/service_concurrent_test.go
//
// The gap filled below: a FilePath RENAME via UpdateBook must tear down the old
// book:path index row, or the stale row becomes a phantom pointer to the (now
// moved) book — a lookup by the old path would resolve to a book that no longer
// lives there, and after that book is deleted the row dangles.

// TestRegression_PathRenameLeavesNoStaleIndexRow reproduces the path-index
// teardown scenario: after UpdateBook changes FilePath, the OLD path must no
// longer resolve, the NEW path must resolve to the book, and no raw
// book:path:<old> row may remain.
func TestRegression_PathRenameLeavesNoStaleIndexRow(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	oldPath := "/lib/old/name.m4b"
	newPath := "/lib/new/name.m4b"

	created, err := store.CreateBook(&Book{Title: "Renamer", FilePath: oldPath})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}

	// Sanity: old path resolves before rename.
	if got, _ := store.GetBookByFilePath(oldPath); got == nil || got.ID != created.ID {
		t.Fatalf("pre-rename: old path did not resolve to the book")
	}

	upd := *created
	upd.FilePath = newPath
	if _, err := store.UpdateBook(created.ID, &upd); err != nil {
		t.Fatalf("UpdateBook rename: %v", err)
	}

	// Old path must NOT resolve; new path must resolve to the same book.
	if got, err := store.GetBookByFilePath(oldPath); err != nil {
		t.Fatalf("GetBookByFilePath(old): %v", err)
	} else if got != nil {
		t.Errorf("stale path index: old path %q still resolves to book %s after rename", oldPath, got.ID)
	}
	if got, err := store.GetBookByFilePath(newPath); err != nil || got == nil || got.ID != created.ID {
		t.Errorf("new path %q did not resolve to book %s: got=%v err=%v", newPath, created.ID, got, err)
	}

	// Raw: no book:path:<oldPath> row may remain.
	ps := store.(*PebbleStore)
	if _, closer, err := ps.db.Get([]byte("book:path:" + oldPath)); err == nil {
		closer.Close()
		t.Errorf("raw book:path:%s row still present after rename (dangling)", oldPath)
	}

	// The whole store must still satisfy every consistency invariant.
	assertStoreInvariants(t, store)
}
