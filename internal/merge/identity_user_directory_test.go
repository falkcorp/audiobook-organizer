// file: internal/merge/identity_user_directory_test.go
// version: 1.0.0
// guid: 29fa4f36-b8cf-44f3-b9b0-0a90ac84b571
// last-edited: 2026-11-28

package merge

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// The per-user fan-outs on a migrated store (F4-08 fix-forward, data-loss
// prevention). After M3 the accounts are core's and books' u: family is empty;
// the store's account list answers from core's directory
// (store/user_directory.go). These cells run the merge and the hard-delete
// gates over exactly that store: the users' progress rows are books' own and
// present, their u: rows are gone, and only the directory knows them.

// migrateUsersToCore turns the carry fixture into a migrated store: it reads
// the users as HEAD listed them, deletes every u: row (what M3 does), shows the
// drained family alone now lists nobody, and installs a directory that answers
// those users, as core's does after the import.
func migrateUsersToCore(t *testing.T, c *carryFixture) []database.User {
	t.Helper()
	users, err := c.s.ListUsers()
	require.NoError(t, err)
	require.Len(t, users, 2)
	for _, u := range users {
		require.NoError(t, c.s.DeleteRaw("u:"+u.ID))
	}
	drained, err := c.s.ListUsers()
	require.NoError(t, err)
	require.Empty(t, drained, "precondition: the drained u: family lists nobody")
	c.s.SetUserDirectory(func() ([]database.User, error) { return users, nil })
	return users
}

// (a) A merge's follow carries every core user's listening progress from the
// absorbed book to the survivor.
func TestUnderCoreIdentity_MergeFollowCarriesCoreUsersProgress(t *testing.T) {
	c := newCarryFixture(t)
	migrateUsersToCore(t, c)

	progress, _, err := FollowAbsorbedJournaled(c.fs, c.keep, c.dup, nil, nil)
	require.NoError(t, err)
	require.Len(t, progress, 2, "the follow journals both users' progress")
	require.Equal(t, 40, c.pct(t, c.userA, c.keep), "A's progress is carried to the survivor")
	require.Equal(t, 70, c.pct(t, c.usB, c.keep), "B's progress is carried to the survivor")
}

// (b) The hard-delete gate keeps a book a core-only user has progress on: the
// probe and BookHasUserProgress see the state, and the delete never runs.
func TestUnderCoreIdentity_HardDeleteGateKeepsBookWithCoreUsersProgress(t *testing.T) {
	c := newCarryFixture(t)
	migrateUsersToCore(t, c)

	has, err := BookHasUserProgress(c.s, c.dup)
	require.NoError(t, err)
	require.True(t, has, "BookHasUserProgress sees the core users' progress")
	probe, err := NewUserStateProbe(c.s)
	require.NoError(t, err)
	has, err = probe.Has(c.dup)
	require.NoError(t, err)
	require.True(t, has, "the probe sees the core users' state")

	ran := false
	err = HardDeleteWithoutUserState(c.fs, c.dup, func() error { ran = true; return nil })
	require.ErrorIs(t, err, ErrUserStateOnDoomedBook)
	require.False(t, ran, "a book a core user has progress on is never hard-deleted")
	require.Equal(t, 40, c.pct(t, c.userA, c.dup))
}

// (c) With the directory missing, or answering an error, the gates refuse:
// the list error is a failed check, never "nobody has state", and nothing is
// deleted or moved.
func TestUnderCoreIdentity_HardDeleteGateRefusesWithoutDirectory(t *testing.T) {
	for name, install := range map[string]func(*database.PebbleStore){
		"missing": func(s *database.PebbleStore) { s.RequireUserDirectory() },
		"erroring": func(s *database.PebbleStore) {
			s.SetUserDirectory(func() ([]database.User, error) { return nil, errors.New("core unavailable") })
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := newCarryFixture(t)
			users, err := c.s.ListUsers()
			require.NoError(t, err)
			for _, u := range users {
				require.NoError(t, c.s.DeleteRaw("u:"+u.ID))
			}
			install(c.s)

			_, err = c.s.ListUsersStrict()
			require.Error(t, err, "the account list is an error, never empty")

			ran := false
			del := func() error { ran = true; return nil }
			err = HardDeleteWithoutUserState(c.fs, c.dup, del)
			require.ErrorIs(t, err, ErrUserStateCheckFailed)
			require.False(t, ran, "no delete without a readable account list")

			_, err = HardDeleteKeepingUserStateLocked(c.fs, nil, &database.Book{ID: c.dup}, nil, del)
			require.ErrorIs(t, err, ErrUserStateCheckFailed)
			require.False(t, ran, "no delete without a readable account list")

			_, err = BookHasUserProgress(c.s, c.dup)
			require.Error(t, err)

			_, _, err = FollowAbsorbedJournaled(c.fs, c.keep, c.dup, nil, nil)
			require.Error(t, err, "a follow does not run as if nobody had progress")
			require.Equal(t, 40, c.pct(t, c.userA, c.dup), "nothing moved")
			require.Equal(t, 0, c.pct(t, c.userA, c.keep))
		})
	}
}
