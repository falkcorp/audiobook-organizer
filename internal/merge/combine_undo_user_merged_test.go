// file: internal/merge/combine_undo_user_merged_test.go
// version: 1.0.0
// guid: 8c4d1e07-2f65-4b93-a0d8-51e7b3c96a24
// last-edited: 2026-10-23

package merge

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// A book merge carried a user's progress; then an admin merged that user's
// account into another one (the storage half of internal/usermerge: the
// account's listening rows move to the survivor account, and the old account is
// locked and retired). The book merge's undo journal still names the old
// account. Undoing it puts the listening state back on the account that holds
// it now, and never recreates rows under the merged-away one.
func TestRestoreFollowedProgress_AfterTheUserWasMergedIntoAnotherAccount(t *testing.T) {
	s := setupTestStore(t).(*database.PebbleStore)
	keep, dup := seedSyncBooks(t, s)
	old := seedSyncUser(t, s)
	ids := database.AsSyncIdentityStore(s)
	for _, id := range []string{keep, dup} {
		_, err := ids.MintOrGetSyncID(id)
		require.NoError(t, err)
	}
	require.NoError(t, s.SetUserBookState(&database.UserBookState{UserID: old.ID, BookID: keep, Status: database.UserBookStatusInProgress, ProgressPct: 10, TotalListenedSeconds: 500, LastActivityAt: rfT0}))
	require.NoError(t, s.SetUserPositionAt(old.ID, keep, "a", 100, rfT0))
	require.NoError(t, s.SetUserBookState(&database.UserBookState{UserID: old.ID, BookID: dup, Status: database.UserBookStatusInProgress, ProgressPct: 40, TotalListenedSeconds: 3000, LastActivityAt: rfT1}))
	require.NoError(t, s.SetUserPositionAt(old.ID, dup, "a", 1200, rfT1))

	progress, redirected, err := FollowAbsorbedJournaled(s, keep, dup, nil, nil)
	require.NoError(t, err)

	// The account merge: both books' rows move to the survivor account, the old
	// account is locked and retired into it.
	survivor, err := s.CreateUser("survivor", "survivor@example.com", "argon2id", "y", []string{"user"}, "active")
	require.NoError(t, err)
	for _, book := range []string{keep, dup} {
		st, err := s.GetUserBookState(old.ID, book)
		require.NoError(t, err)
		require.NotNil(t, st)
		moved := *st
		moved.UserID = survivor.ID
		require.NoError(t, s.SetUserBookState(&moved))
		pos, err := s.ListUserPositionsForBook(old.ID, book)
		require.NoError(t, err)
		for _, p := range pos {
			require.NoError(t, s.SetUserPositionAt(survivor.ID, book, p.SegmentID, p.PositionSeconds, p.UpdatedAt))
		}
		require.NoError(t, s.ReplaceUserPositions(old.ID, book, nil))
		require.NoError(t, s.DeleteUserBookState(old.ID, book))
	}
	_, err = s.SetUserStatus(old.ID, "locked")
	require.NoError(t, err)
	oldRow, err := s.GetUserByID(old.ID)
	require.NoError(t, err)
	survRow, err := s.GetUserByID(survivor.ID)
	require.NoError(t, err)
	fields := func(u *database.User) database.UserIdentityFields {
		return database.UserIdentityFields{Roles: u.Roles, Email: u.Email, EmailSource: u.EmailSource}
	}
	oldAfter := fields(oldRow)
	oldAfter.MergedIntoUserID = survivor.ID
	require.NoError(t, s.RetireMergedUser(database.UserRetirement{SurvivorID: survivor.ID, AbsorbedID: old.ID,
		SurvivorBefore: fields(survRow), SurvivorAfter: fields(survRow), AbsorbedBefore: fields(oldRow), AbsorbedAfter: oldAfter}))

	_, err = RestoreFollowedProgress(s, keep, dup, redirected, progress)
	require.NoError(t, err)

	// dup has its own state back on the survivor account; keep is back to its own.
	dst, err := s.GetUserBookState(survivor.ID, dup)
	require.NoError(t, err)
	require.NotNil(t, dst, "the absorbed book's listening state is restored on the account that holds it now")
	require.Equal(t, 3000.0, dst.TotalListenedSeconds)
	kst, err := s.GetUserBookState(survivor.ID, keep)
	require.NoError(t, err)
	require.NotNil(t, kst)
	require.Equal(t, 500.0, kst.TotalListenedSeconds, "the carried 3000 seconds are taken back off keep")
	for _, book := range []string{keep, dup} {
		st, err := s.GetUserBookState(old.ID, book)
		require.NoError(t, err)
		require.Nil(t, st, "nothing is written back under the merged-away account")
		pos, err := s.ListUserPositionsForBook(old.ID, book)
		require.NoError(t, err)
		require.Empty(t, pos)
	}
}
