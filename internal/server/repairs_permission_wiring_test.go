// file: internal/server/repairs_permission_wiring_test.go
// version: 1.0.0
// guid: 888ff2ac-f8df-4f70-919b-262570c6492b
// last-edited: 2026-10-14

// The Repairs lane's per-fixer permission check as the server wires it
// (wireRepairsRoutes + permCheck): the same semantics as s.perm, driven
// through the real router for the Missing files fixer, which requires
// settings.manage.

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

const missingFilesPlanPath = "/api/v1/repairs/reconcile.missing-files/plan"

// repairsPermServer builds a server with EnableAuth set to enableAuth and,
// when withUsers, seeded roles plus an editor (library.edit_metadata, no
// settings.manage) and an admin, returning their session tokens.
func repairsPermServer(t *testing.T, enableAuth, withUsers bool) (srv *Server, editorToken, adminToken string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	origCfg := config.AppConfig
	tempDir, err := os.MkdirTemp("", "repairs-perm-test-*")
	require.NoError(t, err)
	config.AppConfig = config.Config{
		DatabaseType: "pebble",
		DatabasePath: filepath.Join(tempDir, "test.pebble"),
		RootDir:      tempDir,
		EnableAuth:   enableAuth,
	}
	store, err := database.NewPebbleStoreInMemory(config.AppConfig.DatabasePath)
	require.NoError(t, err)
	database.SetGlobalStore(store)
	require.NoError(t, database.RunMigrations(store))
	if withUsers {
		_, _, err = auth.SeedRoles(store)
		require.NoError(t, err)
		editor, err := store.CreateUser("editor", "editor@x.test", "bcrypt", "hash", []string{auth.SeedRoleEditor}, "active")
		require.NoError(t, err)
		admin, err := store.CreateUser("admin", "admin@x.test", "bcrypt", "hash", []string{auth.SeedRoleAdmin}, "active")
		require.NoError(t, err)
		es, err := store.CreateSession(editor.ID, "127.0.0.1", "test", time.Hour)
		require.NoError(t, err)
		as, err := store.CreateSession(admin.ID, "127.0.0.1", "test", time.Hour)
		require.NoError(t, err)
		editorToken, adminToken = es.ID, as.ID
	}
	srv = newTestServer(t, store)
	t.Cleanup(func() {
		if srv.opRegistry != nil {
			_ = srv.opRegistry.Shutdown(context.Background())
		}
		if srv.fileIOPool != nil {
			srv.fileIOPool.Stop()
		}
		database.SetGlobalStore(nil)
		store.Close()
		_ = os.RemoveAll(tempDir)
		config.AppConfig = origCfg
	})
	return srv, editorToken, adminToken
}

func postPlan(srv *Server, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, missingFilesPlanPath, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	return w
}

// TestRepairsRoutes_MissingFilesNeedsSettingsManage: with auth on, an editor
// (library.edit_metadata only) gets 403 naming settings.manage and an admin
// gets 202; with auth disabled, and on a first run with no users yet, the
// plan starts with no credentials at all, as every s.perm route does.
// Not parallel: it swaps the process-wide config.
func TestRepairsRoutes_MissingFilesNeedsSettingsManage(t *testing.T) {
	t.Run("auth on, editor and admin", func(t *testing.T) {
		srv, editor, admin := repairsPermServer(t, true, true)
		w := postPlan(srv, editor)
		require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
		require.Contains(t, w.Body.String(), "permission denied: settings.manage")
		w = postPlan(srv, admin)
		require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	})
	t.Run("auth disabled", func(t *testing.T) {
		srv, _, _ := repairsPermServer(t, false, false)
		w := postPlan(srv, "")
		require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	})
	t.Run("first run, no users", func(t *testing.T) {
		srv, _, _ := repairsPermServer(t, true, false)
		w := postPlan(srv, "")
		require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	})
}
