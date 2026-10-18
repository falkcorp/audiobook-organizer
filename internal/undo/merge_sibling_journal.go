// file: internal/undo/merge_sibling_journal.go
// version: 1.0.1
// guid: 9b2e4f71-3c58-4a06-8d1e-6f7a0c35b2d9
// last-edited: 2026-10-18

package undo

import (
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// ChangeTypeMergeSiblingJournal: a merge (merge.Service.MergeBooksWithOptions)
// carried version-group siblings nobody named into its group, and wrote its
// own undo record for that move (merge.SiblingMoveJournal); this row names
// that record. It is journaled on the merge's survivor (BookID) right after
// the merge wrote the sibling journal Pending and before its first membership
// write, under the caller's operation id or, for a merge no operation runs, a
// correlation id of its own. FieldName is MergeSiblingJournalField, NewValue
// the sibling journal id, OldValue "".
//
// Restorable: the revert undoes that journal (merge.Service.UndoSiblingMove),
// which puts every sibling the merge moved back in its old group with its old
// flag, and reverses the losers' state follow onto a flag holder. It does not
// bring back the merge's named losers (they are restored from the trash), as
// the retired POST /merge/sibling-undo/:journal_id did not. A journal already
// undone, or aborted (the merge was refused before it moved anything), counts
// restored with nothing written; one a newer merge built on is refused,
// changed since.
//
// The merge package writes the row (merge.RecordJournalPointer, at the
// chokepoint) and cannot import this one, so the string is defined there.
const ChangeTypeMergeSiblingJournal = merge.SiblingJournalPointerType

// MergeSiblingJournalField is the FieldName of a
// ChangeTypeMergeSiblingJournal row.
const MergeSiblingJournalField = merge.JournalPointerField

// validMergeSiblingJournalRow reports whether c names a sibling journal id
// the merge package can look up (no key separators).
func validMergeSiblingJournalRow(c *database.OperationChange) bool {
	return c.FieldName == MergeSiblingJournalField && c.NewValue != "" && !strings.ContainsAny(c.NewValue, ":/")
}
