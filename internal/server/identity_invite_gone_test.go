// file: internal/server/identity_invite_gone_test.go
// version: 0.1.0
// guid: 8a4e6c13-2f95-4d70-b3e1-6c0d9a7f5b28
// last-edited: 2026-11-22

package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// F4-08 B3: under core identity an invite is core's record, so books' two legacy
// public invite routes would read and write a store nothing fills any more. They
// answer 410 and name core's routes; a server built without identity keeps
// HEAD's routes.

func inviteRouteServer(t *testing.T, opts ...ServerOption) *Server {
	t.Helper()
	// AppConfig is process-global: put it back, or every later test in the package
	// runs with auth on (the full-package run saw 401 where tests expected 200).
	prev := config.AppConfig
	t.Cleanup(func() { config.AppConfig = prev })
	config.AppConfig = config.Config{DatabaseType: "pebble", RootDir: t.TempDir(), EnableAuth: true}
	store, err := database.NewPebbleStoreInMemory(t.TempDir() + "/invite-gone.pebble")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	return newTestServerWith(t, store, opts...)
}

func serveInviteRoute(s *Server, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	return w
}

func TestIdentityInviteRoutes_LegacyPublicRoutesAnswer410(t *testing.T) {
	s := inviteRouteServer(t, WithIdentity(adoptIdentity(t, []string{"library.view"}, nil, nil)))
	for _, rt := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/auth/accept-invite", `{"token":"anything","password":"correct horse battery staple"}`},
		{http.MethodGet, "/api/v1/auth/invite/anything", ""},
	} {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			w := serveInviteRoute(s, rt.method, rt.path, rt.body)
			require.Equal(t, http.StatusGone, w.Code, w.Body.String())
			require.Contains(t, w.Body.String(), `"error":"gone"`)
			require.Contains(t, w.Body.String(), "core's invitation routes")
			// The retired route reads no token and echoes none.
			require.NotContains(t, w.Body.String(), "anything")
		})
	}
}

// Without identity the routes are HEAD's: a bogus token is not a 410.
func TestIdentityInviteRoutes_UnchangedWithoutIdentity(t *testing.T) {
	s := inviteRouteServer(t)
	for _, rt := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/auth/accept-invite", `{"token":"anything","password":"correct horse battery staple"}`},
		{http.MethodGet, "/api/v1/auth/invite/anything", ""},
	} {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			w := serveInviteRoute(s, rt.method, rt.path, rt.body)
			require.NotEqual(t, http.StatusGone, w.Code, w.Body.String())
			require.NotContains(t, w.Body.String(), "core's invitation routes")
		})
	}
}
