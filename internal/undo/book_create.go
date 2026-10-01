// file: internal/undo/book_create.go
// version: 1.2.0
// guid: 7f3a9c21-5e8b-4d06-a1c4-2b9e6d0f8a53
// last-edited: 2026-10-01

package undo

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// ChangeTypeRepairBookCreate: a Repairs fixer created the book BookID (the
// folder-books fixer gives each orphaned work of a retired folder-book its own
// book). It is journaled BEFORE the create, under an id the fixer minted, so a
// crash between the two leaves a row naming a book that was never made.
// NewValue is RepairBookCreateValue JSON: the id and the title, author and
// file_path the book was created with (an older row's NewValue is the bare id).
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

// RepairBookCreateValue is a repair_book_create row's NewValue: what the
// fixer created, so the revert can tell the book was edited since.
type RepairBookCreateValue struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	AuthorID *int   `json:"author_id,omitempty"`
	FilePath string `json:"file_path"`
}

// String is the row's NewValue.
func (v RepairBookCreateValue) String() string {
	b, _ := json.Marshal(v) // a struct of strings and an *int: never fails
	return string(b)
}

// ParseRepairBookCreateValue reads a row's NewValue; ok is false for a
// malformed value. A bare id (an older row) parses with only ID set and
// legacy true.
func ParseRepairBookCreateValue(bookID, newValue string) (v RepairBookCreateValue, legacy, ok bool) {
	if newValue == bookID && bookID != "" {
		return RepairBookCreateValue{ID: bookID}, true, true
	}
	if err := json.Unmarshal([]byte(newValue), &v); err != nil || v.ID == "" || v.ID != bookID {
		return RepairBookCreateValue{}, false, false
	}
	return v, false, true
}

// RepairBookCreateStore is what CheckRepairBookCreate reads: the book, its
// book_file rows and external ids, and the operation's journal (the
// book_file_create rows that say which rows the fixer gave it). It must also
// implement merge.UserProgressMerger, or every row is refused: without it the
// check cannot tell whether someone listened to the book.
type RepairBookCreateStore interface {
	GetBookByID(id string) (*database.Book, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	GetOperationChanges(operationID string) ([]*database.OperationChange, error)
	GetExternalIDsForBook(bookID string) ([]database.ExternalIDMapping, error)
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
//   - it joined a version group, carries an iTunes persistent id or any live
//     external id, or its title, author or file_path differ from what the
//     operation created (a user or another job made it theirs).
//   - anyone has listening progress on it.
func CheckRepairBookCreate(store RepairBookCreateStore, c *database.OperationChange) error {
	created, legacy, ok := ParseRepairBookCreateValue(c.BookID, c.NewValue)
	if !ok {
		return &ReferentError{Reason: ReasonOldValueUnparsable, Detail: fmt.Sprintf("created book of change %s", c.ID)}
	}
	b, err := store.GetBookByID(c.BookID)
	if err != nil {
		return &ReferentError{Reason: ReasonBookLookupFailed, Detail: fmt.Sprintf("read created book %s: %v", c.BookID, err)}
	}
	if b == nil || b.IsSoftDeleted() {
		return ErrAlreadyRestored
	}
	changed := func(format string, args ...any) error {
		return &ReferentError{Reason: ReasonChangedSince, Detail: fmt.Sprintf("created book %s: ", c.BookID) + fmt.Sprintf(format, args...)}
	}
	if b.VersionGroupID != nil && *b.VersionGroupID != "" {
		return changed("joined version group %s since", *b.VersionGroupID)
	}
	if b.ITunesPersistentID != nil && *b.ITunesPersistentID != "" {
		return changed("carries iTunes id %s since", *b.ITunesPersistentID)
	}
	if !legacy {
		if b.Title != created.Title {
			return changed("title %q edited to %q since", created.Title, b.Title)
		}
		if (b.AuthorID == nil) != (created.AuthorID == nil) || (b.AuthorID != nil && *b.AuthorID != *created.AuthorID) {
			return changed("author edited since")
		}
		if b.FilePath != created.FilePath {
			return changed("file_path %s changed to %s since", created.FilePath, b.FilePath)
		}
	}
	exts, err := store.GetExternalIDsForBook(c.BookID)
	if err != nil {
		return &ReferentError{Reason: ReasonFieldUnreadable, Detail: fmt.Sprintf("external ids of %s: %v", c.BookID, err)}
	}
	for _, e := range exts {
		if !e.Tombstoned {
			return changed("gained external id %s/%s since", e.Source, e.ExternalID)
		}
	}
	um, ok := store.(merge.UserProgressMerger)
	if !ok {
		return &ReferentError{Reason: ReasonFieldUnreadable, Detail: "this store cannot read listening progress"}
	}
	has, err := merge.BookHasUserProgress(um, c.BookID)
	if err != nil {
		return &ReferentError{Reason: ReasonFieldUnreadable, Detail: fmt.Sprintf("listening progress of %s: %v", c.BookID, err)}
	}
	if has {
		return changed("has listening progress")
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
			return changed("gained row %s (%s) since", r.ID, r.FilePath)
		}
		if r.FilePath != p {
			return changed("row %s moved from %s to %s since", r.ID, p, r.FilePath)
		}
	}
	return nil
}
