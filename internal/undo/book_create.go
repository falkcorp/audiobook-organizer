// file: internal/undo/book_create.go
// version: 1.1.0
// guid: 7f3a9c21-5e8b-4d06-a1c4-2b9e6d0f8a53
// last-edited: 2026-10-01

package undo

import (
	"fmt"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ChangeTypeRepairBookCreate: a Repairs fixer created the book BookID (the
// folder-books fixer gives each orphaned work of a retired folder-book its own
// book). It is journaled BEFORE the create, under an id the fixer minted, so a
// crash between the two leaves a row naming a book that was never made.
// NewValue repeats the id.
//
// Restorable. The revert SOFT-DELETES the created book; it never deletes it,
// and it never deletes the book_file rows the fixer created on it (no repair
// may delete a book_file row, and the purge refuses a book that still owns
// rows, so the hidden book and its rows stay as a record). A book that does
// not exist (the create never happened) or is already soft-deleted counts as
// restored.
//
// Distinct from "book_create", which the organizer's change counters and the
// activity changelog already use for an ordinary import.
const ChangeTypeRepairBookCreate = "repair_book_create"

// RepairBookCreateStore is what CheckRepairBookCreate reads: the book, its
// book_file rows, and the operation's journal (the book_file_create rows that
// say which rows the fixer gave it).
type RepairBookCreateStore interface {
	GetBookByID(id string) (*database.Book, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	GetOperationChanges(operationID string) ([]*database.OperationChange, error)
}

// CheckRepairBookCreate is the preflight's verdict on a
// ChangeTypeRepairBookCreate row: ErrAlreadyRestored when the book is absent
// or already soft-deleted, nil when the revert will soft-delete it, and a
// refusal when the row is malformed, the book cannot be read, or the book is
// no longer only what the operation made:
//
//   - one of its book_file rows is not a book_file_create row of this
//     operation (a row moved or added onto it since), or names another path
//     than the one journaled (organize or a repoint moved it). A journaled row
//     that is not on the book is allowed: a cut-off apply journals before it
//     creates, and a row reassigned away is no longer this book's to hide.
//   - it joined a version group (a user or dedup linked it to another book).
func CheckRepairBookCreate(store RepairBookCreateStore, c *database.OperationChange) error {
	if c.BookID == "" || c.NewValue != c.BookID {
		return &ReferentError{Reason: ReasonOldValueUnparsable, Detail: fmt.Sprintf("created book of change %s", c.ID)}
	}
	b, err := store.GetBookByID(c.BookID)
	if err != nil {
		return &ReferentError{Reason: ReasonBookLookupFailed, Detail: fmt.Sprintf("read created book %s: %v", c.BookID, err)}
	}
	if b == nil || b.IsSoftDeleted() {
		return ErrAlreadyRestored
	}
	if b.VersionGroupID != nil && *b.VersionGroupID != "" {
		return &ReferentError{Reason: ReasonChangedSince, Detail: fmt.Sprintf(
			"created book %s joined version group %s since", c.BookID, *b.VersionGroupID)}
	}
	changes, err := store.GetOperationChanges(c.OperationID)
	if err != nil {
		return &ReferentError{Reason: ReasonFieldUnreadable, Detail: fmt.Sprintf("read journal of %s: %v", c.OperationID, err)}
	}
	journaled := map[string]string{} // book_file id -> path
	for _, oc := range changes {
		if oc.BookID == c.BookID && oc.ChangeType == ChangeTypeBookFileCreate && strings.HasPrefix(oc.FieldName, "book_file:") {
			journaled[strings.TrimPrefix(oc.FieldName, "book_file:")] = oc.NewValue
		}
	}
	rows, err := store.GetBookFiles(c.BookID)
	if err != nil {
		return &ReferentError{Reason: ReasonFieldUnreadable, Detail: fmt.Sprintf("read rows of %s: %v", c.BookID, err)}
	}
	for _, r := range rows {
		p, ok := journaled[r.ID]
		if !ok {
			return &ReferentError{Reason: ReasonChangedSince, Detail: fmt.Sprintf(
				"created book %s gained row %s (%s) since", c.BookID, r.ID, r.FilePath)}
		}
		if r.FilePath != p {
			return &ReferentError{Reason: ReasonChangedSince, Detail: fmt.Sprintf(
				"row %s of created book %s moved from %s to %s since", r.ID, c.BookID, p, r.FilePath)}
		}
	}
	return nil
}
