// file: internal/server/middleware/absauth_apikey_mode_test.go
// version: 1.0.0
// guid: 3a7f92c8-6d41-4b05-8e27-9c14b0fa5e63
// last-edited: 2026-08-03

// These are the API-key tests that reference symbols INTRODUCED by this change
// (ABSModeAPIKey, ResolveAPIKey). They live in their own file so that
// absauth_apikey_test.go — the behavioural regression — still compiles when
// absauth.go is reverted to main, and therefore fails on the actual 401-vs-200
// regression rather than on a build error.

package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/gin-gonic/gin"
)

// The resolved mode is reported as "apikey" so the audit log and /api/me can tell
// the three credential types apart.
func TestABSAPIKey_ReportsAPIKeyMode(t *testing.T) {
	h := newAPIKeyHarness(t)
	h.store.addUser(&database.User{ID: "u-admin", Username: "admin", Status: "active"})
	h.store.addKey("abk_valid_token", &database.APIKey{ID: "k1", UserID: "u-admin", Status: "active"})

	w := h.get("abk_valid_token")
	if !strings.Contains(w.Body.String(), ABSModeAPIKey) {
		t.Fatalf("expected mode %q in body, got %s", ABSModeAPIKey, w.Body.String())
	}
}

// 🔑 Scope narrowing. A key must not reach MORE through ABS than through /api/v1.
// Without the intersectPermissions call in Bind, a read-only key would silently
// become a full-privilege key the moment it was pointed at an ABS route.
func TestABSAPIKey_ScopesNarrowPermissions(t *testing.T) {
	h := newAPIKeyHarness(t)
	h.store.addUser(&database.User{ID: "u1", Username: "admin", Status: "active", Roles: []string{"admin"}})
	h.store.roles["admin"] = &database.Role{
		ID:          "admin",
		Permissions: []string{string(auth.PermLibraryView), string(auth.PermSettingsManage)},
	}
	// Scoped to VIEW only, though the owner's role also grants manage.
	h.store.addKey("abk_scoped", &database.APIKey{
		ID: "k1", UserID: "u1", Status: "active",
		Scopes: []string{string(auth.PermLibraryView)},
	})

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/api/me", nil)
	c.Request.Header.Set("Authorization", "Bearer abk_scoped")

	id, aerr := h.resolver.ResolveAPIKey(c)
	if aerr != nil || id == nil {
		t.Fatalf("expected the scoped key to resolve, got id=%v err=%v", id, aerr)
	}
	h.resolver.Bind(c, id)

	if !auth.Can(c.Request.Context(), auth.PermLibraryView) {
		t.Fatal("scoped permission library:view was lost")
	}
	if auth.Can(c.Request.Context(), auth.PermSettingsManage) {
		t.Fatal("a key scoped to library:view escalated to settings:manage on the ABS surface")
	}
}
