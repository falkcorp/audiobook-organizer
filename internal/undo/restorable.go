// file: internal/undo/restorable.go
// version: 1.1.0
// guid: 6c1f0e9a-4b27-4d3e-9a58-e2b7c41d0f93
// last-edited: 2026-09-12

package undo

import (
	"fmt"
	"reflect"
	"strconv"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// This file is the single classifier for "can POST /operations/:id/revert
// reverse this change row?". Two callers must agree on the answer:
//
//   - audiobooks.RevertService.RevertOperation, which does the reversal and
//     marks only restored rows reverted;
//   - PreflightUndoConflicts, whose report the UI turns into the "Undo N
//     change(s)?" confirmation before it calls that endpoint.
//
// When they disagreed, the preflight counted 1,742 author_delete rows as safe
// and the revert then refused every one of them.
//
// Rows that are NOT restorable are ledger records of a step with no automatic
// reversal:
//
//   - author_delete: maintenance.purge-empty-authors, author-duplicate-merge
//     (journalAuthorMerge) and the API author merge (server/entities_ops.go).
//   - narrator_delete: maintenance.purge-empty-narrators.
//   - series_delete / series_merge: series dedup and the duplicates-handler
//     series merge. Nothing can recreate a deleted series row under its old
//     id (CreateSeries mints a new one).
//   - metadata_update on a field outside revertableBookFields, e.g. the
//     author_id rows maintenance.author-duplicate-merge writes when it
//     re-points a book at the canonical author. Two fields are left out on
//     purpose because a field write cannot reverse what the row records:
//   - version_group_id (organizer version-copy path): the row always says
//     OldValue "" even when the book had already joined that group, so a
//     restore would pull a book out of a group organize never put it in; and
//     the same write also demoted the book (IsPrimaryVersion=false,
//     LibraryState=organized_source) and created the organized copy, none of
//     which the row records or a field write undoes.
//   - series_name (dedup.MergeSeries rename): it renames a Series entity, not
//     a book, and the row carries no series id, so there is no way to tell
//     which series to rename back.
//   - any change type the revert engine has no case for (db_update and
//     dir_create are reversed only by RunUndoOperation, not by the revert
//     endpoint).

// revertableBookFields maps a metadata_update field name to the Book struct
// field the revert engine restores it into by reflection. Every value must name
// a string, *string or *int field of database.Book; RestoreBookField handles
// exactly those three shapes, and TestRevertableBookFields_* hold the map to
// them.
var revertableBookFields = map[string]string{
	"title":                  "Title",
	"narrator":               "Narrator",
	"edition":                "Edition",
	"language":               "Language",
	"publisher":              "Publisher",
	"isbn10":                 "ISBN10",
	"isbn13":                 "ISBN13",
	"asin":                   "ASIN",
	"cover_url":              "CoverURL",
	"library_state":          "LibraryState",
	"open_library_id":        "OpenLibraryID",
	"hardcover_id":           "HardcoverID",
	"google_books_id":        "GoogleBooksID",
	"metadata_review_status": "MetadataReviewStatus",
	// Written by dedup.DedupSeries / dedup.MergeSeries (OldValue is the
	// book's series id before it was repointed at the canonical series, ""
	// when it had none) and maintenance.series-phantom-repair (OldValue is the
	// dangling id it cleared or repointed). The restored id must still name a
	// series row: see CheckRestoreReferent.
	"series_id": "SeriesID",
}

// IsRevertableBookField reports whether a metadata_update row for field names
// a Book field the revert engine can restore.
func IsRevertableBookField(field string) bool {
	_, ok := revertableBookFields[field]
	return ok
}

// RestoreBookField writes a metadata_update row's OldValue back into book.
// OldValue "" restores the field's empty state: nil for a pointer field, ""
// for a string field. A non-empty OldValue for an *int field must be a
// decimal integer; anything else is an error and book is left untouched, so
// the row is counted Failed rather than marked reverted.
func RestoreBookField(book *database.Book, field, oldValue string) error {
	name, ok := revertableBookFields[field]
	if !ok {
		return fmt.Errorf("unknown metadata field: %s", field)
	}
	f := reflect.ValueOf(book).Elem().FieldByName(name)
	if !f.IsValid() {
		return fmt.Errorf("invalid struct field: %s", name)
	}
	if f.Kind() == reflect.String {
		f.SetString(oldValue)
		return nil
	}
	if f.Kind() != reflect.Pointer {
		return fmt.Errorf("field %s: unsupported kind %s", name, f.Kind())
	}
	if oldValue == "" {
		f.Set(reflect.Zero(f.Type()))
		return nil
	}
	val := reflect.New(f.Type().Elem())
	switch val.Elem().Kind() {
	case reflect.String:
		val.Elem().SetString(oldValue)
	case reflect.Int:
		n, err := strconv.Atoi(oldValue)
		if err != nil {
			return fmt.Errorf("field %s: old value %q is not an integer id", field, oldValue)
		}
		val.Elem().SetInt(int64(n))
	default:
		return fmt.Errorf("field %s: unsupported kind *%s", name, val.Elem().Kind())
	}
	f.Set(val)
	return nil
}

// SeriesGetter is the one read CheckRestoreReferent needs.
type SeriesGetter interface {
	GetSeriesByID(id int) (*database.Series, error)
}

// CheckRestoreReferent returns an error when restoring c would point a book at
// a row that no longer exists. Today that is series_id: series dedup deletes
// the merged-from series in the same operation (a record-only series_delete
// row), and series-phantom-repair's old value is a dangling id by definition,
// so writing either back would create exactly the dangling reference
// series-phantom-repair exists to clean up. The revert refuses such a row
// (Failed, left unmarked) and the preflight reports it as a conflict; both call
// this function so they agree.
//
// Rows for any other field, and a series_id row restoring "no series", pass.
func CheckRestoreReferent(store SeriesGetter, c *database.OperationChange) error {
	if c.ChangeType != "metadata_update" || c.FieldName != "series_id" || c.OldValue == "" {
		return nil
	}
	id, err := strconv.Atoi(c.OldValue)
	if err != nil {
		return fmt.Errorf("series_id old value %q is not an integer id", c.OldValue)
	}
	s, err := store.GetSeriesByID(id)
	if err != nil {
		return fmt.Errorf("look up series %d: %w", id, err)
	}
	if s == nil {
		return fmt.Errorf("series %d no longer exists; restoring it would leave the book pointing at a deleted series", id)
	}
	return nil
}

// NotRestorableLabel returns "" when the revert engine can reverse c, and
// otherwise the label c is counted under in not-restorable totals: the change
// type, or "metadata_update:<field>" for a metadata row on a field the engine
// cannot restore.
func NotRestorableLabel(c *database.OperationChange) string {
	switch c.ChangeType {
	case "file_move", "organize_rename", "tag_write",
		"organize_failed", "organize_skipped", "organize_summary":
		return ""
	case "metadata_update":
		if IsRevertableBookField(c.FieldName) {
			return ""
		}
		field := c.FieldName
		if field == "" {
			field = "(no field)"
		}
		return "metadata_update:" + field
	}
	return c.ChangeType
}

// IsRestorable reports whether the revert engine can reverse c.
func IsRestorable(c *database.OperationChange) bool {
	return NotRestorableLabel(c) == ""
}
