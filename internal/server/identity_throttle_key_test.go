// file: internal/server/identity_throttle_key_test.go
// version: 0.1.0
// guid: 8d4f2a60-3c17-4e95-b0a8-6f1e9c24d713
// last-edited: 2026-11-21

package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	coreauth "github.com/falkcorp/mediacore/auth"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// F4-08 B10c, item 7: the ABS failure throttle's bucket under core identity is
// the visitor cfaccess.ClientAddrTrust.ClientBucket names, wired through
// abs.Options.IdentityThrottleKey. These tests drive the real wiring
// (wireABSRoutes over NewServer), so a wiring that leaves the key unset, which
// falls back to the raw peer, fails here.

const accessToken = "synthetic-access-assertion"

// identityABSServer is a server with the ABS surface on, built under core
// identity with opts.
func identityABSServer(t *testing.T, opts ...ServerOption) *Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	setTunnelConnectors(t, "")
	orig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = orig })
	config.AppConfig = config.Config{
		DatabaseType:  "pebble",
		RootDir:       t.TempDir(),
		EnableAuth:    true,
		ABSAPIEnabled: true,
		ABSJWTSecret:  strings.Repeat("batcave-", 6),
	}
	store, err := database.NewPebbleStoreInMemory(t.TempDir() + "/throttle-key.pebble")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	id := adoptIdentity(t, []string{"library.view"}, nil, nil)
	s := newTestServerWith(t, store, append([]ServerOption{WithIdentity(id)}, opts...)...)
	s.loginThrottle().SetSleep(func(time.Duration) {})
	return s
}

// deadRefresh is a refresh with no token, a genuinely dead one the surface charges
// to its own throttle, from a request whose TCP peer is this host (a cloudflared
// on the same machine) and which names visitor in CF-Connecting-IP under an
// Access assertion.
func deadRefresh(s *Server, visitor string) int {
	req := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
	req.RemoteAddr = "127.0.0.1:4000"
	req.Header.Set("Cf-Access-Jwt-Assertion", accessToken)
	req.Header.Set("CF-Connecting-IP", visitor)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	return w.Code
}

func spendRefreshBudget(t *testing.T, s *Server, visitor string) {
	t.Helper()
	for i := range coreauth.DefaultMaxFailuresPerSource {
		require.Equal(t, http.StatusUnauthorized, deadRefresh(s, visitor), "failure %d of %d is a 401, not yet throttled", i+1, coreauth.DefaultMaxFailuresPerSource)
	}
}

// With the Access verifier the bucket is the visitor the assertion vouches for:
// one visitor spending its budget does not lock out another behind the same
// local connector. An unset IdentityThrottleKey falls back to the raw peer, one
// bucket for both, and fails the second assertion.
func TestIdentityThrottleKey_AVerifiedAssertionNamesTheVisitorBucket(t *testing.T) {
	s := identityABSServer(t, WithAccessVerifier(fakeAccessVerifier{accessToken: {Email: "owner@example.test"}}))
	spendRefreshBudget(t, s, "192.0.2.10")
	require.Equal(t, http.StatusTooManyRequests, deadRefresh(s, "192.0.2.10"), "the visitor past its budget")
	require.Equal(t, http.StatusUnauthorized, deadRefresh(s, "192.0.2.11"),
		"another visitor behind the same connector keeps its own budget (a wiring that leaves IdentityThrottleKey unset keys both on the peer)")
}

// No verifier, no belief: the header is a caller's claim and never picks the
// bucket, so both visitors share the peer's, and the throttle is never skipped.
func TestIdentityThrottleKey_WithoutAVerifierTheHeaderPicksNothing(t *testing.T) {
	s := identityABSServer(t)
	spendRefreshBudget(t, s, "192.0.2.10")
	require.Equal(t, http.StatusTooManyRequests, deadRefresh(s, "192.0.2.11"),
		"a visitor named only by an unverified header shares the peer's spent budget")
}

// A server built without an identity has no identity key: HEAD's gin copy decides.
func TestIdentityThrottleKey_NoneWithoutAnIdentity(t *testing.T) {
	require.Nil(t, (&Server{}).identityThrottleKey())
	require.NotNil(t, (&Server{identity: adoptIdentity(t, []string{"library.view"}, nil, nil)}).identityThrottleKey())
}
