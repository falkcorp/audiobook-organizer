// file: internal/server/handlers/auth_permissions_flow_test.go
// version: 1.0.0
// guid: 7b1d3a5e-92c4-4f06-8e1a-5c0d6f2a9b34
// last-edited: 2026-10-18

package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// TestAuthPermissions_RealMiddlewareFlow runs login and /auth/me through the
// real RequireAuth middleware over a real store, so the two paths that compute
// the permission set (Login from the roles, Me from the request context the
// middleware bound) are shown to agree, and an API key's scopes are shown to
// narrow what /auth/me reports.
func TestAuthPermissions_RealMiddlewareFlow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	_, _, err = auth.SeedRoles(store)
	require.NoError(t, err)
	_, err = store.CreateRole(&database.Role{
		ID: "curator", Name: "curator",
		Permissions: []string{auth.PermLibraryView, auth.PermCollectionsManage},
	})
	require.NoError(t, err)

	hash, err := bcrypt.GenerateFromPassword([]byte("pw-pw-pw-pw"), bcrypt.MinCost)
	require.NoError(t, err)
	curator, err := store.CreateUser("curator-user", "c@example.test", "bcrypt", string(hash), []string{"curator"}, "active")
	require.NoError(t, err)
	_, err = store.CreateUser("viewer-user", "v@example.test", "bcrypt", string(hash), []string{auth.SeedRoleViewer}, "active")
	require.NoError(t, err)

	h := handlers.NewAuthHandler(store, true)
	r := gin.New()
	r.POST("/auth/login", h.Login)
	r.GET("/auth/me", servermiddleware.RequireAuth(store), h.Me)

	login := func(username string) (cookie string, perms []string) {
		b, _ := json.Marshal(map[string]string{"username": username, "password": "pw-pw-pw-pw"})
		req := httptest.NewRequest(http.MethodPost, "/auth/login", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		for _, c := range w.Result().Cookies() {
			if c.Name == servermiddleware.SessionCookieName {
				cookie = c.Value
			}
		}
		require.NotEmpty(t, cookie)
		return cookie, decodePerms(t, w)
	}
	me := func(set func(*http.Request)) []string {
		req := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
		set(req)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		return decodePerms(t, w)
	}

	cookie, loginPerms := login("curator-user")
	assert.Contains(t, loginPerms, auth.PermCollectionsManage)
	mePerms := me(func(req *http.Request) {
		req.AddCookie(&http.Cookie{Name: servermiddleware.SessionCookieName, Value: cookie})
	})
	assert.ElementsMatch(t, loginPerms, mePerms, "login and /auth/me must report the same set for the same session")

	vCookie, vLogin := login("viewer-user")
	assert.NotContains(t, vLogin, auth.PermCollectionsManage)
	assert.NotContains(t, me(func(req *http.Request) {
		req.AddCookie(&http.Cookie{Name: servermiddleware.SessionCookieName, Value: vCookie})
	}), auth.PermCollectionsManage)

	// An API key for the curator, scoped to library.view only: the role holds
	// collections.manage but this request cannot use it.
	raw, keyHash, err := database.GenerateAPIKeyToken()
	require.NoError(t, err)
	exp := time.Now().Add(time.Hour)
	_, err = store.CreateAPIKey(&database.APIKey{
		UserID: curator.ID, Name: "scoped", TokenHash: keyHash,
		Scopes: []string{auth.PermLibraryView}, Status: "active", ExpiresAt: &exp,
	})
	require.NoError(t, err)
	scoped := me(func(req *http.Request) { req.Header.Set("Authorization", "Bearer "+raw) })
	assert.Equal(t, []string{auth.PermLibraryView}, scoped)
}

func decodePerms(t *testing.T, w *httptest.ResponseRecorder) []string {
	t.Helper()
	var resp struct {
		Data struct {
			User struct {
				Permissions []string `json:"permissions"`
			} `json:"user"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), w.Body.String())
	return resp.Data.User.Permissions
}
