// file: internal/undo/current.go
// version: 1.0.0
// guid: 2e7b4c19-8d3a-4f60-9b15-c6a0e8d2f473
// last-edited: 2026-09-29

package undo

import (
	"strconv"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The compare-and-set checks the revert (audiobooks.RevertService) and the
// preflight (PreflightUndoConflicts) share for the change types a Repairs
// apply journals BEFORE it writes (repairs/writer_files.go, JOURNAL FIRST).
// Each answers, for the current state:
//
//   - nil: the target still holds what the operation wrote; revert it.
//   - ErrAlreadyRestored: the target already holds OldValue. Either the step
//     was journaled and its write never happened (a run cut off between the
//     two, or a write that failed), or something already put it back. There
//     is nothing to write, and the row is counted restored, not failed.
//   - a ReasonChangedSince refusal: the target holds something else; a later
//     change is left in place.
//
// NewValue is checked first, so a row whose OldValue equals its NewValue is
// reverted (a no-op), never mistaken for either answer.

// SoftDeleteStamp renders the marked_for_deletion_at a soft-delete writes, as
// the NewValue its book_soft_delete row journals. The revert compares the
// book's stamp against it (CheckSoftDeleteCurrent).
func SoftDeleteStamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// CheckSoftDeleteCurrent: a live book is already restored. A book_soft_delete
// row whose NewValue is a stamp (SoftDeleteStamp) is reverted only while the
// book's marked_for_deletion_at is that stamp, so a leftover row (the retire
// journaled it and never wrote it) cannot un-delete a book the user deleted
// later. A row with any other NewValue (the note fs-regroup-xml journals) keeps
// the earlier rule: any soft-deleted book is restored.
func CheckSoftDeleteCurrent(book *database.Book, c *database.OperationChange) error {
	if !book.IsSoftDeleted() {
		return ErrAlreadyRestored
	}
	stamp, err := time.Parse(time.RFC3339Nano, c.NewValue)
	if err != nil {
		return nil
	}
	if book.MarkedForDeletionAt == nil || !book.MarkedForDeletionAt.Equal(stamp) {
		return refuse(ReasonChangedSince, "book %s was deleted again since the operation (marked %v, not %s)",
			c.BookID, book.MarkedForDeletionAt, c.NewValue)
	}
	return nil
}

// CheckMergedIntoCurrent compares merged_into_book_id ("" is unset).
func CheckMergedIntoCurrent(book *database.Book, c *database.OperationChange) error {
	cur := ""
	if book.MergedIntoBookID != nil {
		cur = *book.MergedIntoBookID
	}
	return checkValue(c, cur, "merged_into of book "+c.BookID)
}

// CheckPathUpdateCurrent compares the book's file_path.
func CheckPathUpdateCurrent(book *database.Book, c *database.OperationChange) error {
	return checkValue(c, book.FilePath, "path of book "+c.BookID)
}

// CheckPrimaryDemoteCurrent: the demote wrote an explicit false. A flag that
// is not false but matches OldValue ("true" covers an unset flag too, as it
// reads as primary; "" is unset) is already restored.
func CheckPrimaryDemoteCurrent(book *database.Book, c *database.OperationChange) error {
	p := book.IsPrimaryVersion
	if p != nil && !*p {
		return nil
	}
	switch {
	case c.OldValue == "true", c.OldValue == "" && p == nil:
		return ErrAlreadyRestored
	}
	return refuse(ReasonChangedSince, "book %s was made primary again since the operation", c.BookID)
}

// CheckExternalIDOwnerCurrent compares the book an external id names now.
func CheckExternalIDOwnerCurrent(owner string, c *database.OperationChange) error {
	return checkValue(c, owner, "owner of "+c.FieldName)
}

// CheckTrackCurrent compares a book_file row's track number.
func CheckTrackCurrent(track int, c *database.OperationChange) error {
	return checkValue(c, strconv.Itoa(track), "track of "+c.FieldName)
}

// CheckRepointCurrent compares a book_file row's location, path and Missing
// flag only (SameFileLocation).
func CheckRepointCurrent(f *database.BookFile, c *database.OperationChange) error {
	set, err := DecodeBookFileLocation(c.NewValue)
	if err != nil {
		return refuse(ReasonOldValueUnparsable, "%v", err)
	}
	cur := LocationOf(f)
	if SameFileLocation(cur, set) {
		return nil
	}
	if was, err := DecodeBookFileLocation(c.OldValue); err == nil && SameFileLocation(cur, was) {
		return ErrAlreadyRestored
	}
	return refuse(ReasonChangedSince, "book_file %s is now at %q, not where the operation pointed it (%q)", f.ID, f.FilePath, set.Path)
}

// CheckReassignCurrent: the row is still on BookID (revert it), back on
// OldValue (already restored), or elsewhere (refused).
func CheckReassignCurrent(onTarget, onSource bool, c *database.OperationChange) error {
	switch {
	case onTarget:
		return nil
	case onSource:
		return ErrAlreadyRestored
	}
	return refuse(ReasonChangedSince, "%s is on neither book %s nor book %s", c.FieldName, c.BookID, c.OldValue)
}

func checkValue(c *database.OperationChange, cur, what string) error {
	switch cur {
	case c.NewValue:
		return nil
	case c.OldValue:
		return ErrAlreadyRestored
	}
	return refuse(ReasonChangedSince, "%s is %q, not the %q the operation set", what, cur, c.NewValue)
}
