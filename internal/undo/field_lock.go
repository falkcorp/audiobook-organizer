// file: internal/undo/field_lock.go
// version: 1.1.0
// guid: 4ea3ea92-44d9-4af5-ae13-c7c0ccc3e1c7
// last-edited: 2026-10-03

package undo

import (
	"errors"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ChangeTypeFieldLock: a repair set the lock on one of a book's metadata
// fields (FieldName is the database.FieldKey* it locked) after writing that
// field, so a forced rescan cannot put the file tag's value back
// (scanner.applyScannerFields honours the lock). The title repairs write it:
// the file tags of a book with a junk or swapped title still hold the junk.
//
// OldValue is FieldLockUnlocked (the field carried no override when the
// repair locked it), or FieldLockTakenFrom(src) when the repair took over
// another repair operation's lock (src is that lock's LockSource: an apply
// finishing an interrupted one re-locks under its own id). A repair never
// locks a field a person spoke for. NewValue is FieldLockLocked
// (OverrideLocked set, no override value, LockSource
// database.RepairLockSource(the row's OperationID)).
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
	fieldLockTaken    = "locked:"
)

// FieldLockTakenFrom is the OldValue of a lock taken over from another
// repair operation's lock whose LockSource is src.
func FieldLockTakenFrom(src string) string { return fieldLockTaken + src }

// FieldLockTakenSource returns the LockSource a FieldLockTakenFrom value
// names.
func FieldLockTakenSource(v string) (string, bool) {
	src, ok := strings.CutPrefix(v, fieldLockTaken)
	return src, ok && database.IsRepairLockSource(src)
}

// FieldLockStateOf renders the part of a field's state a ChangeTypeFieldLock
// row of operation opID records: FieldLockUnlocked when the field carries no
// override, FieldLockLocked when it carries the bare lock opID set,
// FieldLockTakenFrom(src) for another repair operation's bare lock, and ""
// for a person's lock or override, which no row value matches.
func FieldLockStateOf(st *database.MetadataFieldState, opID string) string {
	switch {
	case st == nil || !st.HasUserOverride():
		return FieldLockUnlocked
	case st.IsRepairLock() && st.LockSource == database.RepairLockSource(opID):
		return FieldLockLocked
	case st.IsRepairLock():
		return FieldLockTakenFrom(st.LockSource)
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
	if _, taken := FieldLockTakenSource(c.OldValue); (c.OldValue != FieldLockUnlocked && !taken) || c.NewValue != FieldLockLocked {
		return false
	}
	for _, f := range database.UserLockableFields {
		if f.Key == c.FieldName {
			return true
		}
	}
	return false
}

// CheckPairedFieldLock is the extra compare-and-set for a metadata_update row
// of a repair operation that also locked the field it wrote (a
// ChangeTypeFieldLock row of the same operation, book and field in changes).
// The value goes back only while that lock is still the operation's, or
// already gone: a lock a person took over (they set or claimed it since, so
// LockSource no longer names the operation) means the field's value is now
// theirs, and reverting it would leave the junk value under their lock for
// good. Row by row the value check alone cannot see that. Returns nil when
// the row has no paired lock row, or the lock is current or lifted.
func CheckPairedFieldLock(changes []*database.OperationChange, states []database.MetadataFieldState, c *database.OperationChange) error {
	if c.ChangeType != "metadata_update" {
		return nil
	}
	for _, l := range changes {
		if l == nil || l.Voided || l.ChangeType != ChangeTypeFieldLock || l.OperationID != c.OperationID ||
			l.BookID != c.BookID || l.FieldName != c.FieldName {
			continue
		}
		if err := CheckFieldLockCurrent(states, l); err != nil && !errors.Is(err, ErrAlreadyRestored) {
			return refuse(ReasonChangedSince, "book %s %s: the lock this operation set is someone else's now; the value is theirs", c.BookID, c.FieldName)
		}
	}
	return nil
}
