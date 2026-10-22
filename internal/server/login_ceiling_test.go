// file: internal/server/login_ceiling_test.go
// version: 2.0.0
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

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/oauth"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// ceilingRouter is a router whose one route answers with *code, behind the
// ceiling middleware, and reports whether the request reached it saturated.
func ceilingRouter(l *loginCeiling, code *int, sawSaturated *bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/login", l.middleware(nil), func(c *gin.Context) {
		if sawSaturated != nil {
			*sawSaturated = absauth.LoginSaturated(c)
		}
		c.Status(*code)
	})
	return r
}

func ceilingPost(r *gin.Engine, remote string, hdr map[string]string) int {
	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	req.RemoteAddr = remote
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code
}

// A spent ceiling does not refuse: the request reaches the handler marked
// saturated, so the handler can delay it by what its account has already failed.
func TestLoginCeiling_ConnectorCeilingMarksSaturatedAndOthersAreNot(t *testing.T) {
	setTunnelConnectors(t, throttleTestConnector+",192.0.2.46")
	l := newLoginCeiling()
	code, saturated := http.StatusUnauthorized, false
	r := ceilingRouter(l, &code, &saturated)
	for i := 0; i < loginConnectorCeiling; i++ {
		require.Equal(t, http.StatusUnauthorized, ceilingPost(r, throttleTestConnector+":4000", nil), "failure %d", i)
		require.False(t, saturated, "failure %d", i)
	}
	require.Equal(t, http.StatusUnauthorized, ceilingPost(r, throttleTestConnector+":4000", nil), "past the ceiling the request still reaches the handler")
	require.True(t, saturated, "...marked saturated")
	require.Equal(t, http.StatusUnauthorized, ceilingPost(r, "192.0.2.46:4000", nil))
	require.False(t, saturated, "another connector is untouched")
}

func TestLoginCeiling_GlobalCeilingCoversEveryPeer(t *testing.T) {
	setTunnelConnectors(t, "")
	l := newLoginCeiling()
	code, saturated := http.StatusUnauthorized, false
	r := ceilingRouter(l, &code, &saturated)
	for i := 0; i < loginGlobalCeiling; i++ {
		ceilingPost(r, fmt.Sprintf("192.0.2.%d:4000", i%200+1), nil)
		require.False(t, saturated, "failure %d", i)
	}
	ceilingPost(r, "198.51.100.9:4000", nil)
	require.True(t, saturated, "a fresh address cannot get past the global ceiling unmarked")
}

// Only failures count: a success (2xx) hands its slot back, and so does any
// other answer (the per-visitor refusal, a bad request, a server error).
func TestLoginCeiling_OnlyFailuresCount(t *testing.T) {
	setTunnelConnectors(t, "")
	l := newLoginCeiling()
	code, saturated := http.StatusOK, false
	r := ceilingRouter(l, &code, &saturated)
	for _, c := range []int{http.StatusOK, http.StatusTooManyRequests, http.StatusBadRequest, http.StatusInternalServerError, http.StatusForbidden} {
		code = c
		for i := 0; i < 2*loginGlobalCeiling; i++ {
			ceilingPost(r, "192.0.2.5:4000", nil)
			require.False(t, saturated, "status %d request %d", c, i)
		}
	}
	code = http.StatusUnauthorized
	ceilingPost(r, "192.0.2.5:4000", nil)
	require.False(t, saturated)
}

// A success from a visitor key takes back what that key contributed, and only
// that key's.
func TestLoginCeiling_SuccessRefundsThatVisitorsFailures(t *testing.T) {
	setTunnelConnectors(t, "")
	gin.SetMode(gin.TestMode)
	l := newLoginCeiling()
	code := http.StatusUnauthorized
	r := gin.New()
	r.POST("/login", l.middleware(nil), func(c *gin.Context) { c.Status(code) })
	post := func(remote string) {
		req := httptest.NewRequest(http.MethodPost, "/login", nil)
		req.RemoteAddr = remote
		r.ServeHTTP(httptest.NewRecorder(), req)
	}
	count := func() int { l.mu.Lock(); defer l.mu.Unlock(); return l.global.count }
	for range 7 {
		post("192.0.2.5:4000")
	}
	for range 4 {
		post("192.0.2.6:4000")
	}
	require.Equal(t, 11, count())
	code = http.StatusOK
	post("192.0.2.5:4000")
	require.Equal(t, 4, count(), "the successful visitor's 7 failures are handed back, the other visitor's 4 stay")
}

func TestLoginCeiling_WindowRollsOver(t *testing.T) {
	setTunnelConnectors(t, "")
	l := newLoginCeiling()
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	code, saturated := http.StatusUnauthorized, false
	r := ceilingRouter(l, &code, &saturated)
	for i := 0; i < loginGlobalCeiling; i++ {
		ceilingPost(r, "192.0.2.5:4000", nil)
	}
	ceilingPost(r, "192.0.2.5:4000", nil)
	require.True(t, saturated)
	now = now.Add(loginCeilingWindow + time.Second)
	ceilingPost(r, "192.0.2.5:4000", nil)
	require.False(t, saturated, "a new window starts empty")
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

// Slots are taken on entry, so parallel guesses cannot all pass a check made
// before any has failed.
func TestLoginCeiling_InFlightAttemptsCountAgainstTheCeiling(t *testing.T) {
	l := newLoginCeiling()
	admitted := 0
	for i := 0; i < loginGlobalCeiling+50; i++ {
		if _, ok, _ := l.reserve(""); ok {
			admitted++
		}
	}
	require.Equal(t, loginGlobalCeiling, admitted)
}

// Saturated requests held in the handlers are bounded; the rest are refused.
func TestLoginCeiling_SaturatedInFlightIsBounded(t *testing.T) {
	setTunnelConnectors(t, "")
	l := newLoginCeiling()
	for i := 0; i < loginGlobalCeiling; i++ {
		l.reserve("")
	}
	gin.SetMode(gin.TestMode)
	hold, entered := make(chan struct{}), make(chan struct{}, maxSaturatedInFlight+10)
	r := gin.New()
	r.POST("/login", l.middleware(nil), func(c *gin.Context) { entered <- struct{}{}; <-hold; c.Status(http.StatusUnauthorized) })
	done := make(chan int, maxSaturatedInFlight)
	for range maxSaturatedInFlight {
		go func() {
			req := httptest.NewRequest(http.MethodPost, "/login", nil)
			req.RemoteAddr = "192.0.2.5:4000"
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			done <- w.Code
		}()
	}
	for range maxSaturatedInFlight {
		<-entered
	}
	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	req.RemoteAddr = "192.0.2.5:4000"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusTooManyRequests, w.Code, "past the bound on saturated requests in flight")
	close(hold)
	for range maxSaturatedInFlight {
		require.Equal(t, http.StatusUnauthorized, <-done)
	}
}

// The exemption needs a verified assertion for a verified, allow-listed email.
func TestLoginCeiling_ExemptionNeedsAVerifiedAllowListedIdentity(t *testing.T) {
	setTunnelConnectors(t, "")
	cfg := oauth.New(oauth.Config{AllowedEmails: []string{"kowner@example.test"}})
	ver := fakeAccessVerifier{
		"jwt-ok":         {Provider: oauth.ProviderCFAccess, Subject: "s1", Email: "kowner@example.test", EmailVerified: true},
		"jwt-not-listed": {Provider: oauth.ProviderCFAccess, Subject: "s2", Email: "stranger@example.test", EmailVerified: true},
		"jwt-unverified": {Provider: oauth.ProviderCFAccess, Subject: "s3", Email: "kowner@example.test", EmailVerified: false},
	}
	l := newLoginCeiling()
	l.exempt = newAccessExemption(ver, cfg)
	for i := 0; i < loginGlobalCeiling; i++ {
		l.reserve("")
	}
	code, saturated := http.StatusUnauthorized, false
	r := ceilingRouter(l, &code, &saturated)
	for _, tc := range []struct {
		name string
		hdr  map[string]string
		want bool // saturated
	}{
		{"verified allow-listed identity", map[string]string{oauth.CFAccessHeader: "jwt-ok"}, false},
		{"the same assertion as the CF_Authorization cookie", map[string]string{"Cookie": "CF_Authorization=jwt-ok"}, false},
		{"verified but not allow-listed", map[string]string{oauth.CFAccessHeader: "jwt-not-listed"}, true},
		{"email not verified", map[string]string{oauth.CFAccessHeader: "jwt-unverified"}, true},
		{"assertion that does not verify", map[string]string{oauth.CFAccessHeader: "forged"}, true},
		{"no assertion", nil, true},
		{"only the unsigned email header", map[string]string{"Cf-Access-Authenticated-User-Email": "kowner@example.test"}, true},
	} {
		saturated = !tc.want
		ceilingPost(r, "192.0.2.5:4000", tc.hdr)
		require.Equal(t, tc.want, saturated, tc.name)
	}
	// An exempt request's failure does not count against the ceilings.
	l2 := newLoginCeiling()
	l2.exempt = newAccessExemption(ver, cfg)
	r2 := ceilingRouter(l2, &code, &saturated)
	for i := 0; i < 3*loginGlobalCeiling; i++ {
		ceilingPost(r2, "192.0.2.5:4000", map[string]string{oauth.CFAccessHeader: "jwt-ok"})
	}
	l2.mu.Lock()
	require.Zero(t, l2.global.count)
	l2.mu.Unlock()
}

// Real router end to end: a flood spends the global ceiling, then
//   - a Cloudflare-verified request is neither delayed nor refused (it still
//     fails on its password, normally);
//   - the first attempt for an untouched account is delayed, not refused;
//   - an account that has failed SoftThreshold times is refused.
func TestLoginCeiling_RealRouterFloodDoesNotLockOutVerifiedOrUntouchedAccounts(t *testing.T) {
	cfAccessVerifierOverride = fakeAccessVerifier{
		"jwt-owner": {Provider: oauth.ProviderCFAccess, Subject: "sub-owner", Email: ownerRouteEmail, EmailVerified: true},
	}
	t.Cleanup(func() { cfAccessVerifierOverride = nil })
	fx := setupCredGuardServerWith(t, func(c *config.Config) {
		c.OwnerEmail = ownerRouteEmail
		c.OAuthAllowedEmails = ownerRouteEmail
		c.OAuthDefaultRole = auth.SeedRoleAdmin
		c.ABSAPIEnabled = true
		c.ABSJWTSecret = "synthetic-throttle-test-secret-0123456789abcdef"
	})
	post := func(path, remote, user string, hdr map[string]string) (int, time.Duration) {
		body, _ := json.Marshal(map[string]string{"username": user, "password": "wrong"})
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = remote
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		start := time.Now()
		fx.srv.router.ServeHTTP(w, req)
		return w.Code, time.Since(start)
	}
	// The flood: unknown usernames from many addresses, split over both logins.
	for i := 0; i < loginGlobalCeiling; i++ {
		path := "/api/v1/auth/login"
		if i%2 == 1 {
			path = "/login"
		}
		code, _ := post(path, fmt.Sprintf("192.0.2.%d:%d", i%200+1, 4000+i), fmt.Sprintf("flood-%d", i), nil)
		require.Equal(t, http.StatusUnauthorized, code, "flood request %d", i)
	}
	const minDelay = absauth.SaturationDelayBase - 100*time.Millisecond

	// An untouched account (the fixture's admin), first attempt, on both logins.
	for _, path := range []string{"/api/v1/auth/login", "/login"} {
		code, took := post(path, "198.51.100.10:4000", "synthetic-admin", nil)
		require.Equal(t, http.StatusUnauthorized, code, "%s: an untouched account is not refused", path)
		require.GreaterOrEqual(t, took, minDelay, "%s: it is delayed instead", path)
	}
	// A Cloudflare-verified request: not delayed, not refused.
	for _, path := range []string{"/api/v1/auth/login", "/login"} {
		code, took := post(path, "198.51.100.11:4000", "synthetic-target", map[string]string{"Cf-Access-Jwt-Assertion": "jwt-owner"})
		require.Equal(t, http.StatusUnauthorized, code, "%s: a verified identity still fails on its password, normally", path)
		require.Less(t, took, minDelay, "%s: and is not delayed", path)
	}
	// An account already past its soft threshold is refused while saturated.
	for i := 0; i < absauth.SoftThreshold; i++ {
		post("/api/v1/auth/login", fmt.Sprintf("203.0.113.%d:4000", i+1), "synthetic-admin", map[string]string{"Cf-Access-Jwt-Assertion": "jwt-owner"})
	}
	code, took := post("/api/v1/auth/login", "198.51.100.12:4000", "synthetic-admin", nil)
	require.Equal(t, http.StatusTooManyRequests, code, "an account past %d failures of its own is refused", absauth.SoftThreshold)
	require.Less(t, took, minDelay, "a refusal is not delayed")
}
