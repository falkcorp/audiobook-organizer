// file: internal/server/identity_admin_gates_test.go
// version: 0.1.0
// guid: 3b7e1d58-94c2-4a06-8f1d-c5a2e9b60741
// last-edited: 2026-11-21

package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// adminGateRoute is one real admin-gated route and the §6.2 requirement the
// manifest holds for it, with the gate shape that fronts it.
type adminGateRoute struct {
	name   string
	method string
	path   string
	body   string
	scopes []rbac.Permission // what satisfies the manifest requirement
	always bool              // answers 401 with authentication off at HEAD
}

var adminGateRoutes = []adminGateRoute{
	{"per-route read-only (adminRoute)", http.MethodGet, "/api/v1/cache/stats/keys", "", []rbac.Permission{"books:admin.view"}, false},
	{"per-route always-authenticated (adminRoute)", http.MethodPost, "/api/v1/cache/invalidate", "", []rbac.Permission{"core:admin"}, true},
	{"per-route always-authenticated (adminGroup + credRoute)", http.MethodGet, "/api/v1/admin/guest-service-tokens", "", []rbac.Permission{"core:admin"}, true},
	{"group-level read-only (master-list group)", http.MethodGet, "/api/v1/master-lists", "", []rbac.Permission{"core:users.manage", "books:admin.view"}, false},
	{"admindebug read gate", http.MethodGet, "/api/v1/admin/debug/lookup", "", []rbac.Permission{"books:admin.view"}, false},
	{"admindebug write gate", http.MethodPatch, "/api/v1/admin/debug/books/b1", `{}`, []rbac.Permission{"books:admin.debug"}, true},
}

// adminGateWorld is a HEAD server and a core-identity server over one store,
// with an admin-role user and a viewer-role user known to the bridge.
type adminGateWorld struct {
	head, core *Server
	alfred     *database.User // role admin
	bruce      *database.User // role viewer
}

func newAdminGateWorld(t *testing.T, authOn bool) *adminGateWorld {
	t.Helper()
	gin.SetMode(gin.TestMode)
	orig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = orig })
	config.AppConfig.EnableAuth = authOn
	store, err := database.NewPebbleStoreInMemory(t.TempDir() + "/admin-gates.pebble")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	w := &adminGateWorld{
		alfred: &database.User{ID: "u-alfred", Username: "alfred", Roles: []string{auth.SeedRoleAdmin}, Status: "active"},
		bruce:  &database.User{ID: "u-bruce", Username: "bruce", Roles: []string{auth.SeedRoleViewer}, Status: "active"},
	}
	// The table maps every legacy atom, as books' does: an API key is judged by
	// whether it holds all of them.
	var atoms []string
	for _, p := range auth.All() {
		atoms = append(atoms, string(p))
	}
	id := adoptIdentity(t, atoms, nil, nil, w.alfred, w.bruce)
	w.head = newTestServer(t, store)
	w.core = newTestServerWith(t, store, WithIdentity(id))
	return w
}

func (r adminGateRoute) request() *http.Request {
	req := httptest.NewRequest(r.method, r.path, strings.NewReader(r.body))
	if r.body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

// scopesFor are the route's scopes plus the permission the master-list routes'
// own s.perm asks, so only the admin gate is under test.
func (r adminGateRoute) scopesFor(extra ...rbac.Permission) []rbac.Permission {
	return append(append([]rbac.Permission{"test:users.manage"}, r.scopes...), extra...)
}

func opened(code int) bool { return code != http.StatusUnauthorized && code != http.StatusForbidden }

// TestAdminGates_BridgeRefusals: under core identity every admin gate shape is
// the conjunction of the manifest requirement through the bridge and HEAD's own
// role-name test, so it refuses whatever either refuses.
func TestAdminGates_BridgeRefusals(t *testing.T) {
	w := newAdminGateWorld(t, true)
	for _, rt := range adminGateRoutes {
		t.Run(rt.name, func(t *testing.T) {
			serve := func(p *coreauth.Principal, c rbac.Checker) int {
				return identityServe(w.core.router, rt.request(), p, c).Code
			}
			require.Equal(t, http.StatusUnauthorized, serve(nil, nil), "no principal")

			p, c := adoptPrincipal(coreauth.MethodSession, w.alfred, "test:users.manage")
			require.Equal(t, http.StatusForbidden, serve(p, c), "an admin-role user whose scopes lack the requirement")

			p, c = adoptPrincipal(coreauth.MethodSession, w.alfred, rt.scopesFor()...)
			require.True(t, opened(serve(p, c)), "an admin-role user holding the requirement passes both tests")

			p, c = adoptPrincipal(coreauth.MethodSession, w.bruce, rt.scopesFor()...)
			require.Equal(t, http.StatusForbidden, serve(p, c), "scopes alone do not pass: HEAD's role-name test still runs")

			p, c = adoptPrincipal(coreauth.MethodAPIKey, w.alfred, rt.scopesFor()...)
			require.Equal(t, http.StatusForbidden, serve(p, c), "a key principal is refused as HEAD's key-scope test refuses it")
		})
	}
}

// TestAdminGates_AuthDisabledPrincipal: HEAD's adminRole passes a read-only
// route with auth off and its RequireAdmin answers 401 on any other; the
// bridge keeps the pass-through and the refusal, does not let the
// auth-disabled checker open an always-authenticated route, and answers the
// refusal 403 rather than HEAD's 401, which would end a no-auth install's SPA
// session (F4-08 ruling G5(a)).
func TestAdminGates_AuthDisabledPrincipal(t *testing.T) {
	w := newAdminGateWorld(t, true)
	for _, rt := range adminGateRoutes {
		t.Run(rt.name, func(t *testing.T) {
			// core's auth-disabled checker holds every requirement that is not
			// AuthRequired; the fixture stands for it by holding the scopes.
			p, c := adoptPrincipal(coreauth.MethodAuthDisabled, nil, rt.scopesFor()...)
			code := identityServe(w.core.router, rt.request(), p, c).Code
			if rt.always {
				require.Equal(t, http.StatusForbidden, code, "HEAD answered 401 with auth off, now 403 (G5(a)); the checker holding the scope must not widen it")
				return
			}
			require.True(t, opened(code), "a read-only route opens for the auth-disabled principal, as HEAD's adminRole did")
			np, nc := adoptPrincipal(coreauth.MethodAuthDisabled, nil)
			require.Equal(t, http.StatusForbidden, identityServe(w.core.router, rt.request(), np, nc).Code,
				"and is refused (no user, 403 not the session-ending 401: G5(a)) where its own checker does not hold")
		})
	}
}

// TestAdminGates_NoManifestRowFailsClosed: a route gated under core identity that the
// manifest has no row for is refused with 403 for every principal, never opened, whatever
// its kind and whichever helper registered it.
func TestAdminGates_NoManifestRowFailsClosed(t *testing.T) {
	w := newAdminGateWorld(t, true)
	every, everyC := adoptPrincipal(coreauth.MethodSession, w.alfred,
		"core:admin", "books:admin.view", "books:admin.debug", "core:users.manage")
	off, offC := adoptPrincipal(coreauth.MethodAuthDisabled, nil, "core:admin", "books:admin.view")
	for _, kind := range []adminRouteKind{adminRouteReadOnly, adminRouteAlwaysAuthenticated} {
		r := gin.New()
		r.Use(w.core.identity.Bind(), w.core.requireAuth())
		ok := func(c *gin.Context) { c.Status(http.StatusNoContent) }
		g := r.Group("/api/v1")
		w.core.adminRoute(g, kind, http.MethodGet, "/no/such/admin/route", ok)
		w.core.adminGroup(g, kind, http.MethodGet, "/no/such/group/route").GET("/no/such/group/route", ok)
		g.Group("").Use(w.core.adminRole()).GET("/no/such/role/route", ok)
		for _, path := range []string{"/api/v1/no/such/admin/route", "/api/v1/no/such/group/route", "/api/v1/no/such/role/route"} {
			for name, pc := range map[string]struct {
				p *coreauth.Principal
				c rbac.Checker
			}{"admin holding everything": {every, everyC}, "auth-disabled": {off, offC}} {
				rec := identityServe(r, httptest.NewRequest(http.MethodGet, path, nil), pc.p, pc.c)
				require.Equal(t, http.StatusForbidden, rec.Code, "%s %s %s", kind, path, name)
			}
		}
	}
}

// TestAdminGates_ManifestCoversEveryRecordedAdminRoute: every route the server records
// as admin-gated has a manifest row with an AllOf or AnyOf requirement, so none is
// refused outright by the fail-closed rule.
func TestAdminGates_ManifestCoversEveryRecordedAdminRoute(t *testing.T) {
	w := newAdminGateWorld(t, true)
	require.NotEmpty(t, w.core.adminRoutes)
	for key := range w.core.adminRoutes {
		method, full, _ := strings.Cut(key, " ")
		req, ok := adminManifestRequirement(method, full)
		require.True(t, ok, "no manifest row for %s", key)
		require.Contains(t, []rbac.RequirementKind{rbac.KindAllOf, rbac.KindAnyOf}, req.Kind(), key)
	}
}

// TestAdminGates_Legacy: without WithIdentity HEAD's gates decide, whatever
// core principal is bound and however many scopes it holds.
func TestAdminGates_Legacy(t *testing.T) {
	for _, authOn := range []bool{true, false} {
		w := newAdminGateWorld(t, authOn)
		for _, rt := range adminGateRoutes {
			p, c := adoptPrincipal(coreauth.MethodSession, w.alfred, rt.scopesFor("core:admin", "books:admin.view", "books:admin.debug")...)
			code := identityServe(w.head.router, rt.request(), p, c).Code
			switch {
			case authOn:
				require.Equal(t, http.StatusUnauthorized, code, "%s: HEAD reads its own session, not the core principal", rt.name)
			case rt.always:
				require.Equal(t, http.StatusUnauthorized, code, "%s: RequireAdmin answers 401 with auth off", rt.name)
			default:
				require.True(t, opened(code), "%s: adminRole is a pass-through with auth off", rt.name)
			}
		}
	}
}
