// file: internal/batch/trash_grants.go
// version: 1.4.0
// guid: 5e7d5df1-473b-4136-b691-2798b1df7c12
// last-edited: 2026-10-26

package batch

import (
	"errors"
	"fmt"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TrashGrants is what the HTTP caller of a batch request may do to books' place
// in the library: move books into the trash (library.delete) and take them out
// (library.organize). TrashEffects decides from the request's shape whether the
// handler must ask for a permission before anything runs; TrashGrants is what
// the handler found the caller to hold, handed to the service so each row can
// be judged on what the edit does to THAT row, read under its write lock. The
// shape is not enough: library_state "organized" on a book that is in the trash
// takes it out of the trash and looks like any other metadata edit.
//
// It travels on the request (never decoded from the body: `json:"-"`). A nil
// *TrashGrants fails closed: it is judged as a caller holding neither
// permission, so an edit that moves a book into or out of the trash is refused
// and every other edit is unaffected. Both HTTP handlers (the /batch-operations
// update and /batch endpoints) always set it; there is no in-process caller of
// these two service methods that edits trash state on nobody's behalf, and one
// that appears must say what it may do (&TrashGrants{Trash: true, Restore: true})
// rather than inherit an unjudged bypass. This covers update payloads (the
// two ways a payload moves a row into or out of the trash: library_state and
// marked_for_deletion) and the "delete" (soft and hard) and "restore" actions
// of ExecuteOperations: a soft or hard delete needs Trash, and a restore of a
// book in the trash needs Restore.
type TrashGrants struct {
	// Trash: the caller holds library.delete.
	Trash bool
	// Restore: the caller holds library.organize.
	Restore bool
}

// TrashDeniedError is a row's edit refused because it would change the book's
// place in the library beyond what the caller may do. Nothing was written.
type TrashDeniedError struct {
	// Permission is the one the caller lacks.
	Permission string
	// Action says what the edit would have done to the book.
	Action string
}

func (e *TrashDeniedError) Error() string {
	return fmt.Sprintf("not changed: this edit would %s, which needs %s", e.Action, e.Permission)
}

// libraryStateLeavesTrash reports whether the payload sets library_state to a
// value other than the trash label (any spelling), the other way besides
// marked_for_deletion=false that a batch edit can take a book out of the trash.
func libraryStateLeavesTrash(updates map[string]any) bool {
	v, ok := updates["library_state"].(string)
	return ok && !strings.EqualFold(strings.TrimSpace(v), "deleted")
}

// checkRow judges one row's edit: before is the stored row as it was read under
// the write lock, after the same row with the payload applied. A book in the
// trash (database.IsInTrash: its deletion flag is set or it is labelled
// "deleted") whose library_state the payload sets to a live value is a restore
// and needs library.organize; a book that was not in the trash and is after the
// edit is a trash and needs library.delete. A nil receiver is a caller with
// neither grant (see TrashGrants). An update that sets marked_for_deletion to
// false never gets here: it is a restore through merge.RestoreFromTrash and is
// judged by checkRestore.
func (g *TrashGrants) checkRow(before, after *database.Book, updates map[string]any) error {
	if g == nil {
		g = &TrashGrants{}
	}
	// There is no "no longer in the trash after the edit" term in the restore
	// rule: the one way besides a live library_state to leave the trash is
	// marked_for_deletion=false, which does not reach this function, and
	// nothing else in applyUpdates clears the flag or the label, so such a term
	// could never differ from libraryStateLeavesTrash.
	if database.IsInTrash(before) && libraryStateLeavesTrash(updates) && !g.Restore {
		return &TrashDeniedError{Permission: "library.organize", Action: "change the library state of a book in the trash, taking it out of the trash"}
	}
	if !database.IsInTrash(before) && database.IsInTrash(after) && !g.Trash {
		return &TrashDeniedError{Permission: "library.delete", Action: "move the book to the trash"}
	}
	return nil
}

// checkRestore judges an update payload that sets marked_for_deletion=false for
// one row, and the "restore" action: both are a restore
// (merge.RestoreFromTrash), so a book in the trash needs library.organize, and
// a book that is not in it is a plain edit. This is the early refusal, on the
// row the caller read before taking the restore's locks; judgeRestore repeats
// the decision on the row as locked. A nil receiver is a caller holding
// nothing, so the service refuses rather than trusting every caller to have
// asked.
func (g *TrashGrants) checkRestore(book *database.Book) error {
	if g == nil {
		g = &TrashGrants{}
	}
	if book != nil && database.IsInTrash(book) && !g.Restore {
		return &TrashDeniedError{Permission: "library.organize", Action: "restore the book from the trash"}
	}
	return nil
}

// restoreJudge is the judge for merge.RestoreFromTrash for a restore carrying
// updates (nil for the "restore" action): judgeRestore with the payload in hand,
// so a refusal can say which of its causes applies.
func (g *TrashGrants) restoreJudge(updates map[string]any) func(before, after *database.Book) error {
	return func(before, after *database.Book) error { return g.judgeRestore(before, after, updates) }
}

// judgeRestore is the verdict merge.RestoreFromTrash asks for, before it
// touches the row's merge redirect and again inside its write: before is the
// row as read (as locked, the second time), after the row as it would be
// written (the restore and the payload applied). A book in the trash needs
// library.organize to come back, and a book that was not in the trash and is
// after the write (library_state "deleted" next to marked_for_deletion=false)
// needs library.delete, so both directions are judged on both branches.
//
// A restore of a book that was in the trash and would still be in it after the
// write is refused whatever the caller holds, because the write would leave a
// book in the trash by its label alone: deletion flag cleared (IsSoftDeleted
// false), merge link cleared, which the purge precheck reads as "restored
// meanwhile". Two inputs reach it. A payload that sets library_state "deleted"
// while restoring (the handler already asks for library.delete for that label,
// but cannot make the service honour both halves). And a row another writer
// trashed between RestoreFromTrash's read and its write: the restore rule ran
// on the live row it read, so only the payload's flag change lands on a row
// that still carries the label. updates tells them apart for the message.
//
// A book that was NOT in the trash is not covered by that refusal: the same
// payload on a live book moves it to the trash by label (flag false) when the
// caller holds library.delete, as a plain {library_state: "deleted"} edit does
// (checkRow); nothing is lost on a live row, and it predates this check.
//
// It runs under the merge lock, the version-group locks and the book's stripe:
// it reads only the two rows and calls nothing else. A nil receiver holds
// neither permission.
func (g *TrashGrants) judgeRestore(before, after *database.Book, updates map[string]any) error {
	if g == nil {
		g = &TrashGrants{}
	}
	if database.IsInTrash(before) {
		if !g.Restore {
			return &TrashDeniedError{Permission: "library.organize", Action: "restore the book from the trash"}
		}
		if database.IsInTrash(after) {
			if isTrash(updates) {
				return errRestoreAndTrashInOneEdit
			}
			return errMovedToTrashMeanwhile
		}
	}
	if !database.IsInTrash(before) && database.IsInTrash(after) && !g.Trash {
		return &TrashDeniedError{Permission: "library.delete", Action: "move the book to the trash"}
	}
	return nil
}

// The refusals of a restore that would leave the book in the trash (see
// judgeRestore). Nothing was written.
var (
	errRestoreAndTrashInOneEdit = errors.New(`not changed: this edit would restore the book from the trash and put it back in the trash in one write (marked_for_deletion=false with library_state "deleted"); send one or the other`)
	errMovedToTrashMeanwhile    = errors.New("not changed: the book was moved to the trash by another change while this edit was being applied, so it was not restored; reload the book and try again")
)

// checkTrash judges the "delete" action, soft or hard: both need
// library.delete. action says what the edit would do, for the message. A nil
// receiver holds nothing.
func (g *TrashGrants) checkTrash(action string) error {
	if g == nil || !g.Trash {
		return &TrashDeniedError{Permission: "library.delete", Action: action}
	}
	return nil
}
