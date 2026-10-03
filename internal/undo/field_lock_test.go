// file: internal/undo/field_lock_test.go
// version: 1.1.0
// guid: 200d0f84-651c-4656-bbfe-3747474a198f
// last-edited: 2026-10-03

package undo

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestFieldLock_RestorableAndCompareAndSet(t *testing.T) {
	c := &database.OperationChange{ID: "c1", OperationID: "op-1", BookID: "b1", ChangeType: ChangeTypeFieldLock,
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
	src := database.RepairLockSource("op-1")
	// Still this operation's bare lock: lift it.
	require.NoError(t, CheckFieldLockCurrent([]database.MetadataFieldState{
		{Field: "title", FetchedValue: &fv, OverrideLocked: true, LockSource: src}}, c))
	// A person's bare lock (no source) or another operation's: refused.
	for _, other := range []string{"", database.RepairLockSource("op-2")} {
		err := CheckFieldLockCurrent([]database.MetadataFieldState{{Field: "title", OverrideLocked: true, LockSource: other}}, c)
		require.Equal(t, ReasonChangedSince, RefusalReason(err), "source %q", other)
	}
	// No override (no row, or unlocked): already restored.
	require.ErrorIs(t, CheckFieldLockCurrent(nil, c), ErrAlreadyRestored)
	require.ErrorIs(t, CheckFieldLockCurrent([]database.MetadataFieldState{{Field: "title", FetchedValue: &fv}}, c), ErrAlreadyRestored)
	// Another field's lock is not this row's.
	require.ErrorIs(t, CheckFieldLockCurrent([]database.MetadataFieldState{{Field: "author_name", OverrideLocked: true}}, c), ErrAlreadyRestored)
	// A person typed a value since: refused, never thrown away.
	err := CheckFieldLockCurrent([]database.MetadataFieldState{{Field: "title", OverrideValue: &ov, OverrideLocked: true}}, c)
	require.Equal(t, ReasonChangedSince, RefusalReason(err))
}

// A value whose paired repair lock a person took over (claimed, or locked by
// hand: LockSource cleared) stays: the title row is refused while the lock
// row is too, so the junk value never ends up under the person's lock.
func TestCheckPairedFieldLock(t *testing.T) {
	titleRow := &database.OperationChange{OperationID: "op-1", BookID: "b", ChangeType: "metadata_update",
		FieldName: "title", OldValue: "read by X", NewValue: "Real Title"}
	lockRow := &database.OperationChange{OperationID: "op-1", BookID: "b", ChangeType: ChangeTypeFieldLock,
		FieldName: "title", OldValue: FieldLockUnlocked, NewValue: FieldLockLocked}
	locks := []*database.OperationChange{lockRow}
	claimed := []database.MetadataFieldState{{BookID: "b", Field: "title", OverrideLocked: true}}
	require.Equal(t, ReasonChangedSince, RefusalReason(CheckPairedFieldLock(locks, claimed, titleRow)))
	taken := []database.MetadataFieldState{{BookID: "b", Field: "title", OverrideLocked: true, LockSource: database.RepairLockSource("op-2")}}
	require.Equal(t, ReasonChangedSince, RefusalReason(CheckPairedFieldLock(locks, taken, titleRow)), "a later op's lock")
	own := []database.MetadataFieldState{{BookID: "b", Field: "title", OverrideLocked: true, LockSource: database.RepairLockSource("op-1")}}
	require.NoError(t, CheckPairedFieldLock(locks, own, titleRow), "still the op's lock")
	require.NoError(t, CheckPairedFieldLock(locks, nil, titleRow), "lock already lifted")
	require.NoError(t, CheckPairedFieldLock(nil, claimed, titleRow), "no paired lock row")
	voided := *lockRow
	voided.Voided = true
	require.NoError(t, CheckPairedFieldLock([]*database.OperationChange{&voided}, claimed, titleRow), "a voided lock row never happened")

	// A taken-over lock's row is restorable and renders against its source.
	take := &database.OperationChange{ID: "c2", OperationID: "op-2", BookID: "b", ChangeType: ChangeTypeFieldLock,
		FieldName: "title", OldValue: FieldLockTakenFrom(database.RepairLockSource("op-1")), NewValue: FieldLockLocked}
	require.True(t, IsRestorable(take))
	require.NoError(t, CheckFieldLockCurrent(taken, take))
	require.ErrorIs(t, CheckFieldLockCurrent(own, take), ErrAlreadyRestored)
}
