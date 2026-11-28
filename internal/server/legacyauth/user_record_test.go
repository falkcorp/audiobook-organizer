// file: internal/server/legacyauth/user_record_test.go
// version: 1.0.0
// guid: 733a3f36-1e58-4ef7-99f5-a5c361a15559
// last-edited: 2026-11-28

package legacyauth

import (
	"context"
	"errors"
	"iter"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

// dirIdentities is a coreauth.Identities whose Users yields a fixed list, then
// err when set.
type dirIdentities struct {
	coreauth.Identities
	users []*coreauth.User
	err   error
	seen  []coreauth.UserFilter
}

func (d *dirIdentities) Users(_ context.Context, f coreauth.UserFilter) iter.Seq2[*coreauth.User, error] {
	d.seen = append(d.seen, f)
	return func(yield func(*coreauth.User, error) bool) {
		for _, u := range d.users {
			if !yield(u, nil) {
				return
			}
		}
		if d.err != nil {
			yield(nil, d.err)
		}
	}
}

// (d) The store's account list under core identity is every account core
// holds, every status and the system user included, unfiltered, each in HEAD's
// words; and any read error fails the whole list.
func TestUserDirectory_ListsEveryCoreAccountInHEADsWords(t *testing.T) {
	at := time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)
	ids := &dirIdentities{users: []*coreauth.User{
		{ID: "u-bruce", Username: "bruce", Email: "bruce@example.test", Roles: []rbac.RoleName{"admin"}, Status: coreauth.StatusActive, CreatedAt: at},
		{ID: "u-jason", Username: "jason", Status: coreauth.StatusDisabled, MergedInto: "u-bruce", CreatedAt: at},
		{ID: "u-cass", Username: "cass", Roles: []rbac.RoleName{"user"}, Status: coreauth.StatusPending, CreatedAt: at},
		{ID: "_system", Username: "_system", Status: coreauth.StatusSystem, CreatedAt: at},
	}}
	users, err := UserDirectory(ids)()
	require.NoError(t, err)
	require.Equal(t, []coreauth.UserFilter{{}}, ids.seen, "unfiltered: every status and role")
	require.Len(t, users, 4)
	require.Equal(t, "active", users[0].Status)
	require.Equal(t, []string{"admin"}, users[0].Roles)
	require.Equal(t, "inactive", users[1].Status, "core's disabled is HEAD's inactive")
	require.Equal(t, "u-bruce", users[1].MergedIntoUserID)
	require.Equal(t, "pending", users[2].Status)
	require.Equal(t, "_system", users[3].ID, "the system user is listed, as HEAD's list included it")
	require.Equal(t, "system", users[3].Status)
	require.Nil(t, users[3].Roles, "nil roles stay nil")
	require.Equal(t, at, users[3].CreatedAt)

	ids.err = errors.New("scan failed")
	users, err = UserDirectory(ids)()
	require.ErrorIs(t, err, ids.err, "a read error fails the list")
	require.Nil(t, users, "never a partial list")

	_, err = UserDirectory(nil)()
	require.ErrorIs(t, err, ErrNoUserDirectory)
}
