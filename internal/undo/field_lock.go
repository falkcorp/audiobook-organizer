// file: internal/undo/field_lock.go
// version: 1.0.0
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
// and NewValue is FieldLockLocked (OverrideLocked set, no override value).
//
// Restorable. The revert lifts the lock only while the field still carries
// exactly the lock the repair set: no override at all is already restored,
// and an override VALUE (a person typed one since) is a later change the
// revert must not throw away. A lock-only row a person set since cannot be
// told apart from the repair's; lifting it is the price of reverting.
const ChangeTypeFieldLock = "field_lock"

// Values of a ChangeTypeFieldLock row.
const (
	FieldLockUnlocked = "unlocked"
	FieldLockLocked   = "locked"
)

// FieldLockStateOf renders the part of a field's state a ChangeTypeFieldLock
// row records: FieldLockUnlocked when the field carries no user override,
// FieldLockLocked when it carries the bare lock a repair sets, and "" for an
// override with a value (a person's edit), which neither row value matches.
func FieldLockStateOf(st *database.MetadataFieldState) string {
	switch {
	case st == nil || !st.HasUserOverride():
		return FieldLockUnlocked
	case st.OverrideValue == nil:
		return FieldLockLocked
	default:
		return ""
	}
}

// CheckFieldLockCurrent is the compare-and-set for a ChangeTypeFieldLock row,
// shared by the revert and the preflight: nil while the field still carries
// the repair's lock (lift it), ErrAlreadyRestored when it carries no
// override, and a ReasonChangedSince refusal when a person has given it an
// override value since.
func CheckFieldLockCurrent(states []database.MetadataFieldState, c *database.OperationChange) error {
	var cur *database.MetadataFieldState
	for i := range states {
		if states[i].Field == c.FieldName {
			cur = &states[i]
			break
		}
	}
	switch FieldLockStateOf(cur) {
	case c.NewValue:
		return nil
	case c.OldValue:
		return ErrAlreadyRestored
	default:
		return refuse(ReasonChangedSince, "book %s field %s has a user override value since the operation", c.BookID, c.FieldName)
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
