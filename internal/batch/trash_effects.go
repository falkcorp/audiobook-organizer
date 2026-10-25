// file: internal/batch/trash_effects.go
// version: 1.0.0
// guid: c203fe3a-05b9-49fd-befe-5c8b07f2c79d
// last-edited: 2026-10-25

package batch

import "strings"

// TrashEffects is what a batch request does to books' place in the library,
// derived from the SAME parsed request the batch service then executes. The
// single-book routes guard each of these with its own permission (hard delete
// and soft delete: library.delete; restore: library.organize), and the batch
// routes are mounted on library.edit_metadata, so the handler asks for the
// extra permission each effect needs before it executes anything. The
// classification lives beside the execution (it reads the payload with the
// same type assertions applyUpdates and isRestore use) so the two cannot
// drift: a payload spelling the service would act on is a payload this
// classifies.
type TrashEffects struct {
	// HardDelete: an operation permanently deletes a book.
	HardDelete bool
	// Trash: a book is moved to the trash (soft delete), by the "delete"
	// action without hard_delete or by an update setting marked_for_deletion
	// to true or library_state to "deleted".
	Trash bool
	// Restore: a book is taken out of the trash, by the "restore" action or an
	// update setting marked_for_deletion to false.
	Restore bool
}

// Any reports whether the request has any effect beyond a metadata edit.
func (e TrashEffects) Any() bool { return e.HardDelete || e.Trash || e.Restore }

func (e TrashEffects) or(o TrashEffects) TrashEffects {
	return TrashEffects{e.HardDelete || o.HardDelete, e.Trash || o.Trash, e.Restore || o.Restore}
}

// isTrash reports whether a batch payload moves a book to the trash: the same
// two keys applyUpdates acts on (marked_for_deletion=true, and library_state
// "deleted", which is the trash label).
func isTrash(updates map[string]any) bool {
	if v, ok := updates["marked_for_deletion"].(bool); ok && v {
		return true
	}
	if v, ok := updates["library_state"].(string); ok && strings.EqualFold(strings.TrimSpace(v), "deleted") {
		return true
	}
	return false
}

func updateEffects(updates map[string]any) TrashEffects {
	return TrashEffects{Trash: isTrash(updates), Restore: isRestore(updates)}
}

// TrashEffects classifies POST /audiobooks/batch (the same updates for every
// listed book).
func (r *BatchUpdateRequest) TrashEffects() TrashEffects {
	if len(r.IDs) == 0 {
		return TrashEffects{}
	}
	return updateEffects(r.Updates)
}

// TrashEffects classifies POST /audiobooks/batch-operations. An unknown action
// does nothing (ExecuteOperations answers it with an error), so it has no
// effect here.
func (r *BatchOperationsRequest) TrashEffects() TrashEffects {
	var e TrashEffects
	for _, op := range r.Operations {
		switch op.Action {
		case "update":
			e = e.or(updateEffects(op.Updates))
		case "delete":
			if op.HardDelete {
				e.HardDelete = true
			} else {
				e.Trash = true
			}
		case "restore":
			e.Restore = true
		}
	}
	return e
}
