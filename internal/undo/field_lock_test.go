// file: internal/undo/field_lock_test.go
// version: 1.0.0
// guid: 200d0f84-651c-4656-bbfe-3747474a198f
// last-edited: 2026-10-03

package undo

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestFieldLock_RestorableAndCompareAndSet(t *testing.T) {
	c := &database.OperationChange{ID: "c1", BookID: "b1", ChangeType: ChangeTypeFieldLock,
		FieldName: database.FieldKeyTitle, OldValue: FieldLockUnlocked, NewValue: FieldLockLocked}
	require.True(t, IsRestorable(c))
	bad := *c
	bad.FieldName = "not_a_field"
	require.Equal(t, ChangeTypeFieldLock+":(unparsable)", NotRestorableLabel(&bad))
	bad = *c
	bad.NewValue = "x"
	require.False(t, IsRestorable(&bad))

	fv := `"Provider"`
	ov := `"Typed"`
	// Still the repair's bare lock: lift it.
	require.NoError(t, CheckFieldLockCurrent([]database.MetadataFieldState{
		{Field: "title", FetchedValue: &fv, OverrideLocked: true}}, c))
	// No override (no row, or unlocked): already restored.
	require.ErrorIs(t, CheckFieldLockCurrent(nil, c), ErrAlreadyRestored)
	require.ErrorIs(t, CheckFieldLockCurrent([]database.MetadataFieldState{{Field: "title", FetchedValue: &fv}}, c), ErrAlreadyRestored)
	// Another field's lock is not this row's.
	require.ErrorIs(t, CheckFieldLockCurrent([]database.MetadataFieldState{{Field: "author_name", OverrideLocked: true}}, c), ErrAlreadyRestored)
	// A person typed a value since: refused, never thrown away.
	err := CheckFieldLockCurrent([]database.MetadataFieldState{{Field: "title", OverrideValue: &ov, OverrideLocked: true}}, c)
	require.Equal(t, ReasonChangedSince, RefusalReason(err))
}
