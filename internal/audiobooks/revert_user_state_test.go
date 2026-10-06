// file: internal/audiobooks/revert_user_state_test.go
// version: 1.2.0
// guid: 4b5ee81e-d142-4889-b411-a97b06ed7baa
// last-edited: 2026-10-06

package audiobooks

import (
	"errors"
	"sync"
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

// rusFaultStore injects position-write failures: every per-row
// SetUserPositionAt after the first fails (so a clear-then-write-each-row
// restore stops half way), and ReplaceUserPositions fails without writing
// when failReplace is set.
type rusFaultStore struct {
	*database.PebbleStore
	failReplace bool
	mu          sync.Mutex
	rowWrites   int
}

func (s *rusFaultStore) SetUserPositionAt(userID, bookID, seg string, sec float64, at time.Time) error {
	s.mu.Lock()
	s.rowWrites++
	n := s.rowWrites
	s.mu.Unlock()
	if n > 1 {
		return errors.New("injected position write failure")
	}
	return s.PebbleStore.SetUserPositionAt(userID, bookID, seg, sec, at)
}

func (s *rusFaultStore) ReplaceUserPositions(userID, bookID string, positions []database.UserPosition) error {
	if s.failReplace {
		return errors.New("injected replace failure")
	}
	return s.PebbleStore.ReplaceUserPositions(userID, bookID, positions)
}

// rusPositionsRow sets up a revertable row whose positions part changed:
// before the import two older rows, after it the import's single end row.
func rusPositionsRow(t *testing.T, st *database.PebbleStore, op string) (oldPos, newPos []database.UserPosition) {
	t.Helper()
	older := rusTS.Add(-time.Hour)
	oldPos = []database.UserPosition{
		{UserID: "u1", BookID: "b1", SegmentID: "s1", PositionSeconds: 900, UpdatedAt: older},
		{UserID: "u1", BookID: "b1", SegmentID: "s2", PositionSeconds: 100, UpdatedAt: older},
	}
	oldState := &database.UserBookState{UserID: "u1", BookID: "b1", Status: database.UserBookStatusInProgress, ProgressPct: 30,
		LastActivityAt: older, LastSegmentID: "s1", TotalListenedSeconds: 1000}
	rusWrite(t, st, "b1", undo.UserStateSnapshot{State: oldState, Positions: oldPos})
	require.NoError(t, st.ClearUserPositions("u1", "b1"))
	newSnap := rusFinished("b1", true)
	rusWrite(t, st, "b1", newSnap)
	rusRow(t, st, op, "b1", undo.UserStateSnapshot{State: oldState, Positions: oldPos}, newSnap)
	return oldPos, newSnap.Positions
}

// Item 3 (#3777 review): the positions go back in one atomic replace. With
// the old clear-then-write-each-row restore, a failing second row write left
// the book with its current rows cleared and only one of the old rows.
func TestRevertUserBookStateSet_PositionsRestoredAtomically(t *testing.T) {
	st := rusStore(t)
	oldPos, _ := rusPositionsRow(t, st, "op4")
	fs := &rusFaultStore{PebbleStore: st}

	res, err := NewRevertService(fs).RevertOperation("op4")
	require.NoError(t, err, "%+v", res)
	pos, err := st.ListUserPositionsForBook("u1", "b1")
	require.NoError(t, err)
	require.True(t, undo.SameUserPositions(oldPos, pos), "both old rows back with their timestamps: %+v", pos)
}

// Item 3: a restore whose write fails leaves the positions currently on the
// book (the import's) exactly as they are -- never cleared, never partial --
// and the row is reported failed.
func TestRevertUserBookStateSet_FailedRestoreLeavesCurrentPositions(t *testing.T) {
	st := rusStore(t)
	_, newPos := rusPositionsRow(t, st, "op5")
	fs := &rusFaultStore{PebbleStore: st, failReplace: true}

	res, err := NewRevertService(fs).RevertOperation("op5")
	require.Error(t, err)
	require.Equal(t, 1, res.Failed, "%+v", res)
	pos, gerr := st.ListUserPositionsForBook("u1", "b1")
	require.NoError(t, gerr)
	require.True(t, undo.SameUserPositions(newPos, pos), "the current positions are untouched: %+v", pos)
}
