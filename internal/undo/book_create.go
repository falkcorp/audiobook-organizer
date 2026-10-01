// file: internal/undo/book_create.go
// version: 1.0.0
// guid: 7f3a9c21-5e8b-4d06-a1c4-2b9e6d0f8a53
// last-edited: 2026-10-01

package undo

import (
	"fmt"

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

// CheckRepairBookCreate is the preflight's verdict on a
// ChangeTypeRepairBookCreate row: ErrAlreadyRestored when the book is absent
// or already soft-deleted, nil when the revert will soft-delete it, and a
// refusal when the row is malformed or the book cannot be read.
func CheckRepairBookCreate(store interface {
	GetBookByID(id string) (*database.Book, error)
}, c *database.OperationChange) error {
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
	return nil
}
