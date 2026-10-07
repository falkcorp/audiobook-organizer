// file: internal/server/apikey_credential_guard_test.go
// version: 1.1.0
// guid: 3e9a5c72-4b1d-4f08-b6e3-9d2c7a0f5e14
// last-edited: 2026-10-07

// Router-level coverage for the credential guard (2026-10-07): an API key
// (here an all-scope admin key, the strongest there is) gets 403 from the
// guard on every route that changes credentials or identity, and a signed-in
// password session gets the same request through. Synthetic users only.

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

type credGuardFixture struct {
	srv          *Server
	store        *database.PebbleStore
	adminID      string
	sessionToken string // password-origin: an interactive sign-in
	apiKey       string // all scopes, owned by the admin
	targetID     string // a second user the requests act on
	targetKeyID  string // an API key owned by the target user
}

func setupCredGuardServer(t *testing.T) *credGuardFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	origCfg := config.AppConfig
	tempDir := t.TempDir()
	config.AppConfig = config.Config{
		DatabaseType: "pebble",
		DatabasePath: filepath.Join(tempDir, "test.pebble"),
		RootDir:      tempDir,
		EnableAuth:   true,
	}
	store, err := database.NewPebbleStoreInMemory(config.AppConfig.DatabasePath)
	require.NoError(t, err)
	database.SetGlobalStore(store)
	require.NoError(t, database.RunMigrations(store))
	_, _, err = auth.SeedRoles(store)
	require.NoError(t, err)

	admin, err := store.CreateUser("synthetic-admin", "admin@example.test", "bcrypt", "x", []string{auth.SeedRoleAdmin}, "active")
	require.NoError(t, err)
	target, err := store.CreateUser("synthetic-target", "target@example.test", "bcrypt", "x", []string{auth.SeedRoleAdmin}, "active")
	require.NoError(t, err)

	// CreateSession records no origin, which RequireAuth treats as delegated;
	// a password-origin session is the user's own sign-in.
	sess, err := store.CreateSessionWithOrigin(admin.ID, "127.0.0.1", "test", time.Hour, database.SessionOriginPassword)
	require.NoError(t, err)

	raw, hash, err := database.GenerateAPIKeyToken()
	require.NoError(t, err)
	exp := time.Now().Add(time.Hour)
	_, err = store.CreateAPIKey(&database.APIKey{
		UserID: admin.ID, Name: "synthetic admin key", TokenHash: hash,
		Scopes: auth.All(), Status: "active", CreatedAt: time.Now(), ExpiresAt: &exp,
	})
	require.NoError(t, err)

	_, thash, err := database.GenerateAPIKeyToken()
	require.NoError(t, err)
	tk, err := store.CreateAPIKey(&database.APIKey{
		UserID: target.ID, Name: "target key", TokenHash: thash,
		Scopes: []string{string(auth.PermLibraryView)}, Status: "active", CreatedAt: time.Now(), ExpiresAt: &exp,
	})
	require.NoError(t, err)

	srv := newTestServer(t, store)
	if srv.opRegistry != nil {
		srv.opRegistry.Start(context.Background())
	}
	t.Cleanup(func() {
		if srv.opRegistry != nil {
			_ = srv.opRegistry.Shutdown(context.Background())
		}
		if srv.fileIOPool != nil {
			srv.fileIOPool.Stop()
		}
		if srv.writeBackBatcher != nil {
			_ = srv.writeBackBatcher.Stop(context.Background())
		}
		database.SetGlobalStore(nil)
		store.Close()
		_ = os.RemoveAll(tempDir)
		config.AppConfig = origCfg
	})
	return &credGuardFixture{
		srv: srv, store: store, adminID: admin.ID, sessionToken: sess.ID, apiKey: raw,
		targetID: target.ID, targetKeyID: tk.ID,
	}
}

func (f *credGuardFixture) do(t *testing.T, bearer, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	w := httptest.NewRecorder()
	f.srv.router.ServeHTTP(w, req)
	return w
}

type guardedRoute struct {
	name   string
	method string
	path   func(f *credGuardFixture) string
	body   func(f *credGuardFixture) any
	okCode int
}

func guardedRoutes() []guardedRoute {
	return []guardedRoute{
		{"admin password reset", http.MethodPut, func(*credGuardFixture) string { return "/api/v1/auth/me/password" },
			func(f *credGuardFixture) any {
				return map[string]any{"user_id": f.targetID, "new_password": "synthetic-pass-123"}
			}, http.StatusNoContent},
		{"email change", http.MethodPatch, func(*credGuardFixture) string { return "/api/v1/auth/me" },
			func(*credGuardFixture) any { return map[string]any{"email": "changed@example.test"} }, http.StatusOK},
		{"temp-login mint", http.MethodPost, func(*credGuardFixture) string { return "/api/v1/auth/temp-tokens" },
			func(f *credGuardFixture) any { return map[string]any{"user_id": f.targetID} }, http.StatusCreated},
		{"reset-password link", http.MethodPost, func(f *credGuardFixture) string { return "/api/v1/users/" + f.targetID + "/reset-password" },
			func(*credGuardFixture) any { return nil }, http.StatusOK},
		{"invite (user create)", http.MethodPost, func(*credGuardFixture) string { return "/api/v1/users/invite" },
			func(*credGuardFixture) any {
				return map[string]any{"username": "synthetic-new", "role_id": auth.SeedRoleAdmin}
			}, http.StatusCreated},
		{"deactivate user", http.MethodPost, func(f *credGuardFixture) string { return "/api/v1/users/" + f.targetID + "/deactivate" },
			func(*credGuardFixture) any { return nil }, http.StatusOK},
		{"reactivate user", http.MethodPost, func(f *credGuardFixture) string { return "/api/v1/users/" + f.targetID + "/reactivate" },
			func(*credGuardFixture) any { return nil }, http.StatusOK},
		{"api key for another user", http.MethodPost, func(*credGuardFixture) string { return "/api/v1/auth/api-keys" },
			func(f *credGuardFixture) any { return map[string]any{"name": "for target", "user_id": f.targetID} }, http.StatusCreated},
		{"rotate another user's key", http.MethodPost, func(f *credGuardFixture) string { return "/api/v1/auth/api-keys/" + f.targetKeyID + "/rotate" },
			func(*credGuardFixture) any { return nil }, http.StatusCreated},
		{"sign-in setting change", http.MethodPut, func(*credGuardFixture) string { return "/api/v1/config" },
			func(*credGuardFixture) any { return map[string]any{"oauth_default_role": "admin"} }, http.StatusOK},
	}
}

func TestCredentialGuard_APIKeyRefusedEverywhere_SessionAllowed(t *testing.T) {
	f := setupCredGuardServer(t)

	// The key authenticates: a route outside the guard answers it.
	w := f.do(t, f.apiKey, http.MethodGet, "/api/v1/auth/me", nil)
	require.Equal(t, http.StatusOK, w.Code, "the API key must authenticate at all: %s", w.Body.String())

	routes := guardedRoutes()
	// API key first: every request must be refused and change nothing, so
	// the session runs below see untouched state.
	for _, r := range routes {
		t.Run("api key/"+r.name, func(t *testing.T) {
			w := f.do(t, f.apiKey, r.method, r.path(f), r.body(f))
			assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
			assert.Contains(t, w.Body.String(), servermiddleware.CredentialChangeRefusedMessage,
				"the 403 must come from the credential guard, not a permission check")
		})
	}
	target, err := f.store.GetUserByID(f.targetID)
	require.NoError(t, err)
	assert.Equal(t, "x", target.PasswordHash, "a refused reset must not change the password")
	assert.Equal(t, "active", target.Status)

	for _, r := range routes {
		t.Run("session/"+r.name, func(t *testing.T) {
			w := f.do(t, f.sessionToken, r.method, r.path(f), r.body(f))
			assert.Equal(t, r.okCode, w.Code, w.Body.String())
		})
	}
}

// A route the guard does not cover keeps working for a key: PUT /config
// with an unchanged sign-in value (a GET-then-PUT round trip).
func TestCredentialGuard_ConfigRoundTripAllowedForKey(t *testing.T) {
	f := setupCredGuardServer(t)
	w := f.do(t, f.apiKey, http.MethodPut, "/api/v1/config", map[string]any{"enable_auth": true})
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

// TestCredentialGuard_ConfigCaseVariantKeyRefused is the 2026-10-07 review's
// parser differential through the real router: the sign-in check matched
// payload keys exactly while the decoder matched them case-insensitively, so
// an API key's {"OAuth_Default_Role":"admin"} answered 200 and made every new
// SSO user an admin.
func TestCredentialGuard_ConfigCaseVariantKeyRefused(t *testing.T) {
	f := setupCredGuardServer(t)
	for _, payload := range []map[string]any{
		{"OAuth_Default_Role": "admin"},
		{"OAUTH_ALLOWED_EMAILS": "attacker@example.test"},
		{"Root_Dir": "/elsewhere"},
	} {
		w := f.do(t, f.apiKey, http.MethodPut, "/api/v1/config", payload)
		assert.GreaterOrEqual(t, w.Code, 400, "%v: %s", payload, w.Body.String())
	}
	cfg := config.Snapshot()
	assert.NotEqual(t, "admin", cfg.OAuthDefaultRole)
	assert.Empty(t, cfg.OAuthAllowedEmails)

	// Path settings need a session too; a key's same-value round trip works.
	w := f.do(t, f.apiKey, http.MethodPut, "/api/v1/config", map[string]any{"root_dir": t.TempDir()})
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "refused_keys")
	w = f.do(t, f.apiKey, http.MethodPut, "/api/v1/config", map[string]any{"root_dir": cfg.RootDir})
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
	w = f.do(t, f.sessionToken, http.MethodPut, "/api/v1/config", map[string]any{"root_dir": t.TempDir()})
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

// TestCredentialGuard_SiblingRoutesRefuseKey: the routes the 2026-10-07
// review's sibling-path finding turned up, each a way around the guard that
// the first inventory missed — re-enabling a deactivated key, installing a
// program, adding a scan root, writing free-form plugin settings, replacing
// the server binary.
func TestCredentialGuard_SiblingRoutesRefuseKey(t *testing.T) {
	f := setupCredGuardServer(t)
	routes := []struct{ method, path string }{
		{http.MethodPatch, "/api/v1/auth/api-keys/" + f.targetKeyID},
		{http.MethodPost, "/api/v1/tools/fpcalc/install"},
		{http.MethodPost, "/api/v1/import-paths"},
		{http.MethodPut, "/api/v1/plugins/synthetic/settings"},
		{http.MethodPost, "/api/v1/update/apply"},
	}
	for _, r := range routes {
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			w := f.do(t, f.apiKey, r.method, r.path, map[string]any{"status": "active", "path": t.TempDir(), "name": "x"})
			assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
			assert.Contains(t, w.Body.String(), servermiddleware.CredentialChangeRefusedMessage)
		})
	}
}

// TestCredentialGuard_KeyWithoutExpiryRefused: a key with no expiry (here
// written straight to the store, as a restored backup would) is refused, not
// treated as never expiring.
func TestCredentialGuard_KeyWithoutExpiryRefused(t *testing.T) {
	f := setupCredGuardServer(t)
	raw, hash, err := database.GenerateAPIKeyToken()
	require.NoError(t, err)
	_, err = f.store.CreateAPIKey(&database.APIKey{
		UserID: f.adminID, Name: "legacy", TokenHash: hash, Scopes: auth.All(), Status: "active", CreatedAt: time.Now(),
	})
	require.NoError(t, err)
	w := f.do(t, raw, http.MethodGet, "/api/v1/auth/me", nil)
	assert.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
	w = f.do(t, raw, http.MethodPost, "/api/v1/auth/api-keys", map[string]any{"name": "child", "expires_in_days": 365})
	assert.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
}

// Store-replacing routes: a reset or factory reset wipes every user (after
// which the public /auth/setup mints a new admin), and a backup restore
// brings back the backup's users and password hashes. The session request
// carries an empty body, so the handler's own 400 proves the guard let it
// through without wiping the test's store.
func TestCredentialGuard_StoreReplacingRoutes(t *testing.T) {
	f := setupCredGuardServer(t)
	routes := []struct{ name, path string }{
		{"system reset", "/api/v1/system/reset"},
		{"factory reset", "/api/v1/system/factory-reset"},
		{"backup restore", "/api/v1/backup/restore"},
	}
	for _, r := range routes {
		t.Run("api key/"+r.name, func(t *testing.T) {
			w := f.do(t, f.apiKey, http.MethodPost, r.path, map[string]any{"confirm": "RESET", "backup_filename": "x.tar.gz"})
			assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
			assert.Contains(t, w.Body.String(), servermiddleware.CredentialChangeRefusedMessage)
		})
		t.Run("session/"+r.name, func(t *testing.T) {
			w := f.do(t, f.sessionToken, http.MethodPost, r.path, map[string]any{})
			assert.Equal(t, http.StatusBadRequest, w.Code, "guard must let a session through to the handler: %s", w.Body.String())
		})
	}
	n, err := f.store.CountUsers()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, n, 2, "no route may have wiped the users")
}
