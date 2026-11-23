// file: internal/server/identity_admin_keys_test.go
// version: 0.1.0
// guid: 9e4a2c75-1d38-4f60-b7a3-6c0d8e5f2a19
// last-edited: 2026-11-21

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/server/legacyauth"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// coreauthBind puts the Identity and the principal and checker on ctx.
func coreauthBind(ctx context.Context, w *adminGateWorld, p *coreauth.Principal, c rbac.Checker) context.Context {
	ctx = legacyauth.WithIdentity(ctx, w.core.identity)
	if p != nil {
		ctx = coreauth.WithPrincipal(ctx, p)
		if c != nil {
			ctx = rbac.WithChecker(ctx, c)
		}
	}
	return ctx
}

// everyAtomScope are the scopes that hold every legacy atom in the fixture's
// table: HEAD's "key holds every atom" condition in core's terms.
func everyAtomScope() []rbac.Permission {
	var out []rbac.Permission
	for _, p := range auth.All() {
		out = append(out, rbac.Permission("test:"+string(p)))
	}
	return out
}

// TestAdminGates_APIKeys: under core identity an API key passes an admin gate
// when it satisfies the route's manifest requirement. Since B10g that is the whole
// gin gate: HEAD's APIKeyMayActAsAdmin (the key holds every atom) and the role-name
// test on its owner no longer run behind the bridge (owner ruling 2026-11-22, B10rn
// STOP 1). It never passes a credential-changing route.
func TestAdminGates_APIKeys(t *testing.T) {
	w := newAdminGateWorld(t, true)
	gates := []adminGateRoute{adminGateRoutes[0], adminGateRoutes[1], adminGateRoutes[3], adminGateRoutes[4], adminGateRoutes[5]}
	for _, rt := range gates {
		t.Run(rt.name, func(t *testing.T) {
			serve := func(owner string, scopes ...rbac.Permission) int {
				u := w.alfred
				if owner == "bruce" {
					u = w.bruce
				}
				p, c := adoptPrincipal(coreauth.MethodAPIKey, u, scopes...)
				return identityServe(w.core.router, rt.request(), p, c).Code
			}
			require.True(t, opened(serve("alfred", append(everyAtomScope(), rt.scopes...)...)), "a key holding every atom and the requirement passes, as in HEAD")
			require.True(t, opened(serve("alfred", rt.scopesFor()...)),
				"a key that satisfies the requirement passes without HEAD's every-atom test")
			require.True(t, opened(serve("bruce", rt.scopesFor()...)),
				"a viewer's key holding the requirement passes: no role-name test on the owner")
			require.Equal(t, http.StatusForbidden, serve("alfred", everyAtomScope()...),
				"a key holding every atom but not the route's requirement is refused")
			if len(rt.scopes) > 1 {
				require.Equal(t, http.StatusForbidden, serve("alfred", append([]rbac.Permission{"test:users.manage"}, rt.scopes[1:]...)...),
					"a key holding part of an AllOf requirement is refused")
			}
		})
	}

	t.Run("a credential-changing route is refused to every key", func(t *testing.T) {
		rt := adminGateRoutes[2]
		full := append(everyAtomScope(), rt.scopes...)
		p, c := adoptPrincipal(coreauth.MethodAPIKey, w.alfred, full...)
		rec := identityServe(w.core.router, rt.request(), p, c)
		require.Equal(t, http.StatusForbidden, rec.Code)
		require.Contains(t, rec.Body.String(), auth.CredentialChangeRefusedMessage, "refused by the credential guard, not the admin gate")
		sess, sessC := adoptPrincipal(coreauth.MethodSession, w.alfred, full...)
		require.True(t, opened(identityServe(w.core.router, rt.request(), sess, sessC).Code), "a session passes the same route")
	})

	t.Run("no manifest row fails closed for a key with every scope", func(t *testing.T) {
		r := gin.New()
		r.Use(w.core.identity.Bind(), w.core.requireAuth())
		g := r.Group("/api/v1")
		ok := func(c *gin.Context) { c.Status(http.StatusNoContent) }
		w.core.adminRoute(g, adminRouteReadOnly, http.MethodGet, "/no/such/admin/route", ok)
		w.core.adminRoute(g, adminRouteAlwaysAuthenticated, http.MethodPost, "/no/such/admin/post", ok)
		scopes := append(everyAtomScope(), "core:admin", "books:admin.view", "books:admin.debug", "core:users.manage")
		p, c := adoptPrincipal(coreauth.MethodAPIKey, w.alfred, scopes...)
		for _, rq := range []*http.Request{
			httptest.NewRequest(http.MethodGet, "/api/v1/no/such/admin/route", nil),
			httptest.NewRequest(http.MethodPost, "/api/v1/no/such/admin/post", nil),
		} {
			require.Equal(t, http.StatusForbidden, identityServe(r, rq, p, c).Code, rq.URL.Path)
		}
	})
}

// TestAPIKeyMayActAsAdmin_UnderCoreIdentity: the function the role-name sites
// call (RequireAdmin, mergeActor, SetUserEmail, inviteTokenVisible, configCan)
// judges the bound principal under core identity and HEAD's own context
// otherwise.
func TestAPIKeyMayActAsAdmin_UnderCoreIdentity(t *testing.T) {
	w := newAdminGateWorld(t, true)
	may := func(bound bool, p *coreauth.Principal, c rbac.Checker) bool {
		g, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx := httptest.NewRequest(http.MethodGet, "/x", nil).Context()
		if bound {
			ctx = coreauthBind(ctx, w, p, c)
		}
		g.Request = httptest.NewRequest(http.MethodGet, "/x", nil).WithContext(ctx)
		return servermiddleware.APIKeyMayActAsAdmin(g)
	}
	every := everyAtomScope()
	full, fullC := adoptPrincipal(coreauth.MethodAPIKey, w.alfred, every...)
	require.True(t, may(true, full, fullC), "a key holding every atom")
	part, partC := adoptPrincipal(coreauth.MethodAPIKey, w.alfred, every[1:]...)
	require.False(t, may(true, part, partC), "a key missing one atom")
	sess, sessC := adoptPrincipal(coreauth.MethodSession, w.alfred)
	require.True(t, may(true, sess, sessC), "a session holds no key")
	require.False(t, may(true, nil, nil), "no principal fails closed")
	require.True(t, may(false, part, partC), "without an Identity HEAD's test decides: no key on the context and no recorded method")
}
