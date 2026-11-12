// file: internal/cfaccess/owner_guard_test.go
// version: 1.0.0
// guid: ae1a808d-30fe-4c2f-ab52-e016ba38611b
// last-edited: 2026-11-11

package cfaccess

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
)

// The route gate HEAD's gin chain keeps until F4-08 asks for the same thing
// the extension asserts: a verified Access sign-in whose e-mail is the
// configured owner's, never a password, key or any other method.
func TestOwnerGuard_GateRefusesWithoutOwnerProof(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const perm auth.Permission = "library.edit"
	for _, tc := range []struct {
		name   string
		method auth.Method
		email  string
		owner  string
		perms  []auth.Permission
		want   int
	}{
		{"owner through Access with the permission", auth.MethodCFAccess, ownerMail, ownerMail, []auth.Permission{perm}, http.StatusOK},
		{"owner through Access without the permission", auth.MethodCFAccess, ownerMail, ownerMail, nil, http.StatusForbidden},
		{"another Access user", auth.MethodCFAccess, "dick@wayne.example.test", ownerMail, []auth.Permission{perm}, http.StatusForbidden},
		{"the owner's e-mail through a password session", auth.MethodSession, ownerMail, ownerMail, []auth.Permission{perm}, http.StatusForbidden},
		{"the owner's e-mail through an API key", auth.MethodAPIKey, ownerMail, ownerMail, []auth.Permission{perm}, http.StatusForbidden},
		{"no owner configured", auth.MethodCFAccess, ownerMail, "", []auth.Permission{perm}, http.StatusForbidden},
		{"Access with no e-mail", auth.MethodCFAccess, "", ownerMail, []auth.Permission{perm}, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			r.Use(func(c *gin.Context) {
				ctx := auth.WithMethod(c.Request.Context(), tc.method)
				ctx = auth.WithAccessEmail(ctx, tc.email)
				ctx = auth.WithPermissions(ctx, tc.perms)
				c.Request = c.Request.WithContext(ctx)
				c.Next()
			})
			r.POST("/owner", RequireOwner(func() string { return tc.owner }, perm, nil), func(c *gin.Context) { c.Status(http.StatusOK) })
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/owner", nil))
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.want, w.Body.String())
			}
		})
	}
	t.Run("applies skips the gate for a preview", func(t *testing.T) {
		r := gin.New()
		r.POST("/owner", RequireOwner(func() string { return ownerMail }, perm, func(*gin.Context) bool { return false }),
			func(c *gin.Context) { c.Status(http.StatusOK) })
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/owner", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d", w.Code)
		}
	})
	t.Run("the owner host hint comes from the first forwarded host", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "http://internal.example.test/", nil)
		r.Header.Set("X-Forwarded-Host", " signal.example.test , other.example.test")
		if got := OwnerPublicHost(r); got != "signal.example.test" {
			t.Fatalf("host = %q", got)
		}
		r.Header.Del("X-Forwarded-Host")
		if got := OwnerPublicHost(r); got != "internal.example.test" {
			t.Fatalf("host = %q", got)
		}
	})
}
