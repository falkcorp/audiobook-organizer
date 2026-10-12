// file: internal/audiobooks/revert_merge_combine.go
// version: 1.0.0
// guid: 3f7b0d52-c1e4-4a98-8d26-9e5a7c13b0f4
// last-edited: 2026-10-12

package audiobooks

import (
	"errors"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// combineUndoer is the merge service surface the revert of an
// undo.ChangeTypeMergeCombineJournal row needs (merge.Service).
type combineUndoer interface {
	GetCombineJournal(id string) (*merge.CombineJournal, error)
	UndoCombine(id string) (*merge.CombineUndoResult, error)
}

// combineUndoerFor returns rs.combineUndo, or merge.NewService over the
// revert's store when that store is a merge.Store (the server's is); nil
// when it is neither, and the row is then refused. Built on demand, not in
// NewRevertService: NewService warns when the store cannot carry sync
// identity, which would be noise on every revert that has no such row.
// revertServiceStore is not widened for it, as for the metadata rows.
func (rs *RevertService) combineUndoerFor() combineUndoer {
	if rs.combineUndo != nil {
		return rs.combineUndo
	}
	if st, ok := rs.db.(merge.Store); ok {
		return merge.NewService(st)
	}
	return nil
}

// revertMergeCombineJournal undoes the merge a pointer row names, through
// its combine journal (merge.Service.UndoCombine), which takes the merge lock
// itself (this must not hold it: the mutex is not re-entrant) and refuses as
// a whole when the library changed since. A journal already undone, through
// the merge undo route or an earlier revert, is already restored.
func (rs *RevertService) revertMergeCombineJournal(c *database.OperationChange) error {
	if _, err := rs.loadBook(c.BookID); err != nil {
		return err
	}
	u := rs.combineUndoerFor()
	if u == nil {
		return &undo.ReferentError{Reason: undo.ReasonFieldUnreadable,
			Detail: fmt.Sprintf("book %s: no merge service to undo combine journal %s with", c.BookID, c.NewValue)}
	}
	j, err := u.GetCombineJournal(c.NewValue)
	switch {
	case errors.Is(err, merge.ErrCombineJournalNotFound):
		return &undo.ReferentError{Reason: undo.ReasonFieldUnreadable,
			Detail: fmt.Sprintf("book %s: combine journal %s not found", c.BookID, c.NewValue)}
	case err != nil:
		return fmt.Errorf("book %s: read combine journal %s: %w", c.BookID, c.NewValue, err)
	case j.Status == merge.CombineJournalUndone:
		return undo.ErrAlreadyRestored
	}
	if _, err := u.UndoCombine(c.NewValue); err != nil {
		var refused *merge.CombineUndoRefusedError
		if errors.As(err, &refused) {
			return &undo.ReferentError{Reason: undo.ReasonChangedSince, Detail: fmt.Sprintf("book %s: %v", c.BookID, refused)}
		}
		return fmt.Errorf("book %s: combine journal %s not undone: %w", c.BookID, c.NewValue, err)
	}
	return nil
}
