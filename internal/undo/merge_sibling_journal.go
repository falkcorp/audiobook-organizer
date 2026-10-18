// file: internal/undo/merge_sibling_journal.go
// version: 1.0.0
// guid: 9b2e4f71-3c58-4a06-8d1e-6f7a0c35b2d9
// last-edited: 2026-10-18

package undo

import (
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
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
const ChangeTypeMergeSiblingJournal = "merge_sibling_journal"

// MergeSiblingJournalField is the FieldName of a
// ChangeTypeMergeSiblingJournal row.
const MergeSiblingJournalField = "journal"

// validMergeSiblingJournalRow reports whether c names a sibling journal id
// the merge package can look up (no key separators).
func validMergeSiblingJournalRow(c *database.OperationChange) bool {
	return c.FieldName == MergeSiblingJournalField && c.NewValue != "" && !strings.ContainsAny(c.NewValue, ":/")
}

// JournalPointerWriter writes undo-ledger rows. Every database.Store carries
// it; the merge cores resolve it with database.AsCapability so their own store
// interfaces are not widened.
type JournalPointerWriter interface {
	CreateOperationChange(change *database.OperationChange) error
}

// RecordMergeJournalPointer journals the row that lets POST
// /operations/:id/revert find a merge's own undo record: a
// ChangeTypeMergeCombineJournal row naming a merge.CombineJournal, or a
// ChangeTypeMergeSiblingJournal row naming a merge.SiblingMoveJournal, on
// bookID (the keep / survivor) under opID. Call it after the journal is
// written and BEFORE the merge's first mutation: an error means the merge must
// be refused, because a merge with no way back is worse than no merge.
//
// The returned void marks the row voided (the revert then skips it), for a
// merge refused after the row was written but before it changed anything.
func RecordMergeJournalPointer(w JournalPointerWriter, opID, bookID, changeType, journalID string) (void func() error, err error) {
	switch {
	case w == nil:
		return nil, fmt.Errorf("no undo ledger to journal the %s row in", changeType)
	case opID == "":
		return nil, fmt.Errorf("no operation id to journal the %s row under", changeType)
	case changeType != ChangeTypeMergeCombineJournal && changeType != ChangeTypeMergeSiblingJournal:
		return nil, fmt.Errorf("%q is not a merge journal pointer type", changeType)
	case journalID == "" || strings.ContainsAny(journalID, ":/"):
		return nil, fmt.Errorf("%s row: invalid journal id %q", changeType, journalID)
	}
	row := &database.OperationChange{
		ID:          ulid.Make().String(),
		OperationID: opID,
		BookID:      bookID,
		ChangeType:  changeType,
		FieldName:   MergeCombineJournalField, // == MergeSiblingJournalField
		NewValue:    journalID,
		CreatedAt:   time.Now(),
	}
	if err := w.CreateOperationChange(row); err != nil {
		return nil, fmt.Errorf("record %s row for journal %s: %w", changeType, journalID, err)
	}
	return func() error {
		voided := *row
		now := time.Now()
		voided.RevertedAt, voided.Voided = &now, true
		return w.CreateOperationChange(&voided)
	}, nil
}
