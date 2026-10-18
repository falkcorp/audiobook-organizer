// file: internal/merge/undo_pointer.go
// version: 1.0.0
// guid: 1a6c9e38-5f02-4b7d-8e49-c3d0a7f25b16
// last-edited: 2026-10-18

package merge

import (
	"fmt"
	"strings"
	"time"

	ulid "github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Undo pointers.
//
// The merge cores keep their own undo records (CombineJournal,
// SiblingMoveJournal), but the live undo path is POST /operations/:id/revert,
// which reads only an operation's opchange rows. So every combine and every
// sibling move journals one pointer row naming its record HERE, at the
// chokepoint, right after the record is written and before the first
// mutation -- for the reason the sibling journal itself is written here: a
// caller that forgot would leave a merge with no way back, and every future
// caller would have to remember. A pointer that cannot be written refuses the
// merge.
//
// The row goes under the caller's operation id (CombineOptions.OperationID,
// MergeOptions.OperationID, dedup's SplitMergeOptions.OperationID), so
// reverting that operation undoes every merge it made. A merge no operation
// runs (a synchronous HTTP handler, a review approval) gets a correlation id
// of its own, minted here and returned in the result, as POST
// /audiobooks/:id/organize does: the revert reads only the opchange rows,
// never an operations row, and GET /audiobooks/:id/changes lists the pointer
// on the survivor.
//
// The change types are the undo package's (undo.ChangeTypeMergeCombineJournal,
// undo.ChangeTypeMergeSiblingJournal, which the revert reverses). undo
// imports this package, so the strings are defined here and undo names them.

// Pointer change types and their FieldName.
const (
	// CombineJournalPointerType names a CombineJournal
	// (undo.ChangeTypeMergeCombineJournal).
	CombineJournalPointerType = "merge_combine_journal"
	// SiblingJournalPointerType names a SiblingMoveJournal
	// (undo.ChangeTypeMergeSiblingJournal).
	SiblingJournalPointerType = "merge_sibling_journal"
	// JournalPointerField is the FieldName of both.
	JournalPointerField = "journal"
)

// JournalPointerWriter writes undo-ledger rows. Every database.Store carries
// it; the merge cores resolve it with database.AsCapability so no store
// interface is widened for it.
type JournalPointerWriter interface {
	CreateOperationChange(change *database.OperationChange) error
}

// The production store carries the undo ledger, so the capability lookup
// never refuses a merge the server runs.
var _ JournalPointerWriter = database.Store(nil)

// RecordJournalPointer journals the row that lets POST
// /operations/:id/revert find a merge's own undo record: a
// CombineJournalPointerType row naming a CombineJournal, or a
// SiblingJournalPointerType row naming a SiblingMoveJournal, on bookID (the
// keep / survivor) under opID. Call it after the journal is written and BEFORE
// the merge's first mutation: an error means the merge must be refused.
//
// The returned void marks the row voided (the revert then leaves it out), for
// a merge refused after the row was written but before it changed anything.
func RecordJournalPointer(w JournalPointerWriter, opID, bookID, changeType, journalID string) (void func() error, err error) {
	switch {
	case w == nil:
		return nil, fmt.Errorf("no undo ledger to journal the %s row in", changeType)
	case opID == "":
		return nil, fmt.Errorf("no operation id to journal the %s row under", changeType)
	case changeType != CombineJournalPointerType && changeType != SiblingJournalPointerType:
		return nil, fmt.Errorf("%q is not a merge journal pointer type", changeType)
	case journalID == "" || strings.ContainsAny(journalID, ":/"):
		return nil, fmt.Errorf("%s row: invalid journal id %q", changeType, journalID)
	}
	row := &database.OperationChange{
		ID:          ulid.Make().String(),
		OperationID: opID,
		BookID:      bookID,
		ChangeType:  changeType,
		FieldName:   JournalPointerField,
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

// RecordJournalPointerIn is RecordJournalPointer with the undo ledger
// resolved as a capability of store. A store without one refuses: a merge
// with no pointer has no undo.
func RecordJournalPointerIn(store any, opID, bookID, changeType, journalID string) (func() error, error) {
	w, _ := database.AsCapability[JournalPointerWriter](store)
	return RecordJournalPointer(w, opID, bookID, changeType, journalID)
}

// UndoOperationID returns given, or a fresh correlation id when it is empty:
// the operation a merge's undo pointer is journaled under.
func UndoOperationID(given string) string {
	if given != "" {
		return given
	}
	return ulid.Make().String()
}

// recordJournalPointer is RecordJournalPointerIn over the merge's store.
func (ms *Service) recordJournalPointer(opID, bookID, changeType, journalID string) (func() error, error) {
	return RecordJournalPointerIn(ms.db, opID, bookID, changeType, journalID)
}
