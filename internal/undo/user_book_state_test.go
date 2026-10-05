// file: internal/undo/user_book_state_test.go
// version: 1.0.0
// guid: 35f35ebb-9e54-4962-96ea-2558ca9f3a67
// last-edited: 2026-10-05

package undo

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func ubsRow(t *testing.T, oldS, newS UserStateSnapshot) *database.OperationChange {
	t.Helper()
	ov, err := EncodeUserStateSnapshot(oldS)
	require.NoError(t, err)
	nv, err := EncodeUserStateSnapshot(newS)
	require.NoError(t, err)
	return &database.OperationChange{ID: "c1", OperationID: "op-1", BookID: "b1", ChangeType: ChangeTypeUserBookStateSet,
		FieldName: UserStateField("u1"), OldValue: ov, NewValue: nv}
}

func TestUserBookStateSet_RestorableAndCompareAndSet(t *testing.T) {
	ts := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	old := &database.UserBookState{UserID: "u1", BookID: "b1", Status: database.UserBookStatusInProgress, ProgressPct: 30,
		LastActivityAt: ts.Add(-time.Hour), HideFromContinueListening: true}
	written := *old
	written.Status, written.StatusManual, written.ProgressPct = database.UserBookStatusFinished, true, 100
	written.FinishedAt, written.LastActivityAt = &ts, ts
	pos := []database.UserPosition{{UserID: "u1", BookID: "b1", SegmentID: "abs", PositionSeconds: 60, UpdatedAt: ts.Add(-time.Hour)}}
	c := ubsRow(t, UserStateSnapshot{State: old, Positions: pos}, UserStateSnapshot{State: &written, Positions: pos})
	require.True(t, IsRestorable(c))
	bad := *c
	bad.FieldName = "nobody"
	require.Equal(t, ChangeTypeUserBookStateSet+":(unparsable)", NotRestorableLabel(&bad))
	bad = *c
	bad.NewValue = "{"
	require.False(t, IsRestorable(&bad))

	// Still what the import wrote (the store's UpdatedAt aside): restore the
	// state; the positions were not changed.
	cur := written
	cur.UpdatedAt = ts.Add(time.Minute)
	parts, err := CheckUserBookStateSet(UserStateSnapshot{State: &cur, Positions: pos}, c)
	require.NoError(t, err)
	require.Equal(t, UserStateRestore{State: true}, parts)

	// Back at the old state already.
	_, err = CheckUserBookStateSet(UserStateSnapshot{State: old, Positions: pos}, c)
	require.ErrorIs(t, err, ErrAlreadyRestored)

	// A later listen moved the activity: refused.
	moved := written
	moved.LastActivityAt = ts.Add(time.Hour)
	_, err = CheckUserBookStateSet(UserStateSnapshot{State: &moved, Positions: pos}, c)
	require.Equal(t, ReasonChangedSince, RefusalReason(err))

	// A new position: refused, whatever the state.
	more := append([]database.UserPosition{}, pos...)
	more[0].UpdatedAt = ts.Add(time.Hour)
	_, err = CheckUserBookStateSet(UserStateSnapshot{State: &written, Positions: more}, c)
	require.Equal(t, ReasonChangedSince, RefusalReason(err))

	// A row the import created: restorable to "no row".
	created := ubsRow(t, UserStateSnapshot{}, UserStateSnapshot{State: &written})
	parts, err = CheckUserBookStateSet(UserStateSnapshot{State: &written}, created)
	require.NoError(t, err)
	require.True(t, parts.State)
	_, err = CheckUserBookStateSet(UserStateSnapshot{}, created)
	require.ErrorIs(t, err, ErrAlreadyRestored)
}

// The preflight reads the user's state the way the revert does: a row still
// holding what the import wrote is safe, one the user moved on from is
// refused as changed since.
func TestPreflightUndoConflicts_UserBookStateRows(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ts := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	written := func(book string) *database.UserBookState {
		return &database.UserBookState{UserID: "u1", BookID: book, Status: database.UserBookStatusFinished, StatusManual: true,
			ProgressPct: 100, FinishedAt: &ts, LastActivityAt: ts}
	}
	for _, b := range []string{"b1", "b2"} {
		require.NoError(t, store.SetUserBookState(written(b)))
		c := ubsRow(t, UserStateSnapshot{}, UserStateSnapshot{State: written(b)})
		c.ID, c.BookID = "c-"+b, b
		require.NoError(t, store.CreateOperationChange(c))
	}
	// b2: a device listened after the import.
	require.NoError(t, store.SetUserPositionAt("u1", "b2", "abs", 10, ts.Add(time.Hour)))

	report, err := PreflightUndoConflicts(store, "op-1")
	require.NoError(t, err)
	require.Equal(t, 1, report.Safe, "%+v", report)
	require.Len(t, report.CheckFailed, 1, "%+v", report)
	require.Equal(t, "c-b2", report.CheckFailed[0].ChangeID)
	require.Equal(t, ReasonChangedSince, report.CheckFailed[0].Reason)
}
