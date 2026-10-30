// file: internal/server/login_throttle_key_test.go
// version: 1.1.1
// guid: 5a1f9c7e-62b8-4d03-9e4a-b7c2d8e1f063
// last-edited: 2026-10-29

package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 192.0.2.0/24 is TEST-NET-1 (RFC 5737). The connector is a configured tunnel
// connector; the visitors are what Cloudflare would put in CF-Connecting-IP.
const (
	throttleTestConnector = "192.0.2.45"
	throttleTestOther     = "192.0.2.99"
)

func throttleKeyFor(t *testing.T, remote string, hdr map[string]string) string {
	t.Helper()
	gin.SetMode(gin.TestMode)
	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	req.RemoteAddr = remote
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = req
	return loginThrottleKey(c)
}

func TestLoginThrottleKey(t *testing.T) {
	setTunnelConnectors(t, throttleTestConnector)
	tests := []struct {
		name   string
		remote string
		hdr    map[string]string
		want   string
	}{
		{"a visitor through a configured connector is keyed on its own address", throttleTestConnector + ":4000", map[string]string{"CF-Connecting-IP": "198.51.100.7"}, "198.51.100.7"},
		{"two visitors through one connector get two keys", throttleTestConnector + ":4001", map[string]string{"CF-Connecting-IP": "198.51.100.8"}, "198.51.100.8"},
		{"a connector request with no CF-Connecting-IP falls back to the connector", throttleTestConnector + ":4000", nil, throttleTestConnector},
		{"an unparseable CF-Connecting-IP is ignored", throttleTestConnector + ":4000", map[string]string{"CF-Connecting-IP": "not-an-ip"}, throttleTestConnector},
		{"a peer that is not a connector keeps its own address whatever it claims", throttleTestOther + ":4000", map[string]string{"CF-Connecting-IP": "198.51.100.7", "X-Forwarded-For": "198.51.100.7"}, throttleTestOther},
		{"a public IPv6 visitor is one key per /64", throttleTestConnector + ":4000", map[string]string{"CF-Connecting-IP": "2001:db8:1:2:aaaa:bbbb:cccc:dddd"}, "2001:db8:1:2::/64"},
		{"another address in that /64 shares the key", throttleTestConnector + ":4000", map[string]string{"CF-Connecting-IP": "2001:db8:1:2::1"}, "2001:db8:1:2::/64"},
		{"a zoned link-local peer is keyed, not empty like c.ClientIP()", "[fe80::1%en0]:4000", nil, "fe80::1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, throttleKeyFor(t, tt.remote, tt.hdr))
		})
	}
}

// One login asks several times; the answer is worked out once per request and
// cannot change under it.
func TestLoginThrottleKey_IsComputedOncePerRequest(t *testing.T) {
	setTunnelConnectors(t, throttleTestConnector)
	gin.SetMode(gin.TestMode)
	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	req.RemoteAddr = throttleTestConnector + ":4000"
	req.Header.Set("CF-Connecting-IP", "198.51.100.7")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = req
	require.Equal(t, "198.51.100.7", loginThrottleKey(c))
	req.Header.Set("CF-Connecting-IP", "198.51.100.8")
	require.Equal(t, "198.51.100.7", loginThrottleKey(c))
}

// webLoginRequest posts a failing login through the real AuthHandler.Login.
func webLoginRequest(t *testing.T, h *handlers.AuthHandler, remote string, hdr map[string]string) int {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	body, _ := json.Marshal(map[string]string{"username": "nobody", "password": "wrong"})
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.RemoteAddr = remote
	for k, v := range hdr {
		c.Request.Header.Set(k, v)
	}
	h.Login(c)
	return w.Code
}

func newWebLoginHandler(t *testing.T, withKey bool) *handlers.AuthHandler {
	t.Helper()
	store, err := database.NewPebbleStoreInMemory(t.TempDir() + "/auth.pebble")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	h := handlers.NewAuthHandler(store, true)
	// Every handler here fails one account (nobody) past the soft threshold, and
	// its progressive delay (200 ms a failure past the 5th, ~11 s over the 15 of
	// a budget) is real time.Sleep: the delay is not under test, the buckets are.
	h.SetFailureDelay(func(time.Duration) {})
	if withKey {
		h.SetThrottleKeyFunc(loginThrottleKey)
	}
	return h
}

// maxWebLoginFailures mirrors handlers.maxFailedLoginsPerIP (unexported).
const maxWebLoginFailures = 15

// The control: with no key function the web login throttle keys on the peer,
// and the peer behind the tunnel is the connector, one bucket for every
// visitor. One visitor failing 15 times locks out all the rest.
func TestWebLoginThrottle_WithoutKeyFuncVisitorsShareTheConnectorBucket(t *testing.T) {
	setTunnelConnectors(t, throttleTestConnector)
	h := newWebLoginHandler(t, false)
	remote := throttleTestConnector + ":4000"
	for i := range maxWebLoginFailures {
		require.Equal(t, http.StatusUnauthorized, webLoginRequest(t, h, remote, map[string]string{"CF-Connecting-IP": "198.51.100.7"}), "attempt %d", i)
	}
	require.Equal(t, http.StatusTooManyRequests, webLoginRequest(t, h, remote, map[string]string{"CF-Connecting-IP": "198.51.100.8"}),
		"a second visitor is locked out by the first (the weakness)")
}

// Peer is a configured connector: each visitor has its own budget.
func TestWebLoginThrottle_ConnectorVisitorsHaveIndependentBudgets(t *testing.T) {
	setTunnelConnectors(t, throttleTestConnector)
	h := newWebLoginHandler(t, true)
	remote := throttleTestConnector + ":4000"
	abuser := map[string]string{"CF-Connecting-IP": "198.51.100.7"}
	for i := range maxWebLoginFailures {
		require.Equal(t, http.StatusUnauthorized, webLoginRequest(t, h, remote, abuser), "attempt %d", i)
	}
	require.Equal(t, http.StatusTooManyRequests, webLoginRequest(t, h, remote, abuser), "the abuser past its own budget")
	require.Equal(t, http.StatusUnauthorized, webLoginRequest(t, h, remote, map[string]string{"CF-Connecting-IP": "198.51.100.8"}),
		"another visitor through the same connector is not locked out")
	// No CF-Connecting-IP: the connector's own shared bucket, as before, and
	// separate from both visitors.
	require.Equal(t, http.StatusUnauthorized, webLoginRequest(t, h, remote, nil))
}

// Peer is not a connector: the header is a caller's claim, so rotating it never
// buys a fresh bucket.
func TestWebLoginThrottle_SpoofedHeaderFromAnUnlistedPeerCannotPickABucket(t *testing.T) {
	setTunnelConnectors(t, throttleTestConnector)
	h := newWebLoginHandler(t, true)
	remote := throttleTestOther + ":4000"
	for i := range maxWebLoginFailures {
		hdr := map[string]string{"CF-Connecting-IP": fmt.Sprintf("198.51.100.%d", i+1), "X-Forwarded-For": fmt.Sprintf("203.0.113.%d", i+1)}
		require.Equal(t, http.StatusUnauthorized, webLoginRequest(t, h, remote, hdr), "attempt %d", i)
	}
	require.Equal(t, http.StatusTooManyRequests, webLoginRequest(t, h, remote, map[string]string{"CF-Connecting-IP": "198.51.100.200"}),
		"a fresh spoofed address from the same peer must still land in the spent bucket")
	// A different peer is a different bucket.
	require.Equal(t, http.StatusUnauthorized, webLoginRequest(t, h, "192.0.2.100:4000", nil))
}

// A public IPv6 visitor is bucketed by /64: rotating addresses inside it does
// not reset the budget.
func TestWebLoginThrottle_IPv6VisitorRotationWithinASlash64IsOneBucket(t *testing.T) {
	setTunnelConnectors(t, throttleTestConnector)
	h := newWebLoginHandler(t, true)
	remote := throttleTestConnector + ":4000"
	for i := range maxWebLoginFailures {
		hdr := map[string]string{"CF-Connecting-IP": fmt.Sprintf("2001:db8:1:2::%x", i+1)}
		require.Equal(t, http.StatusUnauthorized, webLoginRequest(t, h, remote, hdr), "attempt %d", i)
	}
	require.Equal(t, http.StatusTooManyRequests, webLoginRequest(t, h, remote, map[string]string{"CF-Connecting-IP": "2001:db8:1:2::ffff"}))
	require.Equal(t, http.StatusUnauthorized, webLoginRequest(t, h, remote, map[string]string{"CF-Connecting-IP": "2001:db8:1:3::1"}), "another /64 has its own budget")
}

// Wiring pin: the handler tests above inject the key function by hand, so they
// cannot notice if wire_handlers.go stops doing it. This one drives the real
// router built by NewServer.
func TestWebLoginThrottle_RealRouterKeysVisitorsBehindAConnector(t *testing.T) {
	fx := setupCredGuardServerWith(t, func(c *config.Config) { c.TunnelConnectorAddrs = throttleTestConnector })
	fx.srv.loginThrottle().SetSleep(func(time.Duration) {}) // the failure delay is not under test
	post := func(visitor string) int {
		body, _ := json.Marshal(map[string]string{"username": "nobody", "password": "wrong"})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = throttleTestConnector + ":4000"
		req.Header.Set("CF-Connecting-IP", visitor)
		w := httptest.NewRecorder()
		fx.srv.router.ServeHTTP(w, req)
		return w.Code
	}
	for i := range maxWebLoginFailures {
		require.Equal(t, http.StatusUnauthorized, post("198.51.100.7"), "attempt %d", i)
	}
	require.Equal(t, http.StatusTooManyRequests, post("198.51.100.7"))
	require.Equal(t, http.StatusUnauthorized, post("198.51.100.8"),
		"the real router must key the login throttle on the visitor, not the connector")
}

// Same pin for the Audiobookshelf-compatible /login, which has its own throttle
// and is wired separately (wire_abs_routes.go).
func TestABSLoginThrottle_RealRouterKeysVisitorsBehindAConnector(t *testing.T) {
	fx := setupCredGuardServerWith(t, func(c *config.Config) {
		c.TunnelConnectorAddrs = throttleTestConnector
		c.ABSAPIEnabled = true
		c.ABSJWTSecret = "synthetic-throttle-test-secret-0123456789abcdef"
	})
	fx.srv.loginThrottle().SetSleep(func(time.Duration) {}) // the failure delay is not under test
	post := func(visitor string) int {
		body, _ := json.Marshal(map[string]string{"username": "nobody", "password": "wrong"})
		req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = throttleTestConnector + ":4000"
		req.Header.Set("CF-Connecting-IP", visitor)
		w := httptest.NewRecorder()
		fx.srv.router.ServeHTTP(w, req)
		return w.Code
	}
	first := post("198.51.100.7")
	require.Equal(t, http.StatusUnauthorized, first, "the ABS login route must be reachable and reject a bad password")
	for i := 1; i < maxWebLoginFailures; i++ {
		require.Equal(t, http.StatusUnauthorized, post("198.51.100.7"), "attempt %d", i)
	}
	require.Equal(t, http.StatusTooManyRequests, post("198.51.100.7"))
	require.Equal(t, http.StatusUnauthorized, post("198.51.100.8"),
		"the ABS login throttle must key on the visitor, not the connector")
}
