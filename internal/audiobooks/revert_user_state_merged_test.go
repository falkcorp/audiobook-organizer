// file: internal/audiobooks/revert_user_state_merged_test.go
// version: 1.0.0
// guid: 6e0b3f58-1c92-4a7d-b385-d7f4a9e21c06
// last-edited: 2026-10-23

package audiobooks

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// mergedPair returns a survivor and an account an admin merged into it (the
// storage half of usermerge: the absorbed row is locked, then retired).
func mergedPair(t *testing.T, st *database.PebbleStore) (survivor, absorbed *database.User) {
	t.Helper()
	var err error
	survivor, err = st.CreateUser("survivor-acct", "survivor@example.com", "bcrypt", "h1", []string{"admin"}, "active")
	require.NoError(t, err)
	absorbed, err = st.CreateUser("gone-acct", "gone@example.com", "bcrypt", "h2", []string{"admin"}, "active")
	require.NoError(t, err)
	_, err = st.SetUserStatus(absorbed.ID, "locked")
	require.NoError(t, err)
	absorbed, err = st.GetUserByID(absorbed.ID)
	require.NoError(t, err)
	survivor, err = st.GetUserByID(survivor.ID)
	require.NoError(t, err)
	fields := func(u *database.User) database.UserIdentityFields {
		return database.UserIdentityFields{Roles: u.Roles, Email: u.Email, EmailSource: u.EmailSource}
	}
	after := fields(absorbed)
	after.MergedIntoUserID = survivor.ID
	require.NoError(t, st.RetireMergedUser(database.UserRetirement{
		SurvivorID: survivor.ID, AbsorbedID: absorbed.ID,
		SurvivorBefore: fields(survivor), SurvivorAfter: fields(survivor),
		AbsorbedBefore: fields(absorbed), AbsorbedAfter: after,
	}))
	return survivor, absorbed
}

func finishedFor(user, book string, at time.Time) undo.UserStateSnapshot {
	ts := at
	return undo.UserStateSnapshot{
		State: &database.UserBookState{UserID: user, BookID: book, Status: database.UserBookStatusFinished,
			StatusManual: true, ProgressPct: 100, FinishedAt: &ts, LastActivityAt: ts, LastSegmentID: "abs"},
		Positions: []database.UserPosition{{UserID: user, BookID: book, SegmentID: "abs", PositionSeconds: 3600, UpdatedAt: ts}},
	}
}

func journalFor(t *testing.T, st *database.PebbleStore, op, user, book string, newS undo.UserStateSnapshot) {
	t.Helper()
	ov, err := undo.EncodeUserStateSnapshot(undo.UserStateSnapshot{})
	require.NoError(t, err)
	nv, err := undo.EncodeUserStateSnapshot(newS)
	require.NoError(t, err)
	require.NoError(t, st.CreateOperationChange(&database.OperationChange{ID: op + "-" + book, OperationID: op, BookID: book,
		ChangeType: undo.ChangeTypeUserBookStateSet, FieldName: undo.UserStateField(user), OldValue: ov, NewValue: nv}))
}

func carryTo(t *testing.T, st *database.PebbleStore, from, to string, snap undo.UserStateSnapshot) {
	t.Helper()
	moved := *snap.State
	moved.UserID = to
	require.NoError(t, st.SetUserBookState(&moved))
	for _, p := range snap.Positions {
		require.NoError(t, st.SetUserPositionAt(to, p.BookID, p.SegmentID, p.PositionSeconds, p.UpdatedAt))
	}
	_ = from
}

// An ABS import wrote a listening state for an account an admin has since
// merged into another. The merge carried the state to the survivor, so the
// import's undo finds it there and takes it off, instead of looking at the
// emptied account and calling it changed.
func TestRevertUserBookStateSet_AfterTheAccountWasMerged(t *testing.T) {
	st := rusStore(t)
	survivor, absorbed := mergedPair(t, st)
	snap := finishedFor(absorbed.ID, "bk", rusTS)
	journalFor(t, st, "op1", absorbed.ID, "bk", snap)
	carryTo(t, st, absorbed.ID, survivor.ID, snap)

	res, err := NewRevertService(st).RevertOperation("op1")
	require.NoError(t, err, "%+v", res)
	require.Equal(t, 1, res.Restored, "%+v", res)

	s, err := st.GetUserBookState(survivor.ID, "bk")
	require.NoError(t, err)
	require.Nil(t, s, "the carried state is taken off the account that holds it now")
	pos, err := st.ListUserPositionsForBook(survivor.ID, "bk")
	require.NoError(t, err)
	require.Empty(t, pos)
	s, err = st.GetUserBookState(absorbed.ID, "bk")
	require.NoError(t, err)
	require.Nil(t, s, "nothing is written back onto the merged-away account")
}

// The survivor listened to the book after the merge: that is the user's and is
// kept, exactly as a later listen is kept for an account that was never merged.
func TestRevertUserBookStateSet_AfterTheAccountWasMergedAndListenedSince(t *testing.T) {
	st := rusStore(t)
	survivor, absorbed := mergedPair(t, st)
	snap := finishedFor(absorbed.ID, "bk", rusTS)
	journalFor(t, st, "op1", absorbed.ID, "bk", snap)
	carryTo(t, st, absorbed.ID, survivor.ID, snap)
	require.NoError(t, st.SetUserPositionAt(survivor.ID, "bk", "abs", 120, rusTS.Add(time.Hour)))

	res, err := NewRevertService(st).RevertOperation("op1")
	require.Error(t, err)
	require.Equal(t, 0, res.Restored, "%+v", res)
	s, err := st.GetUserBookState(survivor.ID, "bk")
	require.NoError(t, err)
	require.NotNil(t, s, "a refused row is left as it is")
}
