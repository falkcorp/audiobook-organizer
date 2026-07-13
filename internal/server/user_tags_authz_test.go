// file: internal/server/user_tags_authz_test.go
// version: 1.0.0
// guid: 7a4e9b3c-2f1d-4a6e-9c8b-5d3f0e1a2b4c
// last-edited: 2026-07-13

// Regression coverage for the book user-tags write routes' authorization
// guard (fixed in this change). setupUserTagRoutes previously registered
// PUT/POST/DELETE .../user-tags with no s.perm(...) check, so any
// authenticated principal — including a view-only role — could mutate the
// global per-book tag set. These tests build a server with auth enabled and
// seeded admin/viewer roles, then assert the viewer session (library.view
// only) gets 403 on all three write routes while the admin session
// (library.edit_metadata included) succeeds.

package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// setupUserTagsAuthzTestServer creates a test server with authentication
// enabled and seeded admin + viewer sessions. setupTestServer (used by most
// of this package's tests) always runs with auth disabled, which would make
// a 403 assertion meaningless here.
func setupUserTagsAuthzTestServer(t *testing.T) (srv *Server, adminToken, viewerToken string, bookID string, cleanup func()) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	origCfg := config.AppConfig
	tempDir, err := os.MkdirTemp("", "audiobook-authz-test-*")
	require.NoError(t, err)

	config.AppConfig = config.Config{
		DatabaseType: "pebble",
		DatabasePath: filepath.Join(tempDir, "test.pebble"),
		RootDir:      tempDir,
		EnableAuth:   true,
	}

	store, err := database.NewPebbleStore(config.AppConfig.DatabasePath)
	require.NoError(t, err)
	database.SetGlobalStore(store)

	err = database.RunMigrations(store)
	require.NoError(t, err)

	_, _, err = auth.SeedRoles(store)
	require.NoError(t, err)

	admin, err := store.CreateUser("admin", "admin@x.test", "bcrypt", "hash", []string{auth.SeedRoleAdmin}, "active")
	require.NoError(t, err)
	viewer, err := store.CreateUser("viewer", "viewer@x.test", "bcrypt", "hash", []string{auth.SeedRoleViewer}, "active")
	require.NoError(t, err)

	adminSession, err := store.CreateSession(admin.ID, "127.0.0.1", "test", time.Hour)
	require.NoError(t, err)
	viewerSession, err := store.CreateSession(viewer.ID, "127.0.0.1", "test", time.Hour)
	require.NoError(t, err)

	book, err := store.CreateBook(&database.Book{
		Title:    "Authz Test Book",
		FilePath: "/tmp/authz-test.m4b",
	})
	require.NoError(t, err)

	server := NewServer(store)
	if server.opRegistry != nil {
		server.opRegistry.Start(context.Background())
	}

	cleanup = func() {
		if server.opRegistry != nil {
			_ = server.opRegistry.Shutdown(context.Background())
		}
		if server.fileIOPool != nil {
			server.fileIOPool.Stop()
		}
		if server.writeBackBatcher != nil {
			_ = server.writeBackBatcher.Stop(context.Background())
		}
		database.SetGlobalStore(nil)
		store.Close()
		_ = os.RemoveAll(tempDir)
		config.AppConfig = origCfg
	}

	return server, adminSession.ID, viewerSession.ID, book.ID, cleanup
}

func TestUserTagRoutes_ViewerForbidden(t *testing.T) {
	server, _, viewerToken, bookID, cleanup := setupUserTagsAuthzTestServer(t)
	defer cleanup()

	cases := []struct {
		name   string
		method string
		path   string
		body   []byte
	}{
		{"PUT set tags", http.MethodPut, "/api/v1/audiobooks/" + bookID + "/user-tags", []byte(`{"tags":["scifi"]}`)},
		{"POST add tag", http.MethodPost, "/api/v1/audiobooks/" + bookID + "/user-tags", []byte(`{"tag":"scifi"}`)},
		{"DELETE remove tag", http.MethodDelete, "/api/v1/audiobooks/" + bookID + "/user-tags/scifi", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, bytes.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+viewerToken)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			server.router.ServeHTTP(w, req)
			require.Equal(t, http.StatusForbidden, w.Code, "viewer (library.view only) should be forbidden; body=%s", w.Body.String())
		})
	}
}

func TestUserTagRoutes_AdminAllowed(t *testing.T) {
	server, adminToken, _, bookID, cleanup := setupUserTagsAuthzTestServer(t)
	defer cleanup()

	cases := []struct {
		name   string
		method string
		path   string
		body   []byte
	}{
		{"PUT set tags", http.MethodPut, "/api/v1/audiobooks/" + bookID + "/user-tags", []byte(`{"tags":["scifi"]}`)},
		{"POST add tag", http.MethodPost, "/api/v1/audiobooks/" + bookID + "/user-tags", []byte(`{"tag":"fantasy"}`)},
		{"DELETE remove tag", http.MethodDelete, "/api/v1/audiobooks/" + bookID + "/user-tags/fantasy", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, bytes.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+adminToken)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			server.router.ServeHTTP(w, req)
			require.NotEqual(t, http.StatusForbidden, w.Code, "admin (library.edit_metadata) should not be forbidden; body=%s", w.Body.String())
			require.NotEqual(t, http.StatusUnauthorized, w.Code, "admin session should authenticate; body=%s", w.Body.String())
			require.Equal(t, http.StatusOK, w.Code, "admin write should succeed; body=%s", w.Body.String())
		})
	}
}
