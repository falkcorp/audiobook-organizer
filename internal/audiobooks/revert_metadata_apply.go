// file: internal/audiobooks/revert_metadata_apply.go
// version: 1.0.0
// guid: 6e2d8a14-0b7c-4f39-a5e1-d3c9b8f27a60
// last-edited: 2026-10-06

package audiobooks

import (
	"errors"
	"fmt"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// The reversals of the two rows maintenance.version-twin-metadata journals:
// a metadata apply (undo.ChangeTypeMetadataApply, one history batch) and a
// candidate-cache copy (undo.ChangeTypeMetadataCacheCopy). Neither widens
// revertServiceStore: the store surfaces they need are asserted on rs.db,
// and a store without them refuses the row (fail closed) rather than
// counting it restored.

// applyBatchUndoer is the metadata service surface the apply row's revert
// needs (metafetch.Service.UndoApplyBatch).
type applyBatchUndoer func(bookID, batchID string) (*metafetch.UndoApplyResult, error)

// defaultApplyBatchUndoer is metafetch's per-batch undo over db, when db is a
// full metafetch store (production's is), else nil.
func defaultApplyBatchUndoer(db revertServiceStore) applyBatchUndoer {
	st, ok := db.(metafetch.Store)
	if !ok {
		return nil
	}
	return metafetch.NewService(st).UndoApplyBatch
}

// revertMetadataApply undoes the history batch a metadata_apply row names.
// Each field goes back only while it still holds what the apply wrote; a
// field edited since is left. All fields left: refused changed-since. Some
// put back and some left: restored in part (partialTagRestore). A batch
// already undone counts restored with nothing written.
func (rs *RevertService) revertMetadataApply(c *database.OperationChange) error {
	if _, err := rs.loadBook(c.BookID); err != nil {
		return err
	}
	if rs.UndoApplyBatch == nil {
		return &undo.ReferentError{Reason: undo.ReasonFieldUnreadable,
			Detail: fmt.Sprintf("book %s: no metadata service to undo apply batch %s with", c.BookID, c.NewValue)}
	}
	res, err := rs.UndoApplyBatch(c.BookID, c.NewValue)
	switch {
	case errors.Is(err, metafetch.ErrApplyAlreadyUndone):
		return undo.ErrAlreadyRestored
	case err != nil:
		return fmt.Errorf("book %s: apply batch %s not undone: %w", c.BookID, c.NewValue, err)
	}
	var left []string
	for _, group := range []struct {
		why    string
		fields []string
	}{{"changed since", res.ChangedSince}, {"locked", res.Locked}, {"failed", res.Failed}} {
		for _, f := range group.fields {
			why := group.why
			if r := res.FailedReasons[f]; r != "" {
				why += ": " + r
			}
			left = append(left, f+" ("+why+")")
		}
	}
	switch {
	case len(res.Reverted) == 0 && len(left) == 0:
		return undo.ErrAlreadyRestored
	case len(res.Reverted) == 0:
		return &undo.ReferentError{Reason: undo.ReasonChangedSince,
			Detail: fmt.Sprintf("book %s: no field of apply batch %s still holds what it wrote: %s", c.BookID, c.NewValue, strings.Join(left, ", "))}
	case len(left) > 0:
		return &partialTagRestore{detail: fmt.Sprintf("book %s apply batch %s: restored %s; left %s",
			c.BookID, c.NewValue, strings.Join(res.Reverted, ", "), strings.Join(left, ", "))}
	}
	return nil
}

// revertCacheStore is the cache-row surface revertMetadataCacheCopy asserts
// on rs.db.
type revertCacheStore interface {
	GetMetadataCache(bookID string) (*database.MetadataCandidateCache, error)
	PutMetadataCache(entry *database.MetadataCandidateCache) error
	DeleteMetadataCache(bookID string) error
}

// revertMetadataCacheCopy puts a book's candidate cache row back as it was
// before a copy: deleted when it had none, the prior row otherwise, only
// while the row still carries the copy's stamp (undo.CheckMetadataCacheCopy).
// The store offers no compare-and-set on a cache row, so a fetch landing
// between the check and the write is overwritten; the window is one read.
func (rs *RevertService) revertMetadataCacheCopy(c *database.OperationChange) error {
	if _, err := rs.loadBook(c.BookID); err != nil {
		return err
	}
	cs, ok := rs.db.(revertCacheStore)
	if !ok {
		return &undo.ReferentError{Reason: undo.ReasonFieldUnreadable,
			Detail: fmt.Sprintf("book %s: this store cannot read or write candidate cache rows", c.BookID)}
	}
	cur, err := cs.GetMetadataCache(c.BookID)
	if err != nil {
		return &undo.ReferentError{Reason: undo.ReasonFieldUnreadable,
			Detail: fmt.Sprintf("read the candidate cache of %s: %v", c.BookID, err)}
	}
	if err := undo.CheckMetadataCacheCopy(cur, c); err != nil {
		return err
	}
	old, err := undo.DecodeMetadataCacheOld(c.OldValue)
	if err != nil {
		return err
	}
	if old == nil {
		if err := cs.DeleteMetadataCache(c.BookID); err != nil {
			return fmt.Errorf("book %s: delete the copied candidate cache: %w", c.BookID, err)
		}
		return nil
	}
	if err := cs.PutMetadataCache(old); err != nil {
		return fmt.Errorf("book %s: restore the prior candidate cache: %w", c.BookID, err)
	}
	return nil
}
