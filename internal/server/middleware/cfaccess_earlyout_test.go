// file: internal/server/middleware/cfaccess_earlyout_test.go
// version: 1.0.0
// guid: 2c9e0b74-6a31-4d58-8f07-1b5a6c2e9d43

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The RequireAuth early-out (skip session check when contextUserKey is already set) is
// load-bearing. This asserts it CANNOT fire spuriously: a request with no session
// token, no API key, and no upstream-bound user still gets 401 on a protected route.
func TestRequireAuth_NoCredentials_Still401(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, _, _ := setupAuthTestStore(t) // a user exists → auth is enforced (not bootstrap mode)

	r := gin.New()
	r.Use(RequireAuth(store))
	r.GET("/protected", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no credentials must 401; got %d (body=%s)", w.Code, w.Body.String())
	}
}

// Conversely, an upstream stage that binds a user (as CloudflareAccessAuth does after a
// verified JWT + allowlist) lets RequireAuth admit the request without a session token.
// This documents/locks the intended early-out behavior.
func TestRequireAuth_UpstreamBoundUser_Admitted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, _, _ := setupAuthTestStore(t)

	bindUser := func(c *gin.Context) {
		c.Set(contextUserKey, &database.User{ID: "u-cf", Username: "cf", Status: "active"})
		c.Next()
	}

	r := gin.New()
	r.Use(bindUser, RequireAuth(store))
	r.GET("/protected", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("an upstream-bound user should be admitted; got %d (body=%s)", w.Code, w.Body.String())
	}
}
