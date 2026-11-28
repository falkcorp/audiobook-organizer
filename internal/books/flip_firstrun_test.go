// file: internal/books/flip_firstrun_test.go
// version: 1.0.0
// guid: d4a81e27-5b39-4c60-9f1e-7a3c2b8e0d65
// last-edited: 2026-11-19

package books

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/app"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// bootFlippedFresh boots the composition with the config boot on over an empty
// store: a fresh install, no user, setup not complete, the real core store and
// the real Engine behind GET/PUT /api/v1/config.
func bootFlippedFresh(t *testing.T) *composed {
	t.Helper()
	stateDir := t.TempDir()
	return bootComposedWith(t, false, stateDir, func(c *config.Config) {
		c.RootDir = ""
		c.SetupComplete = false
	}, nil, func(o *app.Options) {
		o.StateDir = stateDir
		// The process environment carries the retired ABK_STATE_DIR the sandbox
		// sets, which the config boot refuses; the boot sees only its own.
		o.Env = bootEnv()
	})
}

// runFlippedFresh runs the composition with the config boot on over an empty
// store: a fresh install, no account, setup not complete, the real core store
// and the real Engine behind GET/PUT /api/v1/config. Run, so core has written
// its bootstrap token and serves.
func runFlippedFresh(t *testing.T) *runningApp {
	t.Helper()
	stateDir := t.TempDir()
	return runComposedIn(t, stateDir, func(c *config.Config) {
		c.RootDir = ""
		c.SetupComplete = false
	}, nil, func(o *app.Options) {
		o.StateDir = stateDir
		// The process environment carries the retired ABK_STATE_DIR the sandbox
		// sets, which the config boot refuses; the boot sees only its own.
		o.Env = bootEnv()
	}, nil)
}

// firstAdminUser and firstAdminPassword are the account the setup page creates.
const (
	firstAdminUser     = "bruce.wayne"
	firstAdminPassword = "correct-horse-batman-staple-9"
)

// setupFirstAdmin creates the first account as the SPA's setup page does
// (web/src/services/coreIdentity.ts setupAdmin): core's POST
// /api/core/v1/auth/setup with the bootstrap token the boot wrote, then signs
// it in through core's login.
func setupFirstAdmin(t *testing.T, r *runningApp) *coreAdmin {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(stateDirOf(t), ".bootstrap-token"))
	require.NoError(t, err, "core wrote no bootstrap token where the runbook reads it")
	body := map[string]string{"username": firstAdminUser, "password": firstAdminPassword, "bootstrap_token": strings.TrimSpace(string(raw))}
	var rec *httptest.ResponseRecorder
	require.Eventually(t, func() bool {
		rec = post(t, r.app.Handler(), "/api/core/v1/auth/setup", body, false)
		return rec.Code != http.StatusTooEarly
	}, 30*time.Second, 50*time.Millisecond, "the app never became ready")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return loginCoreAdmin(t, r, firstAdminUser, firstAdminPassword)
}

// putConfig is PUT /api/v1/config with the set, signed in as a when it is not
// nil and anonymous otherwise.
func putConfig(t *testing.T, r *runningApp, a *coreAdmin, set map[string]any) (int, string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"set": set})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "192.0.2.10:41000"
	if a != nil {
		req.AddCookie(a.cookie)
	}
	w := httptest.NewRecorder()
	r.app.Handler().ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

// TestFirstRun_NoAccountNoConfigWrite: a fresh install has no first-run config
// window. Before the first account exists, an anonymous PUT /api/v1/config is
// refused 401, whether it names only the setup wizard's keys or a sign-in
// setting beside them, and nothing is written. The window HEAD opened while
// its user count read zero would, under core identity, have stayed open on
// every migrated install (M3 drains books' user rows, so the count is always
// zero): setup goes through core's first administrator instead.
func TestFirstRun_NoAccountNoConfigWrite(t *testing.T) {
	r := runFlippedFresh(t)
	before := config.Snapshot()
	for name, set := range map[string]map[string]any{
		"wizard save":            {"root_dir": filepath.Join(t.TempDir(), "library"), "playlist_dir": filepath.Join(t.TempDir(), "playlists"), "setup_complete": true},
		"auth switch":            {"enable_auth": true},
		"library path and auth":  {"root_dir": filepath.Join(t.TempDir(), "library"), "enable_auth": true},
		"library path and oauth": {"root_dir": filepath.Join(t.TempDir(), "library"), "oauth_enabled": true},
		"basic auth credentials": {"basic_auth_enabled": true, "basic_auth_username": "bruce"},
	} {
		code, body := putConfig(t, r, nil, set)
		require.Equal(t, http.StatusUnauthorized, code, "%s: %s", name, body)
		require.Empty(t, config.Snapshot().RootDir, "%s wrote the library path", name)
		require.False(t, config.Snapshot().SetupComplete, "%s completed setup", name)
		require.Equal(t, before.EnableAuth, config.Snapshot().EnableAuth, "%s changed the auth switch", name)
		require.Equal(t, before.OAuthEnabled, config.Snapshot().OAuthEnabled, "%s changed oauth", name)
	}
}

// TestFirstRun_WizardWritesOverTheRealCoreStore: admin first. The setup page
// creates the first administrator through core, and the wizard's save (library
// path, playlists, setup_complete), made in that administrator's session,
// commits through the real Engine and core's store, and the next read shows it.
func TestFirstRun_WizardWritesOverTheRealCoreStore(t *testing.T) {
	r := runFlippedFresh(t)
	users, err := r.store.ListUsers()
	require.NoError(t, err)
	// Under core identity the store lists core's accounts, and a fresh install
	// holds exactly one: core's _system attribution account, which cannot sign
	// in (HEAD's ListUsers listed its _system row the same way).
	require.Len(t, users, 1, "a fresh install has no account but core's system account: %v", users)
	require.Equal(t, "_system", users[0].ID)
	require.Equal(t, "system", users[0].Status)
	admin := setupFirstAdmin(t, r)

	lib := filepath.Join(t.TempDir(), "library")
	playlists := filepath.Join(t.TempDir(), "playlists")
	code, body := putConfig(t, r, admin, map[string]any{"root_dir": lib, "playlist_dir": playlists, "setup_complete": true})
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, lib, config.Snapshot().RootDir, "the write reached the projection")
	require.Equal(t, playlists, config.Snapshot().PlaylistDir)
	require.True(t, config.Snapshot().SetupComplete)
}

// TestFirstRun_OnlyTheWizardsKeysOverTheRealCoreStore: the first
// administrator's session is not the owner's. With no owner configured, a save
// that names the auth switch (an owner trust-root setting), alone or beside
// the library path, is refused whole, and nothing is written.
func TestFirstRun_OnlyTheWizardsKeysOverTheRealCoreStore(t *testing.T) {
	r := runFlippedFresh(t)
	admin := setupFirstAdmin(t, r)
	before := config.Snapshot()
	for name, set := range map[string]map[string]any{
		"auth switch":           {"enable_auth": !before.EnableAuth},
		"library path and auth": {"root_dir": filepath.Join(t.TempDir(), "library"), "enable_auth": !before.EnableAuth},
	} {
		code, body := putConfig(t, r, admin, set)
		require.Equal(t, http.StatusForbidden, code, "%s: %s", name, body)
		require.Empty(t, config.Snapshot().RootDir, "%s wrote the library path", name)
		require.Equal(t, before.EnableAuth, config.Snapshot().EnableAuth, "%s changed the auth switch", name)
	}
}
