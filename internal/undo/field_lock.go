// file: internal/undo/field_lock.go
// version: 1.1.0
// guid: 4ea3ea92-44d9-4af5-ae13-c7c0ccc3e1c7
// last-edited: 2026-10-03

package undo

import (
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ChangeTypeFieldLock: a repair set the lock on one of a book's metadata
// fields (FieldName is the database.FieldKey* it locked) after writing that
// field, so a forced rescan cannot put the file tag's value back
// (scanner.applyScannerFields honours the lock). The title repairs write it:
// the file tags of a book with a junk or swapped title still hold the junk.
//
// OldValue is FieldLockUnlocked (the field carried no user override when the
// repair locked it: a repair never locks a field a person already spoke for)
// and NewValue is FieldLockLocked (OverrideLocked set, no override value,
// LockSource database.RepairLockSource(the row's OperationID)).
//
// Restorable. The revert lifts the lock only while the field still carries
// exactly the lock this operation set: no override at all is already
// restored, and anything else (a person's override value, a person's lock,
// which clears LockSource, or another operation's lock) is a later change the
// revert must not throw away.
const ChangeTypeFieldLock = "field_lock"

// Values of a ChangeTypeFieldLock row.
const (
	FieldLockUnlocked = "unlocked"
	FieldLockLocked   = "locked"
)

// FieldLockStateOf renders the part of a field's state a ChangeTypeFieldLock
// row of operation opID records: FieldLockUnlocked when the field carries no
// user override, FieldLockLocked when it carries the bare lock opID set, and
// "" for anything else, which neither row value matches.
func FieldLockStateOf(st *database.MetadataFieldState, opID string) string {
	switch {
	case st == nil || !st.HasUserOverride():
		return FieldLockUnlocked
	case st.IsRepairLock() && st.LockSource == database.RepairLockSource(opID):
		return FieldLockLocked
	default:
		return ""
	}
}

// CheckFieldLockCurrent is the compare-and-set for a ChangeTypeFieldLock row,
// shared by the revert and the preflight: nil while the field still carries
// the lock the row's operation set (lift it), ErrAlreadyRestored when it
// carries no override, and a ReasonChangedSince refusal otherwise.
func CheckFieldLockCurrent(states []database.MetadataFieldState, c *database.OperationChange) error {
	var cur *database.MetadataFieldState
	for i := range states {
		if states[i].Field == c.FieldName {
			cur = &states[i]
			break
		}
	}
	switch FieldLockStateOf(cur, c.OperationID) {
	case c.NewValue:
		return nil
	case c.OldValue:
		return ErrAlreadyRestored
	default:
		return refuse(ReasonChangedSince, "book %s field %s is locked or overridden by someone else since the operation", c.BookID, c.FieldName)
	}
}

// validFieldLockRow reports whether c is a ChangeTypeFieldLock row the revert
// can act on: a lockable field and the two values a repair writes.
func validFieldLockRow(c *database.OperationChange) bool {
	if c.OldValue != FieldLockUnlocked || c.NewValue != FieldLockLocked {
		return false
	}
	for _, f := range database.UserLockableFields {
		if f.Key == c.FieldName {
			return true
		}
	}
	return false
}
