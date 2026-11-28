// file: internal/books/store/user_directory_test.go
// version: 1.0.0
// guid: 02c85efc-420e-4fb5-a8de-161c1923a4c1
// last-edited: 2026-11-28

package store

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
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
