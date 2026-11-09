// file: internal/server/middleware/auth_admin_scope_test.go
// version: 1.0.1
// guid: 8afdf68e-ebe4-4c3d-a322-5a92e08da421
// last-edited: 2026-11-09

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// everyAtom is a key scope list holding every permission atom (auth.All()).
func everyAtom() []string {
	out := make([]string, 0, len(auth.All()))
	for _, p := range auth.All() {
		out = append(out, string(p))
	}
	return out
}

// TestLegacyRequireAdmin_HonoursKeyScopes is 05-authn §11.1 item 4 at the
// middleware: RequireAdmin, and so the gate adminRole hands out with auth on
// (wire_admin_role.go returns RequireAdmin() there), refuses an admin's API key
// whose non-empty scopes leave out any atom. A key with every atom, a key with
// empty scopes (it holds all of its owner's role permissions) and a session
// pass for an admin; nothing passes for a non-admin. Before early fix 4 the
// gate read only the role name, so a key scoped to one atom passed it.
func TestLegacyRequireAdmin_HonoursKeyScopes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	admin := &database.User{ID: "u-bruce", Username: "bruce-wayne", Status: "active", Roles: []string{"admin"}}
	butler := &database.User{ID: "u-alfred", Username: "alfred-pennyworth", Status: "active", Roles: []string{"viewer"}}
	allButOne := everyAtom()[:len(auth.All())-1]
	const scopeRefusal = "admin role required: this API key's scopes do not include every permission"

	// bindKey binds user and key the way handleAPIKeyAuth does; a nil key
	// records the api_key method with no key on the context.
	bindKey := func(user *database.User, key *database.APIKey) gin.HandlerFunc {
		return func(c *gin.Context) {
			c.Set(contextUserKey, user)
			if key != nil {
				c.Set(contextAPIKeyKey, key)
			}
			c.Request = c.Request.WithContext(auth.WithMethod(auth.WithUser(c.Request.Context(), user), auth.MethodAPIKey))
		}
	}
	bindSession := func(user *database.User) gin.HandlerFunc {
		return func(c *gin.Context) {
			c.Set(contextUserKey, user)
			c.Request = c.Request.WithContext(auth.WithMethod(auth.WithUser(c.Request.Context(), user), auth.MethodSession))
		}
	}
	key := func(scopes []string) *database.APIKey {
		return &database.APIKey{ID: "k-batcave", UserID: admin.ID, Status: "active", Scopes: scopes}
	}

	cases := []struct {
		name    string
		bind    gin.HandlerFunc
		want    int
		message string
	}{
		{"admin key scoped to one atom", bindKey(admin, key([]string{string(auth.PermLibraryView)})), http.StatusForbidden, scopeRefusal},
		{"admin key scoped to users.manage", bindKey(admin, key([]string{string(auth.PermUsersManage)})), http.StatusForbidden, scopeRefusal},
		{"admin key missing one atom", bindKey(admin, key(allButOne)), http.StatusForbidden, scopeRefusal},
		{"admin api_key method with no key on the context", bindKey(admin, nil), http.StatusForbidden, scopeRefusal},
		{"admin key with every atom", bindKey(admin, key(everyAtom())), http.StatusOK, ""},
		{"admin key with empty scopes", bindKey(admin, key(nil)), http.StatusOK, ""},
		{"admin session", bindSession(admin), http.StatusOK, ""},
		{"non-admin key with every atom", bindKey(butler, key(everyAtom())), http.StatusForbidden, "admin role required"},
		{"non-admin session", bindSession(butler), http.StatusForbidden, "admin role required"},
	}

	// Two routes: one behind RequireAdmin directly (admindebug's writes, the
	// system resets), one behind a one-route sub-group carrying the gate
	// adminRole/adminGate hand out with auth on, the way adminGroup mounts it.
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			r.Use(tc.bind)
			r.GET("/require-admin", RequireAdmin(), func(c *gin.Context) { c.Status(http.StatusOK) })
			adminRoleGate := RequireAdmin() // wire_admin_role.go adminRole with auth on
			sub := r.Group("")
			sub.Use(adminRoleGate)
			sub.GET("/admin-role", func(c *gin.Context) { c.Status(http.StatusOK) })

			for _, path := range []string{"/require-admin", "/admin-role"} {
				w := httptest.NewRecorder()
				r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
				assert.Equal(t, tc.want, w.Code, "%s: %s", path, w.Body.String())
				if tc.message != "" {
					assert.Contains(t, w.Body.String(), tc.message, path)
				}
			}
		})
	}

	// No user on the context is still 401.
	t.Run("no user", func(t *testing.T) {
		r := gin.New()
		r.GET("/require-admin", RequireAdmin(), func(c *gin.Context) { c.Status(http.StatusOK) })
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/require-admin", nil))
		assert.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
	})

	// Through the real key verifier: RequireAuth binds the key, RequireAdmin
	// reads what it bound.
	for name, tc := range map[string]struct {
		scopes []string
		want   int
	}{
		"one-atom key via RequireAuth":    {[]string{string(auth.PermLibraryView)}, http.StatusForbidden},
		"every-atom key via RequireAuth":  {everyAtom(), http.StatusOK},
		"empty-scope key via RequireAuth": {nil, http.StatusOK},
	} {
		t.Run(name, func(t *testing.T) {
			store := authMethodStore()
			store.GetAPIKeyByHashFunc = func(string) (*database.APIKey, error) {
				exp := time.Now().Add(time.Hour)
				return &database.APIKey{ID: "k-batcave", UserID: "owner", Status: "active", ExpiresAt: &exp, Scopes: tc.scopes}, nil
			}
			r := gin.New()
			r.Use(RequireAuth(store))
			r.GET("/require-admin", RequireAdmin(), func(c *gin.Context) { c.Status(http.StatusOK) })
			req := httptest.NewRequest(http.MethodGet, "/require-admin", nil)
			req.Header.Set("Authorization", "Bearer abk_two_face")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			assert.Equal(t, tc.want, w.Code, w.Body.String())
		})
	}
}
