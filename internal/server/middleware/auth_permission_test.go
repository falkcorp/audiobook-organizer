// file: internal/server/middleware/auth_permission_test.go
// version: 1.0.0
// guid: 4f8d2c1a-5e9b-4f70-b7c6-2d8e0f1b9a47

package middleware

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/gin-gonic/gin"
)

// setupAuthTestStore returns a PebbleStore with seed roles + one
// admin user and one viewer user, each with a session.
func setupAuthTestStore(t *testing.T) (database.Store, string, string) {
	t.Helper()
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	if _, _, err := auth.SeedRoles(store); err != nil {
		t.Fatalf("seed roles: %v", err)
	}

	admin, err := store.CreateUser("admin", "admin@x.test", "bcrypt", "hash", []string{auth.SeedRoleAdmin}, "active")
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	viewer, err := store.CreateUser("viewer", "viewer@x.test", "bcrypt", "hash", []string{auth.SeedRoleViewer}, "active")
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}

	adminSession, err := store.CreateSession(admin.ID, "127.0.0.1", "test", time.Hour)
	if err != nil {
		t.Fatalf("create admin session: %v", err)
	}
	viewerSession, err := store.CreateSession(viewer.ID, "127.0.0.1", "test", time.Hour)
	if err != nil {
		t.Fatalf("create viewer session: %v", err)
	}
	return store, adminSession.ID, viewerSession.ID
}

func TestRequirePermission_AdminAllowed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, adminToken, _ := setupAuthTestStore(t)

	r := gin.New()
	r.Use(RequireAuth(store))
	r.GET("/users", RequirePermission(store, auth.PermUsersManage), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	req := httptest.NewRequest(http.MethodGet, "/users", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("admin → users.manage: status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
}

func TestRequirePermission_ViewerRejected(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, _, viewerToken := setupAuthTestStore(t)

	r := gin.New()
	r.Use(RequireAuth(store))
	r.GET("/users", RequirePermission(store, auth.PermUsersManage), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	req := httptest.NewRequest(http.MethodGet, "/users", nil)
	req.Header.Set("Authorization", "Bearer "+viewerToken)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("viewer → users.manage: status = %d, want 403; body=%s", w.Code, w.Body.String())
	}
}

func TestRequirePermission_ViewerAllowedForLibraryView(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, _, viewerToken := setupAuthTestStore(t)

	r := gin.New()
	r.Use(RequireAuth(store))
	r.GET("/books", RequirePermission(store, auth.PermLibraryView), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	req := httptest.NewRequest(http.MethodGet, "/books", nil)
	req.Header.Set("Authorization", "Bearer "+viewerToken)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("viewer → library.view: status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
}

func TestRequirePermission_Unauthenticated(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, _, _ := setupAuthTestStore(t)

	r := gin.New()
	r.Use(RequireAuth(store))
	r.GET("/books", RequirePermission(store, auth.PermLibraryView), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	req := httptest.NewRequest(http.MethodGet, "/books", nil)
	// No Authorization header → RequireAuth responds 401 before
	// RequirePermission runs.
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated: status = %d, want 401; body=%s", w.Code, w.Body.String())
	}
}

func TestRequirePermission_FirstRunBootstrapBypass(t *testing.T) {
	// With no users in the DB, permission checks are bypassed so the
	// /setup wizard can run.
	gin.SetMode(gin.TestMode)
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	r := gin.New()
	r.Use(RequireAuth(store))
	r.POST("/setup", RequirePermission(store, auth.PermUsersManage), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	req := httptest.NewRequest(http.MethodPost, "/setup", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("first-run: status = %d, want 200 (bypass); body=%s", w.Code, w.Body.String())
	}
}

func TestRequirePermission_UserCtxFromAuth(t *testing.T) {
	// Verify that the handler can read the user + permissions from
	// the request context populated by RequireAuth.
	gin.SetMode(gin.TestMode)
	store, adminToken, _ := setupAuthTestStore(t)

	r := gin.New()
	r.Use(RequireAuth(store))
	r.GET("/whoami", func(c *gin.Context) {
		u, ok := auth.UserFromContext(c.Request.Context())
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "no user"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"username": u.Username, "can_users_manage": auth.Can(c.Request.Context(), auth.PermUsersManage)})
	})

	req := httptest.NewRequest(http.MethodGet, "/whoami", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !contains(body, `"username":"admin"`) {
		t.Errorf("body missing username: %s", body)
	}
	if !contains(body, `"can_users_manage":true`) {
		t.Errorf("admin should have users.manage: %s", body)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
