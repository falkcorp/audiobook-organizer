// file: internal/server/login_gate_test.go
// version: 5.0.0
// guid: 47178e5a-1329-49cf-847d-f019456d21c6
// last-edited: 2026-11-10

package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/oauth"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// The ceiling's own state cases (refund arithmetic, the visitor table's sweep
// and bound, the saturated refusals' Retry-After values, the service-wide
// bucket) moved to core/auth/ceiling_test.go with coreauth.LoginCeiling at
// F4-02; these cases drive the gin gate in front of it.

// ceilingRouter is a router whose one route answers with *code, behind the
// gate over l, and reports whether the request reached it saturated.
func ceilingRouter(l *coreauth.LoginCeiling, code *int, sawSaturated *bool) *gin.Engine {
	return ceilingRouterWith(loginCeilingMiddleware(l, nil, nil, nil), code, sawSaturated)
}

// ceilingRouterWith is ceilingRouter behind the gate mw.
func ceilingRouterWith(mw gin.HandlerFunc, code *int, sawSaturated *bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/login", mw, func(c *gin.Context) {
		if sawSaturated != nil {
			*sawSaturated = absauth.LoginSaturated(c)
		}
		if a := c.GetHeader("X-Test-Account"); a != "" {
			absauth.NoteLoginAccount(c, a)
		}
		c.Status(*code)
	})
	return r
}

// fillCeiling spends n global slots of l with failures no success refunds, as
// n failed logins from an unattributed visitor would.
func fillCeiling(t *testing.T, l *coreauth.LoginCeiling, n int) {
	t.Helper()
	for range n {
		a, d := l.Admit("", nil, "")
		require.NotNil(t, a, "refusal %d", d.Refusal)
		require.False(t, a.Saturated(), "the ceiling was spent before the fill ended")
		a.Done(coreauth.LoginFailed, "")
	}
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

// postWithin serves a bodiless POST /login through r and fails the test when it
// is not answered within d. The handler may be parked by the test itself; those
// callers run it in a goroutine instead.
func postWithin(t *testing.T, r *gin.Engine, remote string, d time.Duration) int {
	t.Helper()
	done := make(chan int, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/login", nil)
		req.RemoteAddr = remote
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		done <- w.Code
	}()
	select {
	case code := <-done:
		return code
	case <-time.After(d):
		t.Fatalf("POST /login from %s was not answered within %v", remote, d)
		return 0
	}
}

// A spent ceiling does not refuse: the request reaches the handler marked
// saturated, so the handler can delay it by what its account has already failed.
func TestLoginCeiling_ConnectorCeilingMarksSaturatedAndOthersAreNot(t *testing.T) {
	setTunnelConnectors(t, throttleTestConnector+",192.0.2.46")
	l := newLoginCeiling()
	code, saturated := http.StatusUnauthorized, false
	r := ceilingRouter(l, &code, &saturated)
	for i := 0; i < coreauth.LoginConnectorCeiling; i++ {
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
	for i := 0; i < coreauth.LoginGlobalCeiling; i++ {
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
		for i := 0; i < 2*coreauth.LoginGlobalCeiling; i++ {
			ceilingPost(r, "192.0.2.5:4000", nil)
			require.False(t, saturated, "status %d request %d", c, i)
		}
	}
	code = http.StatusUnauthorized
	ceilingPost(r, "192.0.2.5:4000", nil)
	require.False(t, saturated)
}

func ceilingGlobalCount(l *coreauth.LoginCeiling) int {
	g, _ := l.Failures("")
	return g
}

// A success hands back only what the same visitor failed against the SAME
// account. A caller with one valid account cannot log in to clear its count and
// keep guessing at others: those failures stay counted at every level.
func TestLoginCeiling_SuccessRefundsOnlyTheSameAccountsFailures(t *testing.T) {
	setTunnelConnectors(t, throttleTestConnector)
	l := newLoginCeiling()
	code := http.StatusUnauthorized
	r := ceilingRouter(l, &code, nil)
	conn := throttleTestConnector + ":4000"
	for range 10 {
		ceilingPost(r, conn, map[string]string{"X-Test-Account": "acct-b", "Cf-Connecting-Ip": "192.0.2.5"})
	}
	for range 3 {
		ceilingPost(r, conn, map[string]string{"X-Test-Account": "acct-a", "Cf-Connecting-Ip": "192.0.2.5"})
	}
	// Another visitor's failures on account A are not this visitor's to refund.
	for range 4 {
		ceilingPost(r, conn, map[string]string{"X-Test-Account": "acct-a", "Cf-Connecting-Ip": "192.0.2.6"})
	}
	require.Equal(t, 17, ceilingGlobalCount(l))
	code = http.StatusOK
	ceilingPost(r, conn, map[string]string{"X-Test-Account": "acct-a", "Cf-Connecting-Ip": "192.0.2.5"})
	require.Equal(t, 14, ceilingGlobalCount(l), "only the 3 failures on account A are handed back; the 10 on B and the other visitor's 4 stay")
	connCount := func() int {
		_, n := l.Failures(throttleTestConnector)
		return n
	}
	require.Equal(t, 14, connCount(), "the connector level agrees")
	// A second success on A has nothing left to refund.
	ceilingPost(r, conn, map[string]string{"X-Test-Account": "acct-a", "Cf-Connecting-Ip": "192.0.2.5"})
	require.Equal(t, 14, ceilingGlobalCount(l))
}

// Failures against an unknown username have no account, so no success refunds them.
func TestLoginCeiling_UnknownAccountFailuresAreNeverRefunded(t *testing.T) {
	setTunnelConnectors(t, "")
	l := newLoginCeiling()
	code := http.StatusUnauthorized
	r := ceilingRouter(l, &code, nil)
	for range 6 {
		ceilingPost(r, "192.0.2.5:4000", nil)
	}
	code = http.StatusOK
	ceilingPost(r, "192.0.2.5:4000", map[string]string{"X-Test-Account": "acct-a"})
	require.Equal(t, 6, ceilingGlobalCount(l))
}

// Saturated requests are bounded per visitor: a visitor holds at most
// coreauth.MaxSaturatedPerVisitor, and above coreauth.MaxSaturatedSoft one. The service-wide
// admission bucket (TestLoginCeiling_SaturatedAdmissionIsBoundedServiceWide) is
// switched off here so the per-visitor rules are what is measured.
func TestLoginCeiling_SaturatedInFlightIsBoundedPerVisitor(t *testing.T) {
	setTunnelConnectors(t, "")
	l := newLoginCeiling()
	l.SetSaturatedBound(math.Inf(1), 1)
	fillCeiling(t, l, coreauth.LoginGlobalCeiling)
	gin.SetMode(gin.TestMode)
	hold := make(chan struct{})
	entered := make(chan struct{}, 4*coreauth.MaxSaturatedSoft)
	r := gin.New()
	r.POST("/login", loginCeilingMiddleware(l, nil, nil, nil), func(c *gin.Context) { entered <- struct{}{}; <-hold; c.Status(http.StatusOK) })
	var wg sync.WaitGroup
	// post fails the test, rather than hanging it, when the request is neither
	// answered nor admitted within the deadline (a regression that parks the
	// request would otherwise stall the suite until the package timeout).
	post := func(remote string) int {
		return postWithin(t, r, remote, 10*time.Second)
	}
	spawn := func(remote string) {
		wg.Add(1)
		go func() { defer wg.Done(); post(remote) }()
	}
	// One visitor: its third concurrent saturated request is refused.
	for range coreauth.MaxSaturatedPerVisitor {
		spawn("192.0.2.5:4000")
	}
	for range coreauth.MaxSaturatedPerVisitor {
		<-entered
	}
	require.Equal(t, http.StatusTooManyRequests, post("192.0.2.5:4000"), "past the per-visitor bound")
	// A flood from many fresh visitors is all admitted (one each) while the
	// service-wide bucket is off...
	const flood = coreauth.MaxSaturatedSoft + 44
	for i := 0; i < flood; i++ {
		spawn(fmt.Sprintf("198.51.%d.%d:4000", 100+i/250, i%250+1))
	}
	for range flood {
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("a fresh visitor was not admitted by the per-visitor rules while others flood")
		}
	}
	// ...and with the service-wide count high, a visitor already in flight is held
	// to one, while a brand-new visitor is still admitted by the per-visitor rules.
	require.Equal(t, http.StatusTooManyRequests, post("198.51.100.1:4000"), "above the soft bound a visitor with one in flight is refused")
	spawn("203.0.113.77:4000")
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("a fresh visitor was refused by the per-visitor rules while others flood")
	}
	close(hold)
	wg.Wait()
}

// A visitor that already failed while saturated is refused outright, with no
// delay, until the window ends; other visitors are unaffected.
func TestLoginCeiling_VisitorThatFailedWhileSaturatedIsRefused(t *testing.T) {
	setTunnelConnectors(t, "")
	l := newLoginCeiling()
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	l.SetClock(func() time.Time { return now })
	fillCeiling(t, l, coreauth.LoginGlobalCeiling)
	code, saturated := http.StatusUnauthorized, false
	r := ceilingRouter(l, &code, &saturated)
	require.Equal(t, http.StatusUnauthorized, ceilingPost(r, "192.0.2.5:4000", nil), "first saturated attempt reaches the handler")
	require.True(t, saturated)
	saturated = false
	require.Equal(t, http.StatusTooManyRequests, ceilingPost(r, "192.0.2.5:4000", nil), "the same visitor's next one is refused")
	require.False(t, saturated, "...without reaching the handler")
	require.Equal(t, http.StatusUnauthorized, ceilingPost(r, "192.0.2.6:4000", nil), "another visitor is admitted")
	now = now.Add(coreauth.LoginCeilingWindow + time.Second)
	require.Equal(t, http.StatusUnauthorized, ceilingPost(r, "192.0.2.5:4000", nil), "after the window the visitor reaches the handler again")
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
	exempt := newAccessExemption(ver, cfg)
	fillCeiling(t, l, coreauth.LoginGlobalCeiling)
	code, saturated := http.StatusUnauthorized, false
	r := ceilingRouterWith(loginCeilingMiddleware(l, exempt, nil, nil), &code, &saturated)
	i := 0
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
		i++
		ceilingPost(r, fmt.Sprintf("192.0.2.%d:4000", 10+i), tc.hdr) // a visitor each: one that failed while saturated is refused outright
		require.Equal(t, tc.want, saturated, tc.name)
	}
	// An exempt request's failure does not count against the ceilings.
	l2 := newLoginCeiling()
	r2 := ceilingRouterWith(loginCeilingMiddleware(l2, exempt, nil, nil), &code, &saturated)
	for i := 0; i < 3*coreauth.LoginGlobalCeiling; i++ {
		ceilingPost(r2, "192.0.2.5:4000", map[string]string{oauth.CFAccessHeader: "jwt-ok"})
	}
	require.Zero(t, ceilingGlobalCount(l2))
}

// Real router end to end: a flood spends the global ceiling, then
//   - a Cloudflare-verified request is neither delayed nor refused (it still
//     fails on its password, normally), on both logins, and is exempt from the
//     saturated-admission bucket too;
//   - the attempt for an untouched account is delayed, not refused, while the
//     service-wide saturated-admission bucket has a token;
//   - an account that has failed SoftThreshold times is refused;
//   - once the bucket is empty a fresh visitor is refused 429 (Retry-After)
//     BEFORE any user lookup, with the same answer and no delay whether the
//     name exists or not. The bucket is pinned to exactly the admissions this
//     test makes (8: 2 untouched + 4 refused-by-account + 2 delayed) and a
//     negligible refill rate, so the count is deterministic.
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
	// Every failed login now spends a real argon2id derivation (so an unknown name
	// takes as long as a real one), so only a first slice of the flood goes through
	// the router, enough to show both logins charge the one global bucket; the rest
	// of the ceiling is taken directly, which is exactly what the remaining failures
	// would have done.
	const realFlood = 20
	for i := 0; i < realFlood; i++ {
		path := "/api/v1/auth/login"
		if i%2 == 1 {
			path = "/login"
		}
		code, _ := post(path, fmt.Sprintf("192.0.2.%d:%d", i%200+1, 4000+i), fmt.Sprintf("flood-%d", i), nil)
		require.Equal(t, http.StatusUnauthorized, code, "flood request %d", i)
	}
	ceiling := fx.srv.loginCeiling()
	require.Equal(t, realFlood, ceilingGlobalCount(ceiling), "failures through both logins count in the one global bucket")
	fillCeiling(t, ceiling, coreauth.LoginGlobalCeiling-realFlood)
	const minDelay = coreauth.SaturationDelayBase - 100*time.Millisecond
	const satAdmissions = 8
	// Deliberately below the validated floor (config.MinLoginSaturatedRate): the
	// bucket is meant to refill negligibly, and the computed wait then hits the
	// defensive window cap, which this test pins.
	ceiling.SetSaturatedBound(0.0001, satAdmissions)

	// An untouched account (the fixture's admin), first attempt, on both logins.
	// (a different visitor per login: one that fails while saturated is refused
	// outright afterwards, which the next test of the visitor rule covers)
	for i, path := range []string{"/api/v1/auth/login", "/login"} {
		code, took := post(path, fmt.Sprintf("198.51.100.%d:4000", 20+i), "synthetic-admin", nil)
		require.Equal(t, http.StatusUnauthorized, code, "%s: an untouched account is not refused", path)
		require.GreaterOrEqual(t, took, minDelay, "%s: it is delayed instead", path)
	}
	// A Cloudflare-verified request: not delayed, not refused.
	for _, path := range []string{"/api/v1/auth/login", "/login"} {
		code, took := post(path, "198.51.100.11:4000", "synthetic-target", map[string]string{"Cf-Access-Jwt-Assertion": "jwt-owner"})
		require.Equal(t, http.StatusUnauthorized, code, "%s: a verified identity still fails on its password, normally", path)
		require.Less(t, took, minDelay, "%s: and is not delayed", path)
	}
	// An account already past its soft threshold is refused while saturated. The
	// failures arrive through BOTH logins (they share one per-account counter), and
	// the same is done to a name that does not exist: while saturated the answer
	// must not tell the two apart.
	for _, name := range []string{"synthetic-admin", "no-such-account"} {
		for i := 0; i < coreauth.DefaultSoftThreshold; i++ {
			path := "/api/v1/auth/login"
			if i%2 == 1 {
				path = "/login"
			}
			post(path, fmt.Sprintf("203.0.113.%d:4000", i+1), name, map[string]string{"Cf-Access-Jwt-Assertion": "jwt-owner"})
		}
	}
	// (a visitor of its own each time: a refusal is itself a failure while
	// saturated, after which the visitor is refused outright whatever it asks)
	visitor := 40
	for _, name := range []string{"synthetic-admin", "no-such-account"} {
		for _, path := range []string{"/api/v1/auth/login", "/login"} {
			visitor++
			code, took := post(path, fmt.Sprintf("198.51.100.%d:4000", visitor), name, nil)
			require.Equal(t, http.StatusTooManyRequests, code, "%s %s: a name past %d failures of its own is refused, whether or not it exists", path, name, coreauth.DefaultSoftThreshold)
			require.Less(t, took, minDelay, "a refusal is not delayed")
		}
	}
	// A name with fewer failures is delayed, exists or not.
	for _, name := range []string{"synthetic-target", "another-ghost"} {
		visitor++
		code, took := post("/api/v1/auth/login", fmt.Sprintf("198.51.100.%d:4000", visitor), name, nil)
		require.Equal(t, http.StatusUnauthorized, code, name)
		require.GreaterOrEqual(t, took, minDelay, "%s: delayed the same way", name)
	}
	// The bucket is now empty: a fresh visitor is refused before any user lookup,
	// the same for a name that exists and one that does not, with no delay.
	for _, name := range []string{"synthetic-target", "synthetic-admin", "yet-another-ghost"} {
		for _, path := range []string{"/api/v1/auth/login", "/login"} {
			visitor++
			w := httptest.NewRecorder()
			body, _ := json.Marshal(map[string]string{"username": name, "password": "wrong"})
			req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.RemoteAddr = fmt.Sprintf("198.51.100.%d:4000", visitor)
			start := time.Now()
			fx.srv.router.ServeHTTP(w, req)
			require.Equal(t, http.StatusTooManyRequests, w.Code, "%s %s: saturated admission is refused once the bucket is empty", path, name)
			// rate 0.0001/s is deliberately below the validated floor, so the computed wait
			// is far past the window and the defensive cap shows
			require.Equal(t, strconv.Itoa(int(coreauth.LoginCeilingWindow/time.Second)), w.Header().Get("Retry-After"), "%s %s", path, name)
			require.Less(t, time.Since(start), minDelay, "%s %s: refused without the saturation delay", path, name)
		}
	}
	// A Cloudflare Access login (verified identity) is unaffected by the empty bucket.
	for _, path := range []string{"/api/v1/auth/login", "/login"} {
		code, _ := post(path, "198.51.100.12:4000", "synthetic-target", map[string]string{"Cf-Access-Jwt-Assertion": "jwt-owner"})
		require.Equal(t, http.StatusUnauthorized, code, "%s: a verified Cloudflare Access login is not refused by the saturated bucket", path)
	}
}

// B-3: the body is read FIRST, under a size cap, and the handler gets the bytes
// back from memory.
func TestLoginCeiling_BodyIsReadFirstAndHandedBackBuffered(t *testing.T) {
	setTunnelConnectors(t, "")
	l := newLoginCeiling()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	var got string
	reached := false
	r.POST("/login", loginCeilingMiddleware(l, nil, nil, nil), func(c *gin.Context) {
		reached = true
		b, _ := io.ReadAll(c.Request.Body)
		got = string(b)
		c.Status(http.StatusUnauthorized)
	})
	post := func(body string, declare int64) int {
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(body))
		req.ContentLength = declare
		req.RemoteAddr = "192.0.2.5:4000"
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	const ok = `{"username":"a","password":"b"}`
	require.Equal(t, http.StatusUnauthorized, post(ok, int64(len(ok))))
	require.Equal(t, ok, got, "the handler reads what was sent")
	require.Equal(t, 1, ceilingGlobalCount(l))

	reached = false
	big := strings.Repeat("a", loginBodyLimit+1)
	require.Equal(t, http.StatusRequestEntityTooLarge, post(big, -1), "an undeclared oversize body is cut at the cap")
	require.Equal(t, http.StatusRequestEntityTooLarge, post("x", loginBodyLimit+1), "a declared oversize body is refused before it is read")
	require.False(t, reached, "an oversize body never reaches the handler")
	require.Equal(t, 1, ceilingGlobalCount(l), "...and holds no ceiling slot")

	atCap := strings.Repeat(" ", loginBodyLimit-len(ok)) + ok
	require.Equal(t, http.StatusUnauthorized, post(atCap, int64(len(atCap))), "exactly at the cap is fine")
}

// B-3: a caller that opens a login and stalls its body is answered 408 within the
// read deadline, holds no ceiling slot and no in-flight slot, and never reaches
// the handler.
func TestLoginCeiling_StalledBodyHoldsNoSlotAndTimesOut(t *testing.T) {
	setTunnelConnectors(t, "")
	saved := loginBodyReadTimeout
	loginBodyReadTimeout = 300 * time.Millisecond
	t.Cleanup(func() { loginBodyReadTimeout = saved })
	l := newLoginCeiling()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	reached := make(chan struct{}, 1)
	// The production writer chain in gin: the raw controller captured (core's
	// stage 9 compresses outside the engine since R1-17).
	r.Use(captureRawResponseController())
	r.POST("/login", loginCeilingMiddleware(l, nil, nil, nil), func(c *gin.Context) { reached <- struct{}{}; c.Status(http.StatusUnauthorized) })
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	require.NoError(t, err)
	defer conn.Close()
	fmt.Fprintf(conn, "POST /login HTTP/1.1\r\nHost: x\r\nAccept-Encoding: gzip\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"us")
	start := time.Now()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	require.NoError(t, err, "a stalled body must be answered, not left hanging")
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	require.Equal(t, http.StatusRequestTimeout, resp.StatusCode)
	require.Less(t, time.Since(start), 3*time.Second)
	select {
	case <-reached:
		t.Fatal("a stalled body reached the handler")
	default:
	}
	require.Zero(t, ceilingGlobalCount(l), "no ceiling slot was ever taken")
	logins, _ := l.InFlight("127.0.0.1")
	require.Zero(t, logins, "no in-flight slot is held")
}

// B-3: one visitor may have coreauth.MaxLoginInFlightPerVisitor logins in the handlers at
// once, saturated or not; the next is refused, others are unaffected, and the
// slot comes back when a request ends.
func TestLoginCeiling_PerVisitorInFlightCap(t *testing.T) {
	setTunnelConnectors(t, "")
	l := newLoginCeiling()
	gin.SetMode(gin.TestMode)
	hold := make(chan struct{})
	entered := make(chan struct{}, 64)
	r := gin.New()
	r.POST("/login", loginCeilingMiddleware(l, nil, nil, nil), func(c *gin.Context) {
		entered <- struct{}{}
		<-hold
		c.Status(http.StatusOK)
	})
	var wg sync.WaitGroup
	spawn := func(remote string) {
		wg.Add(1)
		go func() { defer wg.Done(); postWithin(t, r, remote, 20*time.Second) }()
	}
	wait := func(why string) {
		t.Helper()
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal(why)
		}
	}
	for range coreauth.MaxLoginInFlightPerVisitor {
		spawn("192.0.2.5:4000")
	}
	for range coreauth.MaxLoginInFlightPerVisitor {
		wait("a visitor's login under the cap was not admitted")
	}
	// Timed, so a regression that admits the request parks it in the held handler
	// and fails here rather than hanging the test.
	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	req.RemoteAddr = "192.0.2.5:4001"
	w := httptest.NewRecorder()
	refused := make(chan struct{})
	go func() { r.ServeHTTP(w, req); close(refused) }()
	select {
	case <-refused:
	case <-time.After(5 * time.Second):
		t.Fatal("the fifth concurrent login of a visitor was admitted (parked in the handler), not refused")
	}
	require.Equal(t, http.StatusTooManyRequests, w.Code, "past the per-visitor in-flight cap")
	require.Equal(t, "5", w.Header().Get("Retry-After"))
	require.Equal(t, coreauth.MaxLoginInFlightPerVisitor, ceilingGlobalCount(l), "the refused request took no ceiling slot")

	spawn("192.0.2.6:4000")
	wait("another visitor was refused because of the first one")
	close(hold)
	wg.Wait()
	// Slots came back.
	require.Equal(t, http.StatusOK, postWithin(t, r, "192.0.2.5:4000", 10*time.Second))
}

// S-1: a handler that refuses a saturated login because the account is past its
// threshold has failed that visitor too: it gets no further attempt this window,
// exactly like one whose attempt answered 401.
func TestLoginCeiling_SaturationRefusalCountsAsASaturatedFailure(t *testing.T) {
	setTunnelConnectors(t, "")
	l := newLoginCeiling()
	fillCeiling(t, l, coreauth.LoginGlobalCeiling)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	reached := 0
	r.POST("/login", loginCeilingMiddleware(l, nil, nil, nil), func(c *gin.Context) {
		reached++
		absauth.NoteLoginAccount(c, "acct-a")
		if absauth.RefuseSaturatedLogin(c, coreauth.DefaultSoftThreshold, func(time.Duration) {}) {
			c.Status(http.StatusTooManyRequests)
			return
		}
		c.Status(http.StatusOK)
	})
	require.Equal(t, http.StatusTooManyRequests, postWithin(t, r, "192.0.2.5:4000", 5*time.Second))
	require.Equal(t, 1, reached)
	require.Equal(t, http.StatusTooManyRequests, postWithin(t, r, "192.0.2.5:4000", 5*time.Second))
	require.Equal(t, 1, reached, "the refusal counted: the visitor's next login is refused without reaching the handler")
	require.Equal(t, http.StatusTooManyRequests, postWithin(t, r, "192.0.2.6:4000", 5*time.Second))
	require.Equal(t, 2, reached, "a different visitor is admitted to the handler")
}

// N6: the exemption is read when a request arrives, so a loginCeilingExempt set
// after the ceiling was first built is still honoured.
func TestLoginCeiling_ExemptionIsReadLazily(t *testing.T) {
	setTunnelConnectors(t, "")
	s := &Server{}
	l := s.loginCeiling()                    // built first, as wire_handlers does
	mw := s.loginCeilingMiddleware(nil, nil) // and the gate with it
	s.loginCeilingExempt = func(*gin.Context) bool { return true }
	fillCeiling(t, l, coreauth.LoginGlobalCeiling)
	code, saturated := http.StatusUnauthorized, true
	r := ceilingRouterWith(mw, &code, &saturated)
	ceilingPost(r, "192.0.2.5:4000", nil)
	require.False(t, saturated, "an exemption installed after the ceiling was built applies")
	require.Same(t, l, s.loginCeiling(), "web and ABS logins share one ceiling")
	require.Same(t, s.loginThrottle(), s.loginThrottle(), "...and one failed-login throttle")
}

// B4-2: saturated admission is keyed per public IPv6 /48, not /64, so a caller
// that owns a /48 cannot mint a fresh "first attempt" per /64. IPv4 and a
// different /48 are separate.
func TestLoginCeiling_SaturatedAdmissionIsKeyedPerIPv6Slash48(t *testing.T) {
	setTunnelConnectors(t, "")
	l := newLoginCeiling()
	l.SetSaturatedBound(math.Inf(1), 1)
	fillCeiling(t, l, coreauth.LoginGlobalCeiling)
	code := http.StatusUnauthorized
	r := ceilingRouter(l, &code, nil)
	require.Equal(t, http.StatusUnauthorized, ceilingPost(r, "[2001:db8:aaaa:1::1]:4000", nil), "first attempt reaches the handler")
	require.Equal(t, http.StatusTooManyRequests, ceilingPost(r, "[2001:db8:aaaa:2::1]:4000", nil), "another /64 of the same /48 shares the refusal")
	require.Equal(t, http.StatusTooManyRequests, ceilingPost(r, "[2001:db8:aaaa:ffff::9]:4000", nil))
	require.Equal(t, http.StatusUnauthorized, ceilingPost(r, "[2001:db8:bbbb:1::1]:4000", nil), "a different /48 is its own visitor")
	require.Equal(t, http.StatusUnauthorized, ceilingPost(r, "192.0.2.5:4000", nil), "IPv4 keyed per address")
	require.Equal(t, http.StatusUnauthorized, ceilingPost(r, "192.0.2.6:4000", nil))
}

func TestClientSaturatedKey(t *testing.T) {
	for in, want := range map[string]string{
		"2001:db8:aaaa:1::1":    "2001:db8:aaaa::/48",
		"2001:db8:aaaa:ffff::9": "2001:db8:aaaa::/48",
		"::ffff:192.0.2.5":      "192.0.2.5",
		"192.0.2.5":             "192.0.2.5",
		"fe80::1%eth0":          "fe80::1", // link-local LAN: its own key, as clientPrefixKey
	} {
		got, ok := clientSaturatedKey(in)
		require.True(t, ok, in)
		require.Equal(t, want, got, in)
	}
	_, ok := clientSaturatedKey("not-an-ip")
	require.False(t, ok)
}

// An unusable saturated-admission setting stops the service starting; a usable
// one (or none) does not, and reaches the ceiling the server builds.
func TestStartupLoginSaturated_RefusesUnusableSettings(t *testing.T) {
	prevRate, prevBurst := config.Snapshot().LoginSaturatedRate, config.Snapshot().LoginSaturatedBurst
	t.Cleanup(func() {
		config.Mutate(func(c *config.Config) { c.LoginSaturatedRate, c.LoginSaturatedBurst = prevRate, prevBurst })
	})
	set := func(r, b string) {
		config.Mutate(func(c *config.Config) { c.LoginSaturatedRate, c.LoginSaturatedBurst = r, b })
	}
	set("", "")
	require.NoError(t, startupLoginSaturated())
	set("0", "")
	require.ErrorContains(t, startupLoginSaturated(), "LOGIN_SATURATED_RATE")
	set("2", "0")
	require.ErrorContains(t, startupLoginSaturated(), "LOGIN_SATURATED_BURST")
	set("0.5", "3")
	require.NoError(t, startupLoginSaturated())
	s := &Server{}
	l := s.loginCeiling()
	lim, burst := l.SaturatedBound()
	require.InDelta(t, 0.5, lim, 1e-9)
	require.Equal(t, 3, burst)
}

// SF-C through the router: the header and the wording per refusal kind.
func TestLoginCeiling_SaturatedRefusalsAnswerWithTheirOwnRetryAfterAndWording(t *testing.T) {
	setTunnelConnectors(t, "")
	l := newLoginCeiling()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	l.SetClock(func() time.Time { return now })
	l.SetSaturatedBound(0.5, 2)
	fillCeiling(t, l, coreauth.LoginGlobalCeiling)
	code := http.StatusUnauthorized
	r := ceilingRouter(l, &code, nil)
	serve := func(remote string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/login", nil)
		req.RemoteAddr = remote
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	// First attempt fails while saturated (spends one of two tokens).
	require.Equal(t, http.StatusUnauthorized, serve("192.0.2.5:4000").Code)
	now = now.Add(30 * time.Second)
	// Same visitor again: refused for its own failure, with the rest of its window.
	w := serve("192.0.2.5:4000")
	require.Equal(t, http.StatusTooManyRequests, w.Code)
	require.Equal(t, strconv.Itoa(int((coreauth.LoginCeilingWindow-30*time.Second)/time.Second)), w.Header().Get("Retry-After"))
	require.Contains(t, w.Body.String(), "too many failed login attempts")
	// Drain the bucket (30s refilled it to its burst of 2: one admission leaves one).
	code = http.StatusOK
	require.Equal(t, http.StatusOK, serve("192.0.2.6:4000").Code)
	require.Equal(t, http.StatusOK, serve("192.0.2.7:4000").Code)
	w = serve("192.0.2.8:4000")
	require.Equal(t, http.StatusTooManyRequests, w.Code)
	require.Equal(t, "2", w.Header().Get("Retry-After"), "an empty bucket at 0.5/s")
	require.Contains(t, w.Body.String(), "too many login attempts right now")
}

// SF-C: the per-visitor in-flight refusal has its own wording and the short fixed
// wait. Handlers are parked, so the visitor's saturated requests stay in flight.
func TestLoginCeiling_SaturatedBusyRefusalWordingAndRetryAfter(t *testing.T) {
	setTunnelConnectors(t, "")
	l := newLoginCeiling()
	l.SetSaturatedBound(math.Inf(1), 1)
	fillCeiling(t, l, coreauth.LoginGlobalCeiling)
	gin.SetMode(gin.TestMode)
	hold := make(chan struct{})
	entered := make(chan struct{}, 8)
	r := gin.New()
	r.POST("/login", loginCeilingMiddleware(l, nil, nil, nil), func(c *gin.Context) {
		entered <- struct{}{}
		<-hold
		c.Status(http.StatusOK)
	})
	var wg sync.WaitGroup
	for i := 0; i < coreauth.MaxSaturatedPerVisitor; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); postWithin(t, r, "192.0.2.5:4000", 20*time.Second) }()
	}
	for i := 0; i < coreauth.MaxSaturatedPerVisitor; i++ {
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("a saturated login under the per-visitor cap was not admitted")
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	req.RemoteAddr = "192.0.2.5:4001"
	w := httptest.NewRecorder()
	refused := make(chan struct{})
	go func() { r.ServeHTTP(w, req); close(refused) }()
	select {
	case <-refused:
	case <-time.After(5 * time.Second):
		t.Fatal("a third saturated login of one visitor was admitted, not refused")
	}
	require.Equal(t, http.StatusTooManyRequests, w.Code)
	require.Equal(t, "5", w.Header().Get("Retry-After"))
	require.Contains(t, w.Body.String(), "too many simultaneous login attempts")
	close(hold)
	wg.Wait()
}
