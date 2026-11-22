// file: internal/server/identity_seam_test.go
// version: 0.1.0
// guid: 9a2e4c61-5b08-4d37-8f1c-7e3a6d0b2c94
// last-edited: 2026-11-21

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	coreauth "github.com/falkcorp/mediacore/auth"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/server/legacyauth"
)

// TestIdentitySeam_ModeComesFromHowTheServerWasBuilt: a server built without
// WithIdentity (books today, LegacyAuthGin) never reports core identity, even
// for a request that carries a core principal and checker; a server built with
// it reports it for every request, principal or not. Every adopted site reads
// this one value, so this is the pin of "dark while LegacyAuthGin".
func TestIdentitySeam_ModeComesFromHowTheServerWasBuilt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	bruce := &database.User{ID: "u-bruce", Username: "bruce", Status: "active"}
	p, c := adoptPrincipal(coreauth.MethodSession, bruce, "test:library.view")

	probe := func(s *Server) func(*http.Request, *coreauth.Principal) (bridged bool, code int) {
		var seen bool
		s.router.GET("/__identity_probe", func(g *gin.Context) {
			_, seen = legacyauth.BridgeFrom(g.Request.Context())
			g.Status(http.StatusNoContent)
		})
		return func(req *http.Request, p *coreauth.Principal) (bool, int) {
			seen = false
			var w *httptest.ResponseRecorder
			if p == nil {
				w = identityServe(s.router, req, nil, nil)
			} else {
				w = identityServe(s.router, req, p, c)
			}
			return seen, w.Code
		}
	}
	newStore := func() database.Store {
		store, err := database.NewPebbleStoreInMemory(t.TempDir() + "/seam.pebble")
		require.NoError(t, err)
		t.Cleanup(func() { store.Close() })
		return store
	}

	t.Run("without the option HEAD decides, with or without a core principal", func(t *testing.T) {
		do := probe(newTestServer(t, newStore()))
		for name, pr := range map[string]*coreauth.Principal{"no principal": nil, "a core principal": p} {
			bridged, code := do(httptest.NewRequest(http.MethodGet, "/__identity_probe", nil), pr)
			require.Equal(t, http.StatusNoContent, code, name)
			require.False(t, bridged, "%s: a server built without WithIdentity must run HEAD's path", name)
		}
	})

	t.Run("with the option every request runs under core identity, principal or not", func(t *testing.T) {
		id := adoptIdentity(t, []string{"library.view"}, nil, nil, bruce)
		do := probe(newTestServerWith(t, newStore(), WithIdentity(id)))
		for name, pr := range map[string]*coreauth.Principal{"no principal": nil, "a core principal": p} {
			bridged, code := do(httptest.NewRequest(http.MethodGet, "/__identity_probe", nil), pr)
			require.Equal(t, http.StatusNoContent, code, name)
			require.True(t, bridged, "%s: a bridged server must never hand a request to HEAD's path", name)
		}
	})
}

// TestIdentity_ValidateRefusesAPartialWiring: NewServer exits on a partial
// Identity (a server half under core and half under HEAD), so Validate is what
// holds each member.
func TestIdentity_ValidateRefusesAPartialWiring(t *testing.T) {
	full := adoptIdentity(t, []string{"library.view"}, nil, nil)
	require.NoError(t, full.Validate())
	for name, id := range map[string]*legacyauth.Identity{
		"nil":        nil,
		"no bridge":  {Compat: full.Compat, Client: full.Client, Refresh: full.Refresh, Access: full.Access},
		"no compat":  {Bridge: full.Bridge, Client: full.Client, Refresh: full.Refresh, Access: full.Access},
		"no client":  {Bridge: full.Bridge, Compat: full.Compat, Refresh: full.Refresh, Access: full.Access},
		"no refresh": {Bridge: full.Bridge, Compat: full.Compat, Client: full.Client, Access: full.Access},
		"no access":  {Bridge: full.Bridge, Compat: full.Compat, Client: full.Client, Refresh: full.Refresh},
	} {
		require.Error(t, id.Validate(), name)
	}
}
