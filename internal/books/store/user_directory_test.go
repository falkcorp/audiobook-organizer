// file: internal/books/store/user_directory_test.go
// version: 1.0.0
// guid: 02c85efc-420e-4fb5-a8de-161c1923a4c1
// last-edited: 2026-11-28

package store

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/mediacore/storage"
)

// The account list under core identity (user_directory.go): never the drained
// u: family, and an error is never an empty list.
func TestUserDirectory_UnderCoreIdentityListsTheDirectoryNeverTheDrainedFamily(t *testing.T) {
	st, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	p := st.(*PebbleStore)
	// A row left in u: (HEAD's, or one a stale writer put back) must not be
	// what the list answers under core identity.
	_, err := p.CreateUser("joker", "joker@example.test", "argon2id", "x", []string{"user"}, "active")
	require.NoError(t, err)

	headList, err := p.ListUsers()
	require.NoError(t, err)
	require.Len(t, headList, 1, "off identity the u: family is the list, as at HEAD")

	p.RequireUserDirectory()
	for name, list := range map[string]func() ([]User, error){"ListUsers": p.ListUsers, "ListUsersStrict": p.ListUsersStrict} {
		users, err := list()
		require.ErrorIs(t, err, ErrNoUserDirectory, "%s: marked and no directory is an error", name)
		require.Nil(t, users, name)
	}

	core := []User{{ID: "u-bruce", Username: "bruce", Status: "active"}, {ID: "_system", Username: "_system", Status: "system"}}
	p.SetUserDirectory(func() ([]User, error) { return core, nil })
	for name, list := range map[string]func() ([]User, error){"ListUsers": p.ListUsers, "ListUsersStrict": p.ListUsersStrict} {
		users, err := list()
		require.NoError(t, err, name)
		require.Equal(t, core, users, "%s answers the directory, not the u: row", name)
	}

	boom := errors.New("core unavailable")
	p.SetUserDirectory(func() ([]User, error) { return nil, boom })
	for name, list := range map[string]func() ([]User, error){"ListUsers": p.ListUsers, "ListUsersStrict": p.ListUsersStrict} {
		users, err := list()
		require.ErrorIs(t, err, boom, "%s returns the directory's error", name)
		require.Nil(t, users, name)
	}

	p.SetUserDirectory(nil)
	_, err = p.ListUsers()
	require.ErrorIs(t, err, ErrNoUserDirectory, "a nil directory keeps the store refusing")
}

// retireUsers is a standalone spec transform that retires the u: claim, as the
// identity flip's spec does (cutover.Flip.Spec over m3.SourcePrefixes).
func retireUsers(s storage.Spec) storage.Spec {
	s.Claims = append([]storage.Claim(nil), s.Claims...)
	for i := range s.Claims {
		if s.Claims[i].Prefix == userKeyPrefix {
			s.Claims[i].Mode = storage.ClaimRetired
		}
	}
	return s
}

// The store marks itself in its constructor when its spec retires u:, so the
// window between an open and the install of core's directory (Register's, or an
// offline caller's) answers ErrNoUserDirectory, never the drained family as "no
// users". A spec that keeps u: read-write (HEAD's, every pre-flip test) leaves
// the store unmarked.
func TestUserDirectory_ARetiredUserClaimMarksTheStoreAtConstruction(t *testing.T) {
	retired, err := OpenStandalone(t.TempDir()+"/retired.pebble", StandaloneOptions{FS: storage.MemFS(), Spec: retireUsers})
	require.NoError(t, err)
	t.Cleanup(func() { _ = retired.Close() })
	require.True(t, retired.UserDirectoryRequired(), "a store whose u: claim is retired is under core identity from construction")
	for name, list := range map[string]func() ([]User, error){"ListUsers": retired.ListUsers, "ListUsersStrict": retired.ListUsersStrict} {
		users, err := list()
		require.ErrorIs(t, err, ErrNoUserDirectory, "%s: opened and no directory installed yet is an error, never an empty list", name)
		require.Nil(t, users, name)
	}

	head, err := OpenStandalone(t.TempDir()+"/head.pebble", StandaloneOptions{FS: storage.MemFS()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = head.Close() })
	require.False(t, head.UserDirectoryRequired(), "a read-write u: claim is HEAD's account list")
	users, err := head.ListUsers()
	require.NoError(t, err)
	require.Empty(t, users)
}
