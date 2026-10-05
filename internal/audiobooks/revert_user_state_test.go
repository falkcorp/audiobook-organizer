// file: internal/audiobooks/revert_user_state_test.go
// version: 1.1.0
// guid: 4b5ee81e-d142-4889-b411-a97b06ed7baa
// last-edited: 2026-10-05

package audiobooks

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

var rusTS = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func rusStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func rusRow(t *testing.T, st *database.PebbleStore, op, book string, oldS, newS undo.UserStateSnapshot) {
	t.Helper()
	ov, err := undo.EncodeUserStateSnapshot(oldS)
	require.NoError(t, err)
	nv, err := undo.EncodeUserStateSnapshot(newS)
	require.NoError(t, err)
	require.NoError(t, st.CreateOperationChange(&database.OperationChange{ID: op + "-" + book, OperationID: op, BookID: book,
		ChangeType: undo.ChangeTypeUserBookStateSet, FieldName: undo.UserStateField("u1"), OldValue: ov, NewValue: nv}))
}

// write puts snap into the store as the import would have left it.
func rusWrite(t *testing.T, st *database.PebbleStore, book string, snap undo.UserStateSnapshot) {
	t.Helper()
	if snap.State != nil {
		s := *snap.State
		require.NoError(t, st.SetUserBookState(&s))
	}
	for _, p := range snap.Positions {
		require.NoError(t, st.SetUserPositionAt("u1", book, p.SegmentID, p.PositionSeconds, p.UpdatedAt))
	}
}

func rusFinished(book string, pos bool) undo.UserStateSnapshot {
	ts := rusTS
	s := undo.UserStateSnapshot{State: &database.UserBookState{UserID: "u1", BookID: book, Status: database.UserBookStatusFinished,
		StatusManual: true, ProgressPct: 100, FinishedAt: &ts, LastActivityAt: ts, LastSegmentID: "abs"}}
	if pos {
		s.Positions = []database.UserPosition{{UserID: "u1", BookID: book, SegmentID: "abs", PositionSeconds: 3600, UpdatedAt: ts}}
	}
	return s
}

func TestRevertUserBookStateSet(t *testing.T) {
	st := rusStore(t)
	older := rusTS.Add(-time.Hour)

	// created: the import made the row and an end position.
	created := rusFinished("created", true)
	rusWrite(t, st, "created", created)
	rusRow(t, st, "op1", "created", undo.UserStateSnapshot{}, created)

	// changed: an in-progress row with a hide flag and its own position.
	oldState := &database.UserBookState{UserID: "u1", BookID: "changed", Status: database.UserBookStatusInProgress, ProgressPct: 30,
		LastActivityAt: older, HideFromContinueListening: true, LastSegmentID: "abs", TotalListenedSeconds: 900}
	oldPos := []database.UserPosition{{UserID: "u1", BookID: "changed", SegmentID: "abs", PositionSeconds: 900, UpdatedAt: older}}
	rusWrite(t, st, "changed", undo.UserStateSnapshot{State: oldState, Positions: oldPos})
	changedNew := rusFinished("changed", false)
	changedNew.State.HideFromContinueListening, changedNew.State.TotalListenedSeconds = true, 900
	changedNew.Positions = oldPos
	rusWrite(t, st, "changed", changedNew)
	rusRow(t, st, "op1", "changed", undo.UserStateSnapshot{State: oldState, Positions: oldPos}, changedNew)

	// listened: the device synced after the import.
	listened := rusFinished("listened", true)
	rusWrite(t, st, "listened", listened)
	rusRow(t, st, "op1", "listened", undo.UserStateSnapshot{}, listened)
	require.NoError(t, st.SetUserPositionAt("u1", "listened", "abs", 120, rusTS.Add(time.Hour)))

	// already: someone put it back already.
	already := rusFinished("already", false)
	rusRow(t, st, "op1", "already", undo.UserStateSnapshot{}, already)

	res, err := NewRevertService(st).RevertOperation("op1")
	require.ErrorContains(t, err, "positions changed after the import")
	require.Equal(t, 3, res.Restored, "%+v", res)
	require.Equal(t, 1, res.Failed, "%+v", res)

	s, err := st.GetUserBookState("u1", "created")
	require.NoError(t, err)
	require.Nil(t, s, "a row the import created is deleted")
	pos, err := st.ListUserPositionsForBook("u1", "created")
	require.NoError(t, err)
	require.Empty(t, pos)
	fin, err := st.ListUserBookStatesByStatus("u1", database.UserBookStatusFinished, 10, 0)
	require.NoError(t, err)
	for _, f := range fin {
		require.NotEqual(t, "created", f.BookID, "no status index entry is left behind")
	}

	s, err = st.GetUserBookState("u1", "changed")
	require.NoError(t, err)
	require.True(t, undo.SameUserBookState(oldState, s), "%+v", s)
	pos, err = st.ListUserPositionsForBook("u1", "changed")
	require.NoError(t, err)
	require.True(t, undo.SameUserPositions(oldPos, pos))

	s, err = st.GetUserBookState("u1", "listened")
	require.NoError(t, err)
	require.Equal(t, database.UserBookStatusFinished, s.Status, "the refused row is left as it is")
	pos, err = st.ListUserPositionsForBook("u1", "listened")
	require.NoError(t, err)
	require.Len(t, pos, 1)
	require.InDelta(t, 120, pos[0].PositionSeconds, 0.1)
}

// A store that cannot delete a state or write a timestamped position is
// refused, not half-reverted.
func TestRevertUserBookStateSet_NeedsCapabilities(t *testing.T) {
	st := rusStore(t)
	created := rusFinished("b1", false)
	rusWrite(t, st, "b1", created)
	rusRow(t, st, "op2", "b1", undo.UserStateSnapshot{}, created)
	type narrow struct{ database.Store }
	_, err := NewRevertService(narrow{st}).RevertOperation("op2")
	require.Error(t, err)
	s, gerr := st.GetUserBookState("u1", "b1")
	require.NoError(t, gerr)
	require.Equal(t, database.UserBookStatusFinished, s.Status)
}

// The revert of a user_book_state_set row waits for the per-(user, book)
// user-state stripe the ABS write paths and the Repairs writer hold.
func TestRevertUserBookStateSet_HoldsUserStateLock(t *testing.T) {
	st := rusStore(t)
	created := rusFinished("b1", false)
	rusWrite(t, st, "b1", created)
	rusRow(t, st, "op3", "b1", undo.UserStateSnapshot{}, created)
	unlock := database.LockUserBookState("u1", "b1")
	done := make(chan error, 1)
	go func() {
		_, err := NewRevertService(st).RevertOperation("op3")
		done <- err
	}()
	select {
	case err := <-done:
		unlock()
		t.Fatalf("the revert ran while the user-state lock was held: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	unlock()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("the revert never ran after the lock was released")
	}
	s, err := st.GetUserBookState("u1", "b1")
	require.NoError(t, err)
	require.Nil(t, s)
}
