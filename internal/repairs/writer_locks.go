// file: internal/repairs/writer_locks.go
// version: 1.1.0
// guid: 37c8abdf-afee-4662-8fc6-4e18d4194825
// last-edited: 2026-10-03

package repairs

import (
	"errors"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// A repair that corrects a field the scanner also writes from file tags (the
// title, the author) leaves the tags as they were: the Repairs lane writes the
// database, never a file. A forced rescan, or a book flagged for one,
// re-reads the tags and scanner.applyScannerFields writes them back over the
// repair unless the field is locked. LockFields sets that lock, journaled
// under the apply op as undo.ChangeTypeFieldLock, so the op revert lifts it
// with the rest of the row.

// FieldStateStore is the per-field metadata state surface LockFields needs:
// the rows and the pre-migration blob (database.MetadataFieldStateReader), the
// row write, and the blob delete the migration ends with.
type FieldStateStore interface {
	database.LegacyMetadataStateStore
}

// WithFieldStates wires w to lock fields through store. Without it LockFields
// fails.
func (w *Writer) WithFieldStates(store FieldStateStore) *Writer {
	w.fieldStates = store
	return w
}

// LockFields sets a repair's lock on each key of bookID, a
// database.FieldKey*: OverrideLocked, no override value, and LockSource
// database.RepairLockSource(<this op>), so the op revert lifts exactly this
// lock and never one a person set (undo.CheckFieldLockCurrent), and a metadata
// apply a person picks by hand may still overwrite the field.
//
// Each lock is journaled BEFORE it is written. A lock refused after its row
// was journaled (a person's override landed meanwhile, or the write failed)
// voids that row. A crash between the row and the write leaves a row whose
// revert finds no lock of this op and counts it already restored.
//
// A field that already carries a user override, or any lock, is refused as
// ErrChangedSincePlan: the repairs that lock refuse a locked field at plan
// time, so one now is someone's act since. A book whose state still lives
// only in the pre-migration blob is migrated to rows first
// (database.MigrateLegacyMetadataState): writing its first row over the blob
// would hide every lock the blob holds. A blob that cannot be migrated is an
// error; the fixers hold such a book at plan time (before any write).
//
// The read, re-check and write run under the book's field-state stripe
// (database.LockMetadataState), which the metadata services' snapshot save
// also takes, so a snapshot read before the lock cannot erase it.
//
// The row keeps its provider value and its UpdatedAt: the time is when the
// field's value was last recorded (a fetch, a person's edit), which the
// swapped title/author fixer compares across fields.
func (w *Writer) LockFields(bookID string, keys ...string) error {
	if w.fieldStates == nil {
		return errors.New("repairs: writer has no field-state store")
	}
	for _, key := range keys {
		if err := w.lockField(bookID, key); err != nil {
			return err
		}
	}
	return nil
}

func (w *Writer) lockField(bookID, key string) error {
	lockable := false
	for _, f := range database.UserLockableFields {
		lockable = lockable || f.Key == key
	}
	if !lockable {
		return fmt.Errorf("repairs: %q is not a lockable field", key)
	}
	source := database.RepairLockSource(w.opID)
	cur, err := w.fieldState(bookID, key)
	if err != nil {
		return err
	}
	if cur != nil && cur.HasUserOverride() {
		if cur.IsRepairLock() && cur.LockSource == source {
			return nil // this op locked it already (a resumed run)
		}
		return fmt.Errorf("%w: book %s %s carries a lock or user override", ErrChangedSincePlan, bookID, key)
	}
	row, err := w.journalRow(bookID, undo.ChangeTypeFieldLock, key, undo.FieldLockUnlocked, undo.FieldLockLocked)
	if err != nil {
		return err
	}
	if werr := w.writeLock(bookID, key, source); werr != nil {
		if verr := w.voidRow(row); verr != nil {
			return errors.Join(werr, verr)
		}
		return werr
	}
	w.writes.Add(1)
	return nil
}

// writeLock migrates a blob-only book, re-reads the field and sets the lock,
// all under the book's field-state stripe.
func (w *Writer) writeLock(bookID, key, source string) error {
	unlock := database.LockMetadataState(bookID)
	defer unlock()
	if _, err := database.MigrateLegacyMetadataState(w.fieldStates, bookID); err != nil {
		return fmt.Errorf("book %s: migrate its pre-migration metadata state before locking %s: %w", bookID, key, err)
	}
	cur, err := w.fieldState(bookID, key)
	if err != nil {
		return err
	}
	st := database.MetadataFieldState{BookID: bookID, Field: key}
	if cur != nil {
		if cur.HasUserOverride() {
			return fmt.Errorf("%w: book %s %s was locked or given an override during the apply", ErrChangedSincePlan, bookID, key)
		}
		st = *cur
	}
	st.OverrideLocked = true
	st.LockSource = source
	if err := w.fieldStates.UpsertMetadataFieldState(&st); err != nil {
		return fmt.Errorf("lock %s of %s: %w", key, bookID, err)
	}
	return nil
}

// fieldState returns bookID's state row for key, or nil when it has none.
func (w *Writer) fieldState(bookID, key string) (*database.MetadataFieldState, error) {
	states, err := w.fieldStates.GetMetadataFieldStates(bookID)
	if err != nil {
		return nil, fmt.Errorf("read field states of %s: %w", bookID, err)
	}
	for i := range states {
		if states[i].Field == key {
			return &states[i], nil
		}
	}
	return nil, nil
}
