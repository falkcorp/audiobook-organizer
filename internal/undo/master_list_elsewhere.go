// file: internal/undo/master_list_elsewhere.go
// version: 1.0.0
// guid: c3a91e57-48d2-4f06-b7e1-9d05a2f6c814
// last-edited: 2026-10-23

package undo

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ChangeTypeMasterListElsewhereCreate: the master-list import created one
// read-elsewhere record (database.MasterListElsewhere) for a list row that
// matches no library book. FieldName is ElsewhereFieldPrefix + the record id,
// OldValue is ElsewhereAbsent (a create only ever makes a record that did not
// exist), NewValue the record's JSON as created.
//
// The row is journaled on no book (BookID is empty): the record names none.
//
// Restorable. The revert deletes the record while it is still exactly what
// the apply created (database.SameElsewhere ignores only the creation time);
// a record that is gone is already restored.
const ChangeTypeMasterListElsewhereCreate = "master_list_elsewhere_create"

// ElsewhereAbsent is the OldValue of a ChangeTypeMasterListElsewhereCreate row.
const ElsewhereAbsent = "absent"

// ElsewhereFieldPrefix starts the FieldName of a ChangeTypeMasterListElsewhereCreate row.
const ElsewhereFieldPrefix = "elsewhere:"

// ElsewhereField is the FieldName of the create row for a record id.
func ElsewhereField(id string) string { return ElsewhereFieldPrefix + id }

// DecodeMasterListElsewhereCreate parses a ChangeTypeMasterListElsewhereCreate
// row into the record the apply created. The record's own id must be the one
// the field names.
func DecodeMasterListElsewhereCreate(c *database.OperationChange) (database.MasterListElsewhere, error) {
	var rec database.MasterListElsewhere
	id, ok := strings.CutPrefix(c.FieldName, ElsewhereFieldPrefix)
	if !ok || id == "" || c.OldValue != ElsewhereAbsent {
		return rec, &ReferentError{Reason: ReasonOldValueUnparsable, Detail: fmt.Sprintf("read-elsewhere create change %s", c.ID)}
	}
	if err := json.Unmarshal([]byte(c.NewValue), &rec); err != nil || rec.ID != id {
		return rec, &ReferentError{Reason: ReasonOldValueUnparsable, Detail: fmt.Sprintf("created record of change %s", c.ID)}
	}
	return rec, nil
}

// CheckMasterListElsewhereCreate is the compare-and-set for a
// ChangeTypeMasterListElsewhereCreate row: nil while cur is still the record
// the row created (delete it), ErrAlreadyRestored when there is none (cur nil),
// and a ReasonChangedSince refusal when it is not the record the apply made.
func CheckMasterListElsewhereCreate(cur *database.MasterListElsewhere, want database.MasterListElsewhere) error {
	if cur == nil {
		return ErrAlreadyRestored
	}
	if !database.SameElsewhere(*cur, want) {
		return refuse(ReasonChangedSince, "read-elsewhere record %s is no longer what the import created", want.ID)
	}
	return nil
}
