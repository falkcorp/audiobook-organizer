// file: internal/server/handlers/system/config_bridge_sites_test.go
// version: 0.1.0
// guid: 3d5ff52f-e88c-4653-9050-cc8d92d81393
// last-edited: 2026-11-21

package system_test

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/server/legacyauth"
)

// F4-08 B7: configCan (the Actor.Can of PUT /config) answers through the bridge
// under core identity and from HEAD's role-name and permission-set tests
// otherwise. Under core identity there is no first-run grant (it read books' own
// user count), core:admin is HEAD's admin test plus the permission itself (F4-08
// B10y), and any other permission is held through the atom lookup's table
// entries.

// bridgedActor serves PUT /config with the principal holding scopes bound and,
// when id is non-nil, the Identity; pre shapes the request first (HEAD's user,
// permission set).
func bridgedActor(t *testing.T, id *legacyauth.Identity, firstRun bool, method coreauth.Method, scopes []rbac.Permission, pre ...gin.HandlerFunc) config.Actor {
	t.Helper()
	f := &stubFacade{}
	h, _ := newTestHandler(t)
	h.WithConfigFacade(func() config.Facade { return f })
	h.WithConfigAtoms(func(perm string) []string {
		if perm == "core:settings.manage" {
			return []string{string(auth.PermSettingsManage)}
		}
		return nil
	})
	h.WithConfigFirstRun(func() bool { return firstRun })
	w := run(http.MethodPut, "/config", "/config", []byte(`{"set":{"a":1}}`), func(r *gin.Engine) {
		for _, m := range pre {
			r.Use(m)
		}
		r.Use(func(c *gin.Context) {
			if id != nil || len(scopes) > 0 {
				bindCore(c, id, method, scopes...)
			}
			c.Next()
		})
		r.PUT("/config", h.UpdateConfig)
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Len(t, f.actors, 1)
	return f.actors[0]
}

func TestBridgeSites_ConfigCan_CoreIdentityAnswersFromThePrincipal(t *testing.T) {
	id := bridgeIdentity(t, string(auth.PermSettingsManage))
	settings := bridgeScope(string(auth.PermSettingsManage))
	headAdmin := asUser("u-bruce", []string{"admin"}, auth.MethodSession, "")

	// core:admin is HEAD's admin test and core's together (F4-08 B10y, item 5):
	// the active caller holds the admin role by name, its credential may act
	// as admin, and the principal holds core:admin.
	activeAdmin := activeUser("u-bruce", "admin")
	t.Run("core:admin needs the admin role, the credential and the checker", func(t *testing.T) {
		a := bridgedActor(t, id, false, coreauth.MethodSession, []rbac.Permission{rbac.CoreAdmin}, activeAdmin)
		assert.True(t, a.Can("core:admin"))
	})
	t.Run("core:admin through the checker alone is not HEAD's admin", func(t *testing.T) {
		a := bridgedActor(t, id, false, coreauth.MethodSession, []rbac.Permission{rbac.CoreAdmin})
		assert.False(t, a.Can("core:admin"), "no user")
		a = bridgedActor(t, id, false, coreauth.MethodSession, []rbac.Permission{rbac.CoreAdmin}, activeUser("u-selina", "custom-all"))
		assert.False(t, a.Can("core:admin"), "a role other than admin holding core:admin")
		a = bridgedActor(t, id, false, coreauth.MethodSession, []rbac.Permission{rbac.CoreAdmin}, asUser("u-bruce", []string{"admin"}, auth.MethodSession, ""))
		assert.False(t, a.Can("core:admin"), "an admin account that is not active")
	})
	t.Run("an admin's key that may not act as admin", func(t *testing.T) {
		a := bridgedActor(t, id, false, coreauth.MethodAPIKey, []rbac.Permission{rbac.CoreAdmin, settings}, activeAdmin)
		assert.False(t, a.Can("core:admin"), "the key does not hold every atom (APIKeyMayActAsAdmin)")
	})
	t.Run("the admin role without core:admin", func(t *testing.T) {
		a := bridgedActor(t, id, false, coreauth.MethodSession, nil, headAdmin)
		assert.False(t, a.Can("core:admin"))
		a = bridgedActor(t, id, false, coreauth.MethodSession, nil, activeAdmin)
		assert.False(t, a.Can("core:admin"))
	})
	t.Run("an atom's table entry carries its scope", func(t *testing.T) {
		a := bridgedActor(t, id, false, coreauth.MethodSession, []rbac.Permission{settings})
		assert.True(t, a.Can("core:settings.manage"))
		assert.False(t, a.Can("core:tasks.manage"), "a scope no atom carries")
	})
	t.Run("HEAD's permission set is not read", func(t *testing.T) {
		held := func(c *gin.Context) {
			c.Request = c.Request.WithContext(auth.WithPermissions(c.Request.Context(), []auth.Permission{auth.PermSettingsManage}))
			c.Next()
		}
		a := bridgedActor(t, id, false, coreauth.MethodSession, nil, held)
		assert.False(t, a.Can("core:settings.manage"))
	})
	t.Run("the first-run window grants nothing", func(t *testing.T) {
		a := bridgedActor(t, id, true, coreauth.MethodSession, nil)
		assert.False(t, a.Can(config.FirstRunPerm))
	})
	t.Run("the auth-disabled principal holds core:admin no more than the table's atoms", func(t *testing.T) {
		a := bridgedActor(t, id, false, coreauth.MethodAuthDisabled, nil)
		assert.False(t, a.Can("core:admin"))
		assert.False(t, a.Can("core:settings.manage"))
	})
}

// activeUser is HEAD's signed-in user record with roles, active.
func activeUser(id string, roles ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("auth_user", &database.User{ID: id, Roles: roles, Status: "active"})
		c.Next()
	}
}

// Without an Identity HEAD decides, and a core principal in the context changes
// nothing.
func TestBridgeSites_ConfigCan_WithoutAnIdentityHeadDecides(t *testing.T) {
	settings := bridgeScope(string(auth.PermSettingsManage))
	t.Run("the first-run window is HEAD's", func(t *testing.T) {
		a := bridgedActor(t, nil, true, coreauth.MethodSession, nil)
		assert.True(t, a.Can(config.FirstRunPerm))
	})
	t.Run("a core principal holding core:admin and the scope grants nothing", func(t *testing.T) {
		a := bridgedActor(t, nil, false, coreauth.MethodSession, []rbac.Permission{rbac.CoreAdmin, settings})
		assert.False(t, a.Can("core:admin"))
		assert.False(t, a.Can("core:settings.manage"))
	})
	t.Run("HEAD's permission set holds the atom", func(t *testing.T) {
		held := func(c *gin.Context) {
			c.Request = c.Request.WithContext(auth.WithPermissions(c.Request.Context(), []auth.Permission{auth.PermSettingsManage}))
			c.Next()
		}
		a := bridgedActor(t, nil, false, coreauth.MethodSession, nil, held)
		assert.True(t, a.Can("core:settings.manage"))
	})
}
