// file: internal/server/identity_session_stands_test.go
// version: 0.1.0
// guid: 22a60a7e-07cd-45b6-be66-143b43d5e2b5
// last-edited: 2026-11-21

package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	coreauth "github.com/falkcorp/mediacore/auth"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// F4-08 B10b2: an open event stream's "does the credential still stand" check
// asks core under identity (Identity.Refresh on the bound principal), not
// books' store, which is no longer the account record there.
func TestSessionStillStands_IdentityAsksCore(t *testing.T) {
	gin.SetMode(gin.TestMode)
	orig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = orig })
	config.AppConfig.EnableAuth = true

	// Books' store holds no accounts at all: HEAD's reads would call a request
	// with no user (a first run) standing, and a request with a user gone.
	store, err := database.NewPebbleStoreInMemory(t.TempDir() + "/stands.pebble")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	alfred := &database.User{ID: "u-alfred", Username: "alfred", Roles: []string{auth.SeedRoleAdmin}, Status: "active"}
	id := adoptIdentity(t, nil, nil, nil, alfred)
	var asked []*coreauth.Principal
	refusal := error(nil)
	id.Refresh = func(_ context.Context, p *coreauth.Principal) (*coreauth.Principal, error) {
		asked = append(asked, p)
		if refusal != nil {
			return nil, refusal
		}
		return p, nil
	}
	s := newTestServerWith(t, store, WithIdentity(id))
	stands := s.sessionStillStands()

	ask := func(p *coreauth.Principal) bool {
		var got bool
		r := gin.New()
		r.GET("/stands", func(c *gin.Context) { got = stands(c); c.Status(http.StatusNoContent) })
		req := httptest.NewRequest(http.MethodGet, "/stands", nil)
		if p != nil {
			req = req.WithContext(coreauth.WithPrincipal(req.Context(), p))
		}
		require.Equal(t, http.StatusNoContent, serve(r, req))
		return got
	}
	p, _ := adoptPrincipal(coreauth.MethodSession, alfred, "core:admin")

	require.True(t, ask(p), "core still stands behind the principal, whatever books' store holds")
	require.Equal(t, []*coreauth.Principal{p}, asked, "the principal stage 12 bound is what core is asked about")

	for name, err := range map[string]error{
		"user disabled or removed": coreauth.ErrRevoked,
		"session or token expired": coreauth.ErrExpired,
		"credential epoch moved":   coreauth.ErrStaleEpoch,
		"core's store failed":      errors.New("store down"),
	} {
		refusal = err
		require.False(t, ask(p), name)
	}
	refusal = nil

	require.False(t, ask(nil), "no principal is not a first run: it fails closed, where HEAD's no-accounts bypass would stand")

	// Auth disabled in books' config does not make a stream stand: under
	// identity the auth-disabled principal is core's to answer for.
	config.AppConfig.EnableAuth = false
	refusal = coreauth.ErrRevoked
	require.False(t, ask(p), "books' EnableAuth is not read under identity")
}

func serve(h http.Handler, req *http.Request) int {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Code
}
