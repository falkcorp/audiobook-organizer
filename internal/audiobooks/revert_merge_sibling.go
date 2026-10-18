// file: internal/audiobooks/revert_merge_sibling.go
// version: 1.1.0
// guid: 7c4e1a96-2b83-4d50-9f17-a6e3d0b85c21
// last-edited: 2026-10-18

package audiobooks

import (
	"errors"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// siblingUndoer is the merge service surface the revert of an
// undo.ChangeTypeMergeSiblingJournal row needs (merge.Service).
type siblingUndoer interface {
	GetSiblingMoveJournal(id string) (*merge.SiblingMoveJournal, error)
	UndoSiblingMove(id string) (*merge.SiblingUndoResult, error)
}

// siblingUndoerFor returns rs.siblingUndo, or merge.NewService over the
// revert's store when that store is a merge.Store (the server's is); nil when
// it is neither, and the row is then refused. Built on demand for the reason
// combineUndoerFor gives.
func (rs *RevertService) siblingUndoerFor() siblingUndoer {
	if rs.siblingUndo != nil {
		return rs.siblingUndo
	}
	if st, ok := rs.db.(merge.Store); ok {
		return merge.NewService(st)
	}
	return nil
}

// siblingJournalDone reports whether a sibling journal leaves nothing to
// put back: undone (every sibling resolved, by an earlier revert or a
// per-loser undo) or aborted (the merge was refused before it moved any).
func siblingJournalDone(j *merge.SiblingMoveJournal) bool {
	return j.Status == merge.SiblingJournalUndone || j.Status == merge.SiblingJournalAborted
}

// revertMergeSiblingJournal undoes the sibling move a pointer row names,
// through its journal (merge.Service.UndoSiblingMove), which takes the merge
// lock itself (this must not hold it: the mutex is not re-entrant), checks
// each sibling's current group, and refuses as a whole when a newer merge
// moved one of them into the same group again. A journal with nothing left to
// put back is already restored.
//
// The survivor row (c.BookID) is not required: the undo moves the siblings
// back by the journal alone and handles a survivor purged since, so a merge
// whose survivor was later combined away and purged still reverts. A missing
// journal still refuses.
func (rs *RevertService) revertMergeSiblingJournal(c *database.OperationChange) error {
	u := rs.siblingUndoerFor()
	if u == nil {
		return &undo.ReferentError{Reason: undo.ReasonFieldUnreadable,
			Detail: fmt.Sprintf("book %s: no merge service to undo sibling-move journal %s with", c.BookID, c.NewValue)}
	}
	j, err := u.GetSiblingMoveJournal(c.NewValue)
	switch {
	case errors.Is(err, merge.ErrSiblingJournalNotFound):
		return &undo.ReferentError{Reason: undo.ReasonFieldUnreadable,
			Detail: fmt.Sprintf("book %s: sibling-move journal %s not found", c.BookID, c.NewValue)}
	case err != nil:
		return fmt.Errorf("book %s: read sibling-move journal %s: %w", c.BookID, c.NewValue, err)
	case siblingJournalDone(j):
		return undo.ErrAlreadyRestored
	}
	if _, err := u.UndoSiblingMove(c.NewValue); err != nil {
		if errors.Is(err, merge.ErrSiblingUndoRefused) {
			// The pre-read above and the undo are not atomic: another undo
			// (a second revert, a per-loser undo) may have finished in
			// between, and then UndoSiblingMove refuses a journal that is
			// done. Re-read before calling it a change since.
			if j2, rerr := u.GetSiblingMoveJournal(c.NewValue); rerr == nil && siblingJournalDone(j2) {
				return undo.ErrAlreadyRestored
			}
			return &undo.ReferentError{Reason: undo.ReasonChangedSince, Detail: fmt.Sprintf("book %s: %v", c.BookID, err)}
		}
		return fmt.Errorf("book %s: sibling-move journal %s not undone: %w", c.BookID, c.NewValue, err)
	}
	return nil
}
