// file: internal/undo/restorable.go
// version: 1.0.0
// guid: 6c1f0e9a-4b27-4d3e-9a58-e2b7c41d0f93
// last-edited: 2026-09-12

package undo

import "github.com/falkcorp/audiobook-organizer/internal/database"

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
//   - metadata_update on a field outside revertableBookFields, e.g. the
//     author_id rows maintenance.author-duplicate-merge writes when it
//     re-points a book at the canonical author.
//   - any change type the revert engine has no case for (db_update and
//     dir_create are reversed only by RunUndoOperation, not by the revert
//     endpoint).

// revertableBookFields maps a metadata_update field name to the Book struct
// field the revert engine restores it into by reflection. Every value must name
// a string or *string field of database.Book.
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
}

// RevertableBookField returns the Book struct field name the revert engine
// restores a metadata_update row for field into, and whether it has one.
func RevertableBookField(field string) (string, bool) {
	name, ok := revertableBookFields[field]
	return name, ok
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
		if _, ok := revertableBookFields[c.FieldName]; ok {
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
