// file: internal/repairs/writer_locks.go
// version: 1.0.0
// guid: 37c8abdf-afee-4662-8fc6-4e18d4194825
// last-edited: 2026-10-03

package repairs

import (
	"errors"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metastate"
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
// the rows and the pre-migration blob (database.MetadataFieldStateReader), and
// the row write.
type FieldStateStore interface {
	database.MetadataFieldStateReader
	UpsertMetadataFieldState(state *database.MetadataFieldState) error
}

// WithFieldStates wires w to lock fields through store. Without it LockFields
// fails.
func (w *Writer) WithFieldStates(store FieldStateStore) *Writer {
	w.fieldStates = store
	return w
}

// LockFields sets the user lock (OverrideLocked, no override value) on each
// key of bookID, a database.FieldKey*. Each lock is journaled BEFORE it is
// written; a crash between the two leaves a row whose revert finds the field
// unlocked and counts it already restored.
//
// A field that already carries a user override is refused as
// ErrChangedSincePlan: the repairs that lock refuse a locked field at plan
// time, so one now is a person's act since. A book whose state still lives
// only in the pre-migration blob is refused too: writing its first row would
// hide every lock the blob holds (database.LockedUserFields reads the blob
// only while a book has no rows).
//
// The row keeps its provider value and its UpdatedAt: the time is when the
// provider recorded the value, which the swapped title/author fixer compares
// across fields.
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
	cur, err := w.fieldState(bookID, key)
	if err != nil {
		return err
	}
	if cur == nil {
		pref, perr := w.fieldStates.GetUserPreference(metastate.Key(bookID))
		if perr != nil {
			return fmt.Errorf("read pre-migration metadata state of %s: %w", bookID, perr)
		}
		if pref != nil && pref.Value != nil && *pref.Value != "" {
			return fmt.Errorf("book %s keeps its field state in the pre-migration blob; open the book once to migrate it, then re-plan", bookID)
		}
	}
	if undo.FieldLockStateOf(cur) != undo.FieldLockUnlocked {
		return fmt.Errorf("%w: book %s %s carries a user override", ErrChangedSincePlan, bookID, key)
	}
	if err := w.Journal(bookID, undo.ChangeTypeFieldLock, key, undo.FieldLockUnlocked, undo.FieldLockLocked); err != nil {
		return err
	}
	// Re-read right before the write: a person's override landing since the
	// read above is kept, never overwritten by the bare lock. Field-state
	// writes take no lock, so this narrows the window rather than closing it.
	cur, err = w.fieldState(bookID, key)
	if err != nil {
		return err
	}
	st := database.MetadataFieldState{BookID: bookID, Field: key}
	if cur != nil {
		if cur.HasUserOverride() {
			return fmt.Errorf("%w: book %s %s was given a user override during the apply", ErrChangedSincePlan, bookID, key)
		}
		st = *cur
	}
	st.OverrideLocked = true
	if err := w.fieldStates.UpsertMetadataFieldState(&st); err != nil {
		return fmt.Errorf("lock %s of %s: %w", key, bookID, err)
	}
	w.writes.Add(1)
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
