// file: internal/server/gone_session_helpers_test.go
// version: 1.0.0
// guid: 4f7a2c19-6d83-4e50-b1a7-9c0e5d3f8a62
// last-edited: 2026-10-18

package server

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// viewerGoneSession returns a password-origin session for a synthetic user
// holding only the seeded viewer role (library.view and requests.create). It
// is refused by every library.edit_metadata, library.organize, library.delete
// and integrations.manage guard, so a 403 from it on a retired stub proves the
// stub kept its s.perm guard.
func (f *credGuardFixture) viewerGoneSession(t *testing.T) (userID, token string) {
	t.Helper()
	u, err := f.store.CreateUser("synthetic-viewer", "viewer@example.test", "bcrypt", "x", []string{auth.SeedRoleViewer}, "active")
	require.NoError(t, err)
	return u.ID, f.sessionFor(t, u.ID)
}

// exactPermGoneSession returns a session for a synthetic user whose custom
// role holds exactly one permission. Used two ways on a retired stub: with the
// permission the route requires it must reach the 410 (so the guard is that
// permission and not something broader), and with a permission the route does
// not require it must get 403. The role is custom on purpose: a seeded role's
// 403 can come from somewhere other than s.perm (the fingerprint-worker role
// is refused by abortWorkerOnlyOutsideWorkerAPI before any route guard runs).
func (f *credGuardFixture) exactPermGoneSession(t *testing.T, name, perm string) string {
	t.Helper()
	role, err := f.store.CreateRole(&database.Role{
		ID: "synthetic-" + name, Name: "synthetic-" + name, Permissions: []string{perm},
	})
	require.NoError(t, err)
	u, err := f.store.CreateUser("synthetic-"+name, name+"@example.test", "bcrypt", "x", []string{role.ID}, "active")
	require.NoError(t, err)
	return f.sessionFor(t, u.ID)
}

func (f *credGuardFixture) sessionFor(t *testing.T, userID string) string {
	t.Helper()
	sess, err := f.store.CreateSessionWithOrigin(userID, "127.0.0.1", "test", time.Hour, database.SessionOriginPassword)
	require.NoError(t, err)
	return sess.ID
}
