// file: internal/server/admin_routes_test.go
// version: 1.0.0
// guid: 77227bb9-356c-49c7-bb9b-98245ccb7bed
// last-edited: 2026-10-30

package server

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Every route behind the admin role is classified here, with a reason. The rule
// is in wire_admin_role.go and the per-route table in
// docs/decisions/2026-10-30-admin-routes-auth-off.md. A route added behind
// adminRoute / adminGroup, or any admin route wired some other way, fails
// TestAdminRoutes_EveryAdminRouteIsClassified until it is put on one of these
// lists on purpose.

// adminAlwaysAuthenticatedRoutes keep servermiddleware.RequireAdmin: they
// answer 401 with EnableAuth=false, so an unauthenticated caller cannot reach
// them. A route belongs here when it deletes or rewrites data, or is a
// credential surface.
var adminAlwaysAuthenticatedRoutes = map[string]string{
	"POST /api/v1/maintenance/wipe":                          "retired library wipe stub (410): a wipe verb stays behind a login",
	"POST /api/v1/cache/invalidate":                          "retired cache flush stub (410): a flush verb stays behind a login",
	"POST /api/v1/admin/recompact-digests":                   "retired stub (410) of a digest rewrite",
	"GET /api/v1/admin/guest-service-tokens":                 "credential surface: names guests and token ids, calls Cloudflare",
	"GET /api/v1/admin/guest-service-tokens/plan":            "credential surface: reads the real books policy",
	"POST /api/v1/admin/guest-service-tokens/:userId/revoke": "revokes a guest's phone-app access",
	"POST /api/v1/system/reset":                              "wipes the store, users included",
	"POST /api/v1/system/factory-reset":                      "wipes the store and the settings",
	"PATCH /api/v1/admin/debug/books/:id":                    "raw book row edit (preview and apply share the route)",
	"PATCH /api/v1/admin/debug/book-files/:id":               "raw book_file row edit (preview and apply share the route)",
	"POST /api/v1/admin/debug/edits/:edit_id/undo":           "rewrites a row back from an undo image",
}

// adminReadOnlyRoutes use adminRole: RequireAdmin with authentication on, open
// with it off. A route belongs here only when it just returns state.
var adminReadOnlyRoutes = map[string]string{
	"GET /api/v1/cache/stats/keys":           "lists the keys of one in-memory cache",
	"GET /api/v1/admin/debug/books/:id":      "reads one book row",
	"GET /api/v1/admin/debug/book-files/:id": "reads one book_file row",
	"GET /api/v1/admin/debug/lookup":         "finds what references a path",
	"GET /api/v1/admin/debug/edits":          "lists recorded debug edits",
}

func recordedAdminRoutes(srv *Server, kind adminRouteKind) []string {
	var out []string
	for key, k := range srv.adminRoutes {
		if k == kind {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// TestAdminRoutes_EveryAdminRouteIsClassified: the recorded admin routes are
// exactly the two reviewed lists, each is a real route, and RequireAdmin is
// wired nowhere but the one place that records it.
func TestAdminRoutes_EveryAdminRouteIsClassified(t *testing.T) {
	f := setupCredGuardServer(t)
	live := map[string]bool{}
	for _, r := range f.srv.router.Routes() {
		live[r.Method+" "+r.Path] = true
	}
	require.NotEmpty(t, f.srv.adminRoutes, "no admin route was recorded: the helper is not wired")

	for _, c := range []struct {
		kind    adminRouteKind
		allowed map[string]string
	}{
		{adminRouteAlwaysAuthenticated, adminAlwaysAuthenticatedRoutes},
		{adminRouteReadOnly, adminReadOnlyRoutes},
	} {
		got := recordedAdminRoutes(f.srv, c.kind)
		var unlisted []string
		for _, key := range got {
			if _, ok := c.allowed[key]; !ok {
				unlisted = append(unlisted, key)
			}
			assert.True(t, live[key], "%s admin route %q was recorded but is not in the router", c.kind, key)
		}
		assert.Empty(t, unlisted, "%s admin routes missing from the reviewed allowlist; classify each one (wire_admin_role.go)", c.kind)
		for key, why := range c.allowed {
			assert.NotEmpty(t, why, key)
			assert.Equal(t, c.kind, f.srv.adminRoutes[key], "%q is on the %s allowlist but was not recorded as such", key, c.kind)
		}
	}
	for key := range adminAlwaysAuthenticatedRoutes {
		_, both := adminReadOnlyRoutes[key]
		assert.False(t, both, "%q is on both lists", key)
	}
}

// A call of RequireAdmin outside the recording helpers would put a route
// behind the admin role without classifying it, and no router walk can see it
// (gin does not expose a route's middleware chain). So the calls are confined
// to the places that record: wire_admin_role.go and the admindebug package
// (whose Register returns its routes for the server to record).
var requireAdminCall = regexp.MustCompile(`\bRequireAdmin\(\)`)

func TestAdminRoutes_RequireAdminIsOnlyWiredWhereItIsRecorded(t *testing.T) {
	allowed := map[string]bool{
		"wire_admin_role.go":             true,
		"handlers/admindebug/handler.go": true,
		"middleware/auth.go":             true, // the definition
	}
	var offenders []string
	err := filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(raw), "\n") {
			code := strings.TrimSpace(line)
			if strings.HasPrefix(code, "//") || strings.HasPrefix(code, "func RequireAdmin()") {
				continue
			}
			if requireAdminCall.MatchString(code) && !allowed[filepath.ToSlash(p)] {
				offenders = append(offenders, filepath.ToSlash(p)+":"+strconv.Itoa(i+1))
			}
		}
		return nil
	})
	require.NoError(t, err)
	assert.Empty(t, offenders, "RequireAdmin() wired outside wire_admin_role.go: register the route through adminRoute / adminGroup so it is classified")
}

// concretePath fills a route's path parameters.
func concretePath(route string) string {
	return regexp.MustCompile(`:[A-Za-z_]+`).ReplaceAllString(route, "x")
}

func splitRoute(key string) (method, path string) {
	method, path, _ = strings.Cut(key, " ")
	return method, concretePath(path)
}

func (f *credGuardFixture) viewerSession(t *testing.T) string {
	t.Helper()
	viewer, err := f.store.CreateUser("synthetic-viewer", "viewer@example.test", "bcrypt", "x", []string{auth.SeedRoleViewer}, "active")
	require.NoError(t, err)
	sess, err := f.store.CreateSessionWithOrigin(viewer.ID, "127.0.0.1", "test", time.Hour, database.SessionOriginPassword)
	require.NoError(t, err)
	return sess.ID
}

func allAdminRouteKeys() []string {
	var keys []string
	for k := range adminAlwaysAuthenticatedRoutes {
		keys = append(keys, k)
	}
	for k := range adminReadOnlyRoutes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// With authentication on, every classified route (both kinds) refuses a signed
// in non-admin with 403 from the admin gate itself, before its permission
// guard, its credential guard or its handler. This also proves each recorded
// route really sits behind the gate it was recorded with.
func TestAdminRoutes_AuthOnNonAdminGets403(t *testing.T) {
	f := setupCredGuardServer(t)
	viewer := f.viewerSession(t)
	for _, key := range allAdminRouteKeys() {
		method, path := splitRoute(key)
		w := f.do(t, viewer, method, path, nil)
		assert.Equal(t, http.StatusForbidden, w.Code, "%s: %s", key, w.Body.String())
		assert.Contains(t, w.Body.String(), "admin role required", key)
	}
}

// With authentication off, the read-only routes open and every other admin
// route still answers 401, whatever the caller sends.
func TestAdminRoutes_AuthOffOpensReadOnlyKeepsDestructive401(t *testing.T) {
	f := setupCredGuardServerWith(t, func(c *config.Config) { c.EnableAuth = false })

	for key := range adminAlwaysAuthenticatedRoutes {
		method, path := splitRoute(key)
		w := f.do(t, "", method, path, nil)
		assert.Equal(t, http.StatusUnauthorized, w.Code, "%s: %s", key, w.Body.String())
		assert.Contains(t, w.Body.String(), "authentication required", key)
	}
	for key := range adminReadOnlyRoutes {
		method, path := splitRoute(key)
		w := f.do(t, "", method, path, nil)
		assert.NotEqual(t, http.StatusUnauthorized, w.Code, "%s: %s", key, w.Body.String())
		assert.NotEqual(t, http.StatusForbidden, w.Code, "%s: %s", key, w.Body.String())
	}

	// A named read-only route answers 200 with real content...
	name := f.srv.listCache.Name()
	require.NotEmpty(t, name)
	w := f.do(t, "", http.MethodGet, "/api/v1/cache/stats/keys?cache="+name, nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"cache":"`+name+`"`)

	// ...and a named destructive one still answers 401 and wipes nothing: the
	// store is intact, so the gate refused it, not the handler.
	w = f.do(t, "", http.MethodPost, "/api/v1/system/reset", map[string]any{"confirm": "RESET"})
	require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
	u, err := f.store.GetUserByID(f.adminID)
	require.NoError(t, err)
	require.NotNil(t, u, "an unauthenticated reset must not have wiped the users")
}
