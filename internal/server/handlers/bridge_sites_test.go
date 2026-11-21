// file: internal/server/handlers/bridge_sites_test.go
// version: 0.1.0
// guid: 3e9a0b57-c2d4-4816-a5f7-d01b6c48e932
// last-edited: 2026-11-21

package handlers_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
)

// F4-08 B7: the three in-handler permission sites of this package (isAdminUser,
// ChangePassword's reset-another-user check, defPermissionsHeld) ask the
// bridge under core identity and HEAD's auth.Can otherwise. Each is driven
// through the exported handler that reaches it, in four worlds:
//
//   - no Identity, HEAD's permission set holds the atom: HEAD allows;
//   - no Identity, only a core principal holds it: still refused (a principal
//     in the context changes nothing on a LegacyAuthGin server);
//   - Identity, the principal holds the atom's scope: allowed;
//   - Identity, only HEAD's permission set holds it: refused (the bridge is the
//     only judge).

type bridgeWorld struct {
	name      string
	bound     bool
	headPerms []auth.Permission
	scopes    []rbac.Permission
	allowed   bool
}

func bridgeWorlds(atom auth.Permission) []bridgeWorld {
	scope := bridgeScope(string(atom))
	return []bridgeWorld{
		{"legacy: HEAD's permission set holds it", false, []auth.Permission{atom}, nil, true},
		{"legacy: a core principal holding the scope changes nothing", false, nil, []rbac.Permission{scope}, false},
		{"core identity: the principal holds the scope", true, nil, []rbac.Permission{scope}, true},
		{"core identity: HEAD's permission set is not read", true, []auth.Permission{atom}, nil, false},
	}
}

func TestBridgeSites_APIKeyList_ShowAllIsJudgedByTheWorld(t *testing.T) {
	for _, w := range bridgeWorlds(auth.PermUsersManage) {
		t.Run(w.name, func(t *testing.T) {
			caller := &database.User{ID: "u-bruce", Username: "bruce"}
			store := handlersmocks.NewMockAPIKeyHandlerStore(t)
			if w.allowed {
				store.EXPECT().ListAllAPIKeys().Return(nil, nil)
			} else {
				store.EXPECT().ListAPIKeysForUser("u-bruce").Return(nil, nil)
			}
			c, rec := newAuthCtx("GET", "/auth/api-keys?all=true", nil)
			setAuthUser(c, caller)
			if len(w.headPerms) > 0 {
				c.Request = c.Request.WithContext(auth.WithPermissions(c.Request.Context(), w.headPerms))
			}
			var id = bridgeIdentity(t, string(auth.PermUsersManage))
			if !w.bound {
				id = nil
			}
			if w.bound || len(w.scopes) > 0 {
				bindCore(c, id, coreauth.MethodSession, w.scopes...)
			}
			handlers.NewAPIKeyHandler(store).List(c)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		})
	}
}

func TestBridgeSites_ChangePassword_ResetAnotherUserIsJudgedByTheWorld(t *testing.T) {
	for _, w := range bridgeWorlds(auth.PermUsersManage) {
		t.Run(w.name, func(t *testing.T) {
			store := handlersmocks.NewMockAuthStore(t)
			if w.allowed {
				// Past the check: the target is looked up, and this one is gone.
				store.EXPECT().GetUserByID("u-target").Return(nil, errors.New("gone"))
			}
			h := handlers.NewAuthHandler(store, true)
			c, rec := newAuthCtx(http.MethodPut, "/api/v1/auth/me/password",
				map[string]any{"user_id": "u-target", "new_password": "new-password-123"})
			setAuthUser(c, &database.User{ID: "u-bruce", Status: "active"})
			if len(w.headPerms) > 0 {
				c.Request = c.Request.WithContext(auth.WithPermissions(c.Request.Context(), w.headPerms))
			}
			var id = bridgeIdentity(t, string(auth.PermUsersManage))
			if !w.bound {
				id = nil
			}
			if w.bound || len(w.scopes) > 0 {
				bindCore(c, id, coreauth.MethodSession, w.scopes...)
			}
			h.ChangePassword(c)
			if w.allowed {
				require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
			} else {
				require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			}
		})
	}
}

// defPermissionsHeld: the legacy cases keep HEAD's wiring flag; under core
// identity the flag is not read and the auth-disabled principal is the "off".
func TestBridgeSites_TriggerOperationV2_PerDefPermissionIsJudgedByTheWorld(t *testing.T) {
	def := opsregistry.OperationDef{ID: "maintenance.cleanup", Permissions: []auth.Permission{auth.PermSettingsManage}}
	const body = `{"def_id":"maintenance.cleanup"}`
	scope := bridgeScope(string(auth.PermSettingsManage))

	run := func(t *testing.T, enforce, bound bool, method coreauth.Method, scopes []rbac.Permission, headPerms []auth.Permission, wantOK bool) {
		t.Helper()
		registry := handlersmocks.NewMockOperationsRegistry(t)
		registry.EXPECT().Def("maintenance.cleanup").Return(def, true).Maybe()
		if wantOK {
			registry.EXPECT().EnqueueOp(mock.Anything, "maintenance.cleanup", mock.Anything).Return("op1", nil)
		}
		h := handlers.NewOperationsV2Handler(nil, registry, nil, enforce)
		c, w := newOpsV2Ctx(http.MethodPost, "/operations/v2", body, nil)
		if len(headPerms) > 0 {
			withCallerPerms(c, headPerms...)
		}
		var id = bridgeIdentity(t, string(auth.PermSettingsManage))
		if !bound {
			id = nil
		}
		if bound || len(scopes) > 0 {
			bindCore(c, id, method, scopes...)
		}
		h.TriggerOperationV2(c)
		if wantOK {
			require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
		} else {
			require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
		}
	}

	t.Run("legacy: enforcePerms false opens the gate whatever a core principal holds", func(t *testing.T) {
		run(t, false, false, coreauth.MethodSession, nil, nil, true)
	})
	t.Run("legacy: enforcePerms true judges HEAD's permission set", func(t *testing.T) {
		run(t, true, false, coreauth.MethodSession, nil, []auth.Permission{auth.PermSettingsManage}, true)
		run(t, true, false, coreauth.MethodSession, []rbac.Permission{scope}, nil, false)
	})
	t.Run("core identity: the principal's checker decides, enforcePerms is not read", func(t *testing.T) {
		run(t, false, true, coreauth.MethodSession, []rbac.Permission{scope}, nil, true)
		run(t, false, true, coreauth.MethodSession, nil, []auth.Permission{auth.PermSettingsManage}, false)
		run(t, true, true, coreauth.MethodSession, nil, nil, false)
	})
	t.Run("core identity: the auth-disabled principal is the open gate", func(t *testing.T) {
		run(t, true, true, coreauth.MethodAuthDisabled, nil, nil, true)
	})
}
