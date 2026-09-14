// file: internal/organizer/rename_path_failure.go
// version: 1.0.0
// guid: 3e8b1f52-9c47-4a06-b2d1-7f5c0e9a4d18
// last-edited: 2026-09-14

package organizer

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// RenamePathWriteFailurePrefix namespaces the durable records of a rename
// whose files MOVED ON DISK but whose new path could not be written to the
// database, even after retrying. Such a row points at a path that no longer
// exists; the maintenance.repoint-unrecorded-renames op reads these records
// and repoints the row. Stored in the `_system` preference keyspace, the same
// home as ApplyRenameFailurePrefix, and cleared by blanking the value.
const RenamePathWriteFailurePrefix = "rename_path_write_failure:"

// renamePathBookRowKey stands in for the book_file ID when the failed write
// was the book row's own FilePath rather than a book_file row.
const renamePathBookRowKey = "book"

// RenamePathWriteFailure is one {old path, new path} pair a rename moved on
// disk but could not record. BookFileID is empty when the failed write was the
// book row's FilePath (a single-file book, or a multi-file book's directory).
type RenamePathWriteFailure struct {
	BookID     string `json:"book_id"`
	BookFileID string `json:"book_file_id,omitempty"`
	OldPath    string `json:"old_path"`
	NewPath    string `json:"new_path"`
	// NewITunesPath is the itunes_path the rename computed for the new
	// location, so the repair writes the same row the rename meant to.
	NewITunesPath string `json:"new_itunes_path,omitempty"`
	Error         string `json:"error"`
	RecordedAt    string `json:"recorded_at"`
}

// RenamePathWriteFailureKey is the record's key. It includes the book_file ID:
// one rename can fail on several files of the same book, and a key by book ID
// alone would keep only the last of them.
func RenamePathWriteFailureKey(bookID, bookFileID string) string {
	if bookFileID == "" {
		bookFileID = renamePathBookRowKey
	}
	return RenamePathWriteFailurePrefix + bookID + ":" + bookFileID
}

// RecordRenamePathWriteFailure persists the record. It returns the write
// error so the caller can log it; the caller must still return the original
// path-write error either way.
func RecordRenamePathWriteFailure(store DurableSkipStore, f RenamePathWriteFailure) error {
	if store == nil {
		return fmt.Errorf("record rename path write failure: no store")
	}
	if strings.TrimSpace(f.BookID) == "" {
		return fmt.Errorf("record rename path write failure: empty book id")
	}
	if f.RecordedAt == "" {
		f.RecordedAt = time.Now().UTC().Format(time.RFC3339)
	}
	blob, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("encode rename path write failure: %w", err)
	}
	return store.SetUserPreferenceForUser("_system", RenamePathWriteFailureKey(f.BookID, f.BookFileID), string(blob))
}

// ClearRenamePathWriteFailure blanks a record (the keyspace's clear
// convention); readers treat a blank value as no record.
func ClearRenamePathWriteFailure(store DurableSkipStore, bookID, bookFileID string) error {
	if store == nil {
		return fmt.Errorf("clear rename path write failure: no store")
	}
	return store.SetUserPreferenceForUser("_system", RenamePathWriteFailureKey(bookID, bookFileID), "")
}
