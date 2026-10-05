// file: internal/repairs/writer_userstate_test.go
// version: 1.1.0
// guid: 02326bb6-8739-47c5-92ed-341f000b2900
// last-edited: 2026-10-05

package repairs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

var usTS = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func newUSStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func finishedSnap(user, book string, withPos bool) undo.UserStateSnapshot {
	ts := usTS
	s := undo.UserStateSnapshot{State: &database.UserBookState{UserID: user, BookID: book, Status: database.UserBookStatusFinished,
		StatusManual: true, ProgressPct: 100, FinishedAt: &ts, LastActivityAt: ts}}
	if withPos {
		s.Positions = []database.UserPosition{{UserID: user, BookID: book, SegmentID: "abs", PositionSeconds: 3600, UpdatedAt: ts}}
	}
	return s
}

// failingUS fails the chosen write.
type failingUS struct {
	*database.PebbleStore
	failPosition, failState bool
}

func (f failingUS) SetUserPositionAt(u, b, seg string, p float64, at time.Time) error {
	if f.failPosition {
		return errors.New("position write failed")
	}
	return f.PebbleStore.SetUserPositionAt(u, b, seg, p, at)
}

func (f failingUS) SetUserBookState(s *database.UserBookState) error {
	if f.failState {
		return errors.New("state write failed")
	}
	return f.PebbleStore.SetUserBookState(s)
}

func TestWriterSetUserState_JournalsThenWrites(t *testing.T) {
	st := newUSStore(t)
	w := NewWriter(st, st, "fx", "bulk_update", "repairs-").WithJournal(st, st, "op-us").WithUserState(st)
	next := finishedSnap("u1", "b1", true)
	require.NoError(t, w.SetUserState(context.Background(), "u1", "b1", undo.UserStateSnapshot{}, next))
	require.Equal(t, 1, w.Writes())
	require.Equal(t, 1, w.Journaled())

	got, err := st.GetUserBookState("u1", "b1")
	require.NoError(t, err)
	require.True(t, undo.SameUserBookState(next.State, got), "%+v", got)
	pos, err := st.ListUserPositionsForBook("u1", "b1")
	require.NoError(t, err)
	require.True(t, undo.SameUserPositions(next.Positions, pos), "the position keeps its own timestamp: %+v", pos)

	rows, err := st.GetOperationChanges("op-us")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, undo.ChangeTypeUserBookStateSet, rows[0].ChangeType)
	require.Equal(t, undo.UserStateField("u1"), rows[0].FieldName)
	old, err := undo.DecodeUserStateSnapshot(rows[0].OldValue)
	require.NoError(t, err)
	require.Nil(t, old.State)
	require.Empty(t, old.Positions)
}

func TestWriterSetUserState_Refusals(t *testing.T) {
	st := newUSStore(t)
	journal := func(op string) []*database.OperationChange {
		rows, err := st.GetOperationChanges(op)
		require.NoError(t, err)
		return rows
	}
	// The state moved since the decision: nothing journaled or written.
	require.NoError(t, st.SetUserBookState(&database.UserBookState{UserID: "u1", BookID: "moved", Status: database.UserBookStatusInProgress, ProgressPct: 5}))
	w := NewWriter(st, st, "fx", "bulk_update", "repairs-").WithJournal(st, st, "op-a").WithUserState(st)
	err := w.SetUserState(context.Background(), "u1", "moved", undo.UserStateSnapshot{}, finishedSnap("u1", "moved", false))
	require.ErrorIs(t, err, ErrChangedSincePlan)
	require.Empty(t, journal("op-a"))

	// A position with no timestamp is never stamped now.
	bad := finishedSnap("u1", "nots", true)
	bad.Positions[0].UpdatedAt = time.Time{}
	require.Error(t, w.SetUserState(context.Background(), "u1", "nots", undo.UserStateSnapshot{}, bad))
	require.Empty(t, journal("op-a"))
	s, err := st.GetUserBookState("u1", "nots")
	require.NoError(t, err)
	require.Nil(t, s)

	// A state naming another book is refused.
	require.Error(t, w.SetUserState(context.Background(), "u1", "other", undo.UserStateSnapshot{}, finishedSnap("u1", "b1", false)))

	// No journal: ErrNotJournaled, nothing written.
	nj := NewWriter(st, st, "fx", "bulk_update", "repairs-").WithUserState(st)
	require.ErrorIs(t, nj.SetUserState(context.Background(), "u1", "nj", undo.UserStateSnapshot{}, finishedSnap("u1", "nj", false)), ErrNotJournaled)
	s, err = st.GetUserBookState("u1", "nj")
	require.NoError(t, err)
	require.Nil(t, s)

	// A tags-only writer refuses.
	to := NewWriter(st, st, "fx", "bulk_update", "repairs-").WithJournal(st, st, "op-b").WithUserState(st)
	to.restrictToTags()
	require.ErrorIs(t, to.SetUserState(context.Background(), "u1", "to", undo.UserStateSnapshot{}, finishedSnap("u1", "to", false)), ErrTagsOnlyWriter)

	// No user-state store wired.
	none := NewWriter(st, st, "fx", "bulk_update", "repairs-").WithJournal(st, st, "op-c")
	require.Error(t, none.SetUserState(context.Background(), "u1", "x", undo.UserStateSnapshot{}, finishedSnap("u1", "x", false)))
}

// A failed first write leaves nothing and voids the journal row; a failed
// state write after a position landed keeps the row and reports partial.
func TestWriterSetUserState_FailedWrites(t *testing.T) {
	st := newUSStore(t)
	w := NewWriter(st, st, "fx", "bulk_update", "repairs-").WithJournal(st, st, "op-f").
		WithUserState(failingUS{PebbleStore: st, failPosition: true})
	require.Error(t, w.SetUserState(context.Background(), "u1", "b1", undo.UserStateSnapshot{}, finishedSnap("u1", "b1", true)))
	require.Equal(t, 0, w.Journaled())
	rows, err := st.GetOperationChanges("op-f")
	require.NoError(t, err)
	for _, r := range rows {
		require.NotNil(t, r.RevertedAt, "a refused write voids its row")
	}

	w2 := NewWriter(st, st, "fx", "bulk_update", "repairs-").WithJournal(st, st, "op-g").
		WithUserState(failingUS{PebbleStore: st, failState: true})
	err = w2.SetUserState(context.Background(), "u1", "b2", undo.UserStateSnapshot{}, finishedSnap("u1", "b2", true))
	require.ErrorIs(t, err, ErrPartiallyApplied)
	rows, err = st.GetOperationChanges("op-g")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Nil(t, rows[0].RevertedAt, "the position landed: the row stays for the revert")
}

// SetUserState waits for the per-(user, book) lock the ABS write paths and
// the revert hold.
func TestWriterSetUserState_HoldsUserStateLock(t *testing.T) {
	st := newUSStore(t)
	w := NewWriter(st, st, "fx", "bulk_update", "repairs-").WithJournal(st, st, "op-l").WithUserState(st)
	unlock := database.LockUserBookState("u1", "b1")
	done := make(chan error, 1)
	go func() {
		done <- w.SetUserState(context.Background(), "u1", "b1", undo.UserStateSnapshot{}, finishedSnap("u1", "b1", false))
	}()
	select {
	case err := <-done:
		t.Fatalf("SetUserState ran while the lock was held: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	unlock()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("SetUserState never ran after the lock was released")
	}
}

// SetUserState takes the merge lock before the stripe, the revert's order.
func TestWriterSetUserState_TakesMergeLockFirst(t *testing.T) {
	st := newUSStore(t)
	w := NewWriter(st, st, "fx", "bulk_update", "repairs-").WithJournal(st, st, "op-m").WithUserState(st)
	merge.LockMergeRMW()
	done := make(chan error, 1)
	go func() {
		done <- w.SetUserState(context.Background(), "u1", "b1", undo.UserStateSnapshot{}, finishedSnap("u1", "b1", false))
	}()
	select {
	case err := <-done:
		merge.UnlockMergeRMW()
		t.Fatalf("SetUserState ran while the merge lock was held: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	// While it waits for the merge lock it holds no stripe.
	database.LockUserBookState("u1", "b1")()
	merge.UnlockMergeRMW()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("SetUserState never ran after the merge lock was released")
	}

	// A cancelled wait writes nothing.
	merge.LockMergeRMW()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := w.SetUserState(ctx, "u1", "b2", undo.UserStateSnapshot{}, finishedSnap("u1", "b2", false))
	merge.UnlockMergeRMW()
	require.ErrorIs(t, err, context.Canceled)
	s, gerr := st.GetUserBookState("u1", "b2")
	require.NoError(t, gerr)
	require.Nil(t, s)
}
