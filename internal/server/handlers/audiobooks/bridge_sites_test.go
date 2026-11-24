// file: internal/server/handlers/audiobooks/bridge_sites_test.go
// version: 0.1.0
// guid: b9efd634-f96c-4665-8f8f-ce8d974465f0
// last-edited: 2026-11-21

package audiobookshandler_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	audiobookshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/server/legacyauth"
)

// F4-08 B7: StrictPermissionHolds and trashProgressViewer ask the bridge under
// core identity and HEAD's auth.Can otherwise. Four worlds each: no Identity
// with HEAD's set holding users.manage (allowed), no Identity with only a core
// principal holding it (refused: a principal changes nothing on a LegacyAuthGin
// server), an Identity with the principal holding the scope (allowed), an
// Identity with only HEAD's set (refused: the bridge is the only judge).

func bridgeCtx(t *testing.T, bound bool, headPerms []auth.Permission, scopes []rbac.Permission) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/audiobooks/soft-deleted", nil)
	c.Set("auth_user", &database.User{ID: "u-bruce", Status: "active"})
	if len(headPerms) > 0 {
		c.Request = c.Request.WithContext(auth.WithPermissions(c.Request.Context(), headPerms))
	}
	var id *legacyauth.Identity
	if bound {
		id = bridgeIdentity(t, string(auth.PermUsersManage))
	}
	if bound || len(scopes) > 0 {
		bindCore(c, id, coreauth.MethodSession, scopes...)
	}
	return c
}

type bridgeSiteCase struct {
	name      string
	bound     bool
	headPerms []auth.Permission
	scopes    []rbac.Permission
	held      bool
}

func bridgeSiteCases() []bridgeSiteCase {
	scope := bridgeScope(string(auth.PermUsersManage))
	return []bridgeSiteCase{
		{"legacy: HEAD's permission set holds it", false, []auth.Permission{auth.PermUsersManage}, nil, true},
		{"legacy: a core principal holding the scope changes nothing", false, nil, []rbac.Permission{scope}, false},
		{"core identity: the principal holds the scope", true, nil, []rbac.Permission{scope}, true},
		{"core identity: HEAD's permission set is not read", true, []auth.Permission{auth.PermUsersManage}, nil, false},
	}
}

func TestBridgeSites_StrictPermissionHolds(t *testing.T) {
	for _, tc := range bridgeSiteCases() {
		t.Run(tc.name, func(t *testing.T) {
			c := bridgeCtx(t, tc.bound, tc.headPerms, tc.scopes)
			require.Equal(t, tc.held, audiobookshandler.StrictPermissionHolds(c, auth.PermUsersManage))
		})
	}
}

func TestBridgeSites_TrashProgressViewer(t *testing.T) {
	for _, tc := range bridgeSiteCases() {
		t.Run(tc.name, func(t *testing.T) {
			c := bridgeCtx(t, tc.bound, tc.headPerms, tc.scopes)
			viewer, seeAll := audiobookshandler.TrashProgressViewer(c)
			require.Equal(t, "u-bruce", viewer)
			require.Equal(t, tc.held, seeAll)
		})
	}
}

// The read-side successor (core:users.view) does not widen the viewer: a
// principal holding only it is not shown other users' progress, as HEAD's
// users.manage test does not show it to a user who lacks that atom.
func TestBridgeSites_TrashProgressViewer_ASuccessorScopeAloneDoesNotWiden(t *testing.T) {
	c := bridgeCtx(t, true, nil, []rbac.Permission{"core:users.view"})
	_, seeAll := audiobookshandler.TrashProgressViewer(c)
	require.False(t, seeAll)
}
