// file: internal/books/flip_firstrun_test.go
// version: 1.0.0
// guid: d4a81e27-5b39-4c60-9f1e-7a3c2b8e0d65
// last-edited: 2026-11-19

package books

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

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

func putConfig(t *testing.T, c *composed, set map[string]any) (int, string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"set": set})
	require.NoError(t, err)
	w := c.serve(http.MethodPut, "/api/v1/config", body)
	return w.Code, w.Body.String()
}

// TestFirstRun_WizardWritesOverTheRealCoreStore: with no user and setup not
// complete, the setup wizard's save (library path, playlists, setup_complete)
// commits through the real Engine and core's store, and the next read shows it;
// a key outside the wizard's list is refused in the same window, and after
// setup_complete the window is shut.
func TestFirstRun_WizardWritesOverTheRealCoreStore(t *testing.T) {
	c := bootFlippedFresh(t)
	users, err := c.store.ListUsers()
	require.NoError(t, err)
	require.Empty(t, users, "a fresh install has no user")

	lib := filepath.Join(t.TempDir(), "library")
	playlists := filepath.Join(t.TempDir(), "playlists")
	code, body := putConfig(t, c, map[string]any{"root_dir": lib, "playlist_dir": playlists, "setup_complete": true})
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, lib, config.Snapshot().RootDir, "the write reached the projection")
	require.True(t, config.Snapshot().SetupComplete)

	// The window shuts with setup_complete: the same write is now refused.
	code, body = putConfig(t, c, map[string]any{"root_dir": filepath.Join(t.TempDir(), "other")})
	require.Equal(t, http.StatusForbidden, code, body)
	require.Equal(t, lib, config.Snapshot().RootDir)
}

// TestFirstRun_OnlyTheWizardsKeysOverTheRealCoreStore: the window grants nothing
// beyond the wizard's keys, as core itself sees it: an admin key outside the list
// (here the auth switch), alone or beside the library path, is refused whole and
// nothing is written.
func TestFirstRun_OnlyTheWizardsKeysOverTheRealCoreStore(t *testing.T) {
	c := bootFlippedFresh(t)
	before := config.Snapshot()
	for name, set := range map[string]map[string]any{
		"auth switch":            {"enable_auth": true},
		"library path and auth":  {"root_dir": filepath.Join(t.TempDir(), "library"), "enable_auth": true},
		"library path and oauth": {"root_dir": filepath.Join(t.TempDir(), "library"), "oauth_enabled": true},
		"basic auth credentials": {"basic_auth_enabled": true, "basic_auth_username": "bruce"},
	} {
		code, body := putConfig(t, c, set)
		require.Equal(t, http.StatusForbidden, code, "%s: %s", name, body)
		require.Empty(t, config.Snapshot().RootDir, "%s wrote the library path", name)
		require.Equal(t, before.EnableAuth, config.Snapshot().EnableAuth, "%s changed the auth switch", name)
		require.Equal(t, before.OAuthEnabled, config.Snapshot().OAuthEnabled, "%s changed oauth", name)
	}
}
