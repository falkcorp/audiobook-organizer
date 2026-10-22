// file: internal/server/login_ceiling_test.go
// version: 1.0.0
// guid: 47178e5a-1329-49cf-847d-f019456d21c6
// last-edited: 2026-10-21

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
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// ceilingRouter is a router whose one route answers with status code, behind
// the ceiling middleware.
func ceilingRouter(l *loginCeiling, code *int) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/login", l.middleware(nil), func(c *gin.Context) { c.Status(*code) })
	return r
}

func ceilingPost(r *gin.Engine, remote string) int {
	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	req.RemoteAddr = remote
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code
}

func TestLoginCeiling_ConnectorFailuresAreCappedAndOthersAreNot(t *testing.T) {
	setTunnelConnectors(t, throttleTestConnector+",192.0.2.46")
	l := newLoginCeiling()
	code := http.StatusUnauthorized
	r := ceilingRouter(l, &code)
	for i := 0; i < loginConnectorCeiling; i++ {
		require.Equal(t, http.StatusUnauthorized, ceilingPost(r, throttleTestConnector+":4000"), "failure %d", i)
	}
	require.Equal(t, http.StatusTooManyRequests, ceilingPost(r, throttleTestConnector+":4000"), "the connector's budget is spent")
	require.Equal(t, http.StatusUnauthorized, ceilingPost(r, "192.0.2.46:4000"), "another connector is untouched")
}

func TestLoginCeiling_GlobalCeilingCoversEveryPeer(t *testing.T) {
	setTunnelConnectors(t, "")
	l := newLoginCeiling()
	code := http.StatusUnauthorized
	r := ceilingRouter(l, &code)
	for i := 0; i < loginGlobalCeiling; i++ {
		require.Equal(t, http.StatusUnauthorized, ceilingPost(r, fmt.Sprintf("192.0.2.%d:4000", i%200+1)), "failure %d", i)
	}
	require.Equal(t, http.StatusTooManyRequests, ceilingPost(r, "198.51.100.9:4000"), "a fresh address cannot get past the global ceiling")
}

// Only failures count: a successful login (anything but 401) hands its slot
// back, so a busy site's successes never spend the ceiling.
func TestLoginCeiling_SuccessesAndRefusalsCostNothing(t *testing.T) {
	setTunnelConnectors(t, "")
	l := newLoginCeiling()
	code := http.StatusOK
	r := ceilingRouter(l, &code)
	for i := 0; i < 3*loginGlobalCeiling; i++ {
		require.Equal(t, http.StatusOK, ceilingPost(r, "192.0.2.5:4000"))
	}
	code = http.StatusTooManyRequests // the per-visitor throttle refusing: no password work
	for i := 0; i < 3*loginGlobalCeiling; i++ {
		require.Equal(t, http.StatusTooManyRequests, ceilingPost(r, "192.0.2.5:4000"))
	}
	code = http.StatusUnauthorized
	require.Equal(t, http.StatusUnauthorized, ceilingPost(r, "192.0.2.5:4000"))
}

// The window rolls over, and a slot is not handed back into a later window.
func TestLoginCeiling_WindowRollsOver(t *testing.T) {
	setTunnelConnectors(t, "")
	l := newLoginCeiling()
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	code := http.StatusUnauthorized
	r := ceilingRouter(l, &code)
	for i := 0; i < loginGlobalCeiling; i++ {
		ceilingPost(r, "192.0.2.5:4000")
	}
	require.Equal(t, http.StatusTooManyRequests, ceilingPost(r, "192.0.2.5:4000"))
	now = now.Add(loginCeilingWindow + time.Second)
	require.Equal(t, http.StatusUnauthorized, ceilingPost(r, "192.0.2.5:4000"))
	// A ticket from the old window must not decrement the new one.
	tk, ok, _ := l.reserve("")
	require.True(t, ok)
	now = now.Add(loginCeilingWindow + time.Second)
	_, ok, _ = l.reserve("")
	require.True(t, ok)
	l.release(tk)
	l.mu.Lock()
	got := l.global.count
	l.mu.Unlock()
	require.Equal(t, 1, got)
}

// Parallel guesses cannot all pass a check made before any has failed: slots
// are taken on entry.
func TestLoginCeiling_InFlightAttemptsCountAgainstTheCeiling(t *testing.T) {
	setTunnelConnectors(t, "")
	l := newLoginCeiling()
	admitted := 0
	for i := 0; i < loginGlobalCeiling+50; i++ {
		if _, ok, _ := l.reserve(""); ok {
			admitted++
		}
	}
	require.Equal(t, loginGlobalCeiling, admitted)
}

// Wiring pin on the real router: the ceiling sits in front of BOTH password
// logins and is shared by them, and /auth/refresh is outside it.
func TestLoginCeiling_RealRouterGuardsBothLoginsWithOneGlobalBucket(t *testing.T) {
	fx := setupCredGuardServerWith(t, func(c *config.Config) {
		c.ABSAPIEnabled = true
		c.ABSJWTSecret = "synthetic-throttle-test-secret-0123456789abcdef"
	})
	post := func(path, remote string) int {
		body, _ := json.Marshal(map[string]string{"username": "nobody", "password": "wrong"})
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = remote
		w := httptest.NewRecorder()
		fx.srv.router.ServeHTTP(w, req)
		return w.Code
	}
	// Different addresses so no per-visitor budget is the thing that trips.
	for i := 0; i < loginGlobalCeiling; i++ {
		path := "/api/v1/auth/login"
		if i%2 == 1 {
			path = "/login"
		}
		require.Equal(t, http.StatusUnauthorized, post(path, fmt.Sprintf("192.0.2.%d:%d", i%200+1, 4000+i)), "failure %d on %s", i, path)
	}
	require.Equal(t, http.StatusTooManyRequests, post("/api/v1/auth/login", "198.51.100.1:4000"), "web login past the shared global ceiling")
	require.Equal(t, http.StatusTooManyRequests, post("/login", "198.51.100.2:4000"), "ABS login past the shared global ceiling")
	require.NotEqual(t, http.StatusTooManyRequests, post("/auth/refresh", "198.51.100.3:4000"), "refresh is outside the ceiling")
}
