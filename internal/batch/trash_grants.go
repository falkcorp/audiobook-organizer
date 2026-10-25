// file: internal/batch/trash_grants.go
// version: 1.0.0
// guid: 5e7d5df1-473b-4136-b691-2798b1df7c12
// last-edited: 2026-10-25

package batch

import (
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
// *TrashGrants means no HTTP caller to judge, which is how the queued
// metadata apply and other in-process callers use the service; every handler
// sets it.
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
// the write lock, after the same row with the payload applied. A book that was
// in the trash (database.IsInTrash: its deletion flag is set or it is labelled
// "deleted") and leaves it, or has its library_state set to a live value while
// it is in the trash, is a restore and needs library.organize; a book that was
// not in the trash and is after the edit is a trash and needs library.delete.
// A nil receiver judges nothing (see TrashGrants).
func (g *TrashGrants) checkRow(before, after *database.Book, updates map[string]any) error {
	if g == nil {
		return nil
	}
	wasIn, nowIn := database.IsInTrash(before), database.IsInTrash(after)
	if wasIn && (!nowIn || libraryStateLeavesTrash(updates)) && !g.Restore {
		return &TrashDeniedError{Permission: "library.organize", Action: "change the library state of a book in the trash, taking it out of the trash"}
	}
	if !wasIn && nowIn && !g.Trash {
		return &TrashDeniedError{Permission: "library.delete", Action: "move the book to the trash"}
	}
	return nil
}
