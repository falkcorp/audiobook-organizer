// file: internal/server/login_ceiling_test.go
// version: 4.0.0
// guid: 47178e5a-1329-49cf-847d-f019456d21c6
// last-edited: 2026-10-22

package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
		if a := c.GetHeader("X-Test-Account"); a != "" {
			absauth.NoteLoginAccount(c, a)
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

func ceilingGlobalCount(l *loginCeiling) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.global.count
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
		l.mu.Lock()
		defer l.mu.Unlock()
		n := 0
		for _, b := range l.conns {
			n += b.count
		}
		return n
	}
	require.Equal(t, 14, connCount(), "the connector level agrees")
	// A second success on A has nothing left to refund.
	ceilingPost(r, conn, map[string]string{"X-Test-Account": "acct-a", "Cf-Connecting-Ip": "192.0.2.5"})
	require.Equal(t, 14, ceilingGlobalCount(l))
}

// A success refunds at most SoftThreshold failures and only those from the last
// 15 minutes.
func TestLoginCeiling_RefundIsCappedAndAgeLimited(t *testing.T) {
	setTunnelConnectors(t, "")
	l := newLoginCeiling()
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	code := http.StatusUnauthorized
	r := ceilingRouter(l, &code, nil)
	hdr := map[string]string{"X-Test-Account": "acct-a"}
	for range 20 {
		ceilingPost(r, "192.0.2.5:4000", hdr)
	}
	require.Equal(t, 20, ceilingGlobalCount(l))
	code = http.StatusOK
	ceilingPost(r, "192.0.2.5:4000", hdr)
	require.Equal(t, 20-absauth.SoftThreshold, ceilingGlobalCount(l), "capped at the per-account limit")

	// Age: failures older than the window are not refunded even if their
	// bucket window is somehow still open.
	l2 := newLoginCeiling()
	l2.now = func() time.Time { return now }
	for range 5 {
		tk, ok, _ := l2.reserve("")
		require.True(t, ok)
		l2.failed("v", "acct-a", tk)
	}
	l2.mu.Lock()
	vis, _ := l2.visitors.Get("v")
	fs := vis.byAccount["acct-a"]
	for i := range 3 {
		fs[i].at = now.Add(-loginCeilingWindow - time.Minute)
	}
	l2.mu.Unlock()
	tk, ok, _ := l2.reserve("")
	require.True(t, ok)
	require.Equal(t, 6, ceilingGlobalCount(l2))
	l2.succeeded("v", "acct-a", tk)
	require.Equal(t, 3, ceilingGlobalCount(l2), "the success's own slot and the 2 recent failures go back; the 3 old ones stay")
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

// Saturated requests are bounded per visitor, not by one shared pool: a visitor
// holds at most maxSaturatedPerVisitor, and a flood from many visitors does not
// make a fresh visitor's first attempt fail.
func TestLoginCeiling_SaturatedInFlightIsBoundedPerVisitor(t *testing.T) {
	setTunnelConnectors(t, "")
	l := newLoginCeiling()
	for i := 0; i < loginGlobalCeiling; i++ {
		l.reserve("")
	}
	gin.SetMode(gin.TestMode)
	hold := make(chan struct{})
	entered := make(chan struct{}, 4*maxSaturatedSoft)
	r := gin.New()
	r.POST("/login", l.middleware(nil), func(c *gin.Context) { entered <- struct{}{}; <-hold; c.Status(http.StatusOK) })
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
	for range maxSaturatedPerVisitor {
		spawn("192.0.2.5:4000")
	}
	for range maxSaturatedPerVisitor {
		<-entered
	}
	require.Equal(t, http.StatusTooManyRequests, post("192.0.2.5:4000"), "past the per-visitor bound")
	// A flood from many fresh visitors is all admitted (one each)...
	const flood = maxSaturatedSoft + 44
	for i := 0; i < flood; i++ {
		spawn(fmt.Sprintf("198.51.%d.%d:4000", 100+i/250, i%250+1))
	}
	for range flood {
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("a fresh visitor's first attempt was not admitted while others flood")
		}
	}
	// ...and with the service-wide count high, a visitor already in flight is held
	// to one, while a brand-new visitor still gets its first attempt.
	require.Equal(t, http.StatusTooManyRequests, post("198.51.100.1:4000"), "above the soft bound a visitor with one in flight is refused")
	spawn("203.0.113.77:4000")
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("a fresh visitor's first attempt was refused while others flood")
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
	l.now = func() time.Time { return now }
	for i := 0; i < loginGlobalCeiling; i++ {
		l.reserve("")
	}
	code, saturated := http.StatusUnauthorized, false
	r := ceilingRouter(l, &code, &saturated)
	require.Equal(t, http.StatusUnauthorized, ceilingPost(r, "192.0.2.5:4000", nil), "first saturated attempt reaches the handler")
	require.True(t, saturated)
	saturated = false
	require.Equal(t, http.StatusTooManyRequests, ceilingPost(r, "192.0.2.5:4000", nil), "the same visitor's next one is refused")
	require.False(t, saturated, "...without reaching the handler")
	require.Equal(t, http.StatusUnauthorized, ceilingPost(r, "192.0.2.6:4000", nil), "another visitor is admitted")
	now = now.Add(loginCeilingWindow + time.Second)
	l.global = loginCeilingBucket{} // the window has turned over
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
	l.exempt = newAccessExemption(ver, cfg)
	for i := 0; i < loginGlobalCeiling; i++ {
		l.reserve("")
	}
	code, saturated := http.StatusUnauthorized, false
	r := ceilingRouter(l, &code, &saturated)
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
		for i := 0; i < absauth.SoftThreshold; i++ {
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
			require.Equal(t, http.StatusTooManyRequests, code, "%s %s: a name past %d failures of its own is refused, whether or not it exists", path, name, absauth.SoftThreshold)
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
	r.POST("/login", l.middleware(nil), func(c *gin.Context) {
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
	// The production writer chain: raw controller captured, then gzip.
	r.Use(captureRawResponseController(), compressionMiddleware())
	r.POST("/login", l.middleware(nil), func(c *gin.Context) { reached <- struct{}{}; c.Status(http.StatusUnauthorized) })
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
	l.mu.Lock()
	for _, v := range []string{"127.0.0.1"} {
		if vis, ok := l.visitors.Get(v); ok {
			require.Zero(t, vis.inFlight, "no in-flight slot is held")
		}
	}
	l.mu.Unlock()
}

// B-3: one visitor may have maxLoginInFlightPerVisitor logins in the handlers at
// once, saturated or not; the next is refused, others are unaffected, and the
// slot comes back when a request ends.
func TestLoginCeiling_PerVisitorInFlightCap(t *testing.T) {
	setTunnelConnectors(t, "")
	l := newLoginCeiling()
	gin.SetMode(gin.TestMode)
	hold := make(chan struct{})
	entered := make(chan struct{}, 64)
	r := gin.New()
	r.POST("/login", l.middleware(nil), func(c *gin.Context) {
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
	for range maxLoginInFlightPerVisitor {
		spawn("192.0.2.5:4000")
	}
	for range maxLoginInFlightPerVisitor {
		wait("a visitor's login under the cap was not admitted")
	}
	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	req.RemoteAddr = "192.0.2.5:4001"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusTooManyRequests, w.Code, "past the per-visitor in-flight cap")
	require.Equal(t, "5", w.Header().Get("Retry-After"))
	require.Equal(t, maxLoginInFlightPerVisitor, ceilingGlobalCount(l), "the refused request took no ceiling slot")

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
	for i := 0; i < loginGlobalCeiling; i++ {
		l.reserve("")
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	reached := 0
	r.POST("/login", l.middleware(nil), func(c *gin.Context) {
		reached++
		absauth.NoteLoginAccount(c, "acct-a")
		if absauth.RefuseSaturatedLogin(c, absauth.SoftThreshold, func(time.Duration) {}) {
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

// S-3: idle visitors are swept from the cold end, ones with a login in flight are
// kept, and the table never passes its bound.
func TestLoginCeiling_VisitorTableIsSweptAndBounded(t *testing.T) {
	l := newLoginCeiling()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	for i := 0; i < 50; i++ {
		leave, ok := l.enter(fmt.Sprintf("idle-%d", i))
		require.True(t, ok)
		leave()
	}
	_, ok := l.enter("busy") // never left: a login still in the handlers
	require.True(t, ok)
	require.Equal(t, 51, l.visitors.Len())
	now = now.Add(loginCeilingWindow + time.Second)
	leave, ok := l.enter("fresh")
	require.True(t, ok)
	leave()
	require.Equal(t, 2, l.visitors.Len(), "the 50 idle visitors are gone; the busy one and the fresh one remain")
	_, kept := l.visitors.Get("busy")
	require.True(t, kept, "a visitor with a login in flight is not swept")

	l2 := newLoginCeiling()
	for i := 0; i < maxLoginVisitors+25; i++ {
		leave, ok := l2.enter(fmt.Sprintf("v-%d", i))
		require.True(t, ok)
		leave()
	}
	require.Equal(t, maxLoginVisitors, l2.visitors.Len(), "bounded; the oldest entries are evicted one at a time")
	_, first := l2.visitors.Get("v-0")
	require.False(t, first)
	_, last := l2.visitors.Get(fmt.Sprintf("v-%d", maxLoginVisitors+24))
	require.True(t, last)
}

// N6: the exemption is read when a request arrives, so a loginCeilingExempt set
// after the ceiling was first built is still honoured.
func TestLoginCeiling_ExemptionIsReadLazily(t *testing.T) {
	setTunnelConnectors(t, "")
	s := &Server{}
	l := s.loginCeiling() // built first, as wire_handlers does
	s.loginCeilingExempt = func(*gin.Context) bool { return true }
	for i := 0; i < loginGlobalCeiling; i++ {
		l.reserve("")
	}
	code, saturated := http.StatusUnauthorized, true
	r := ceilingRouter(l, &code, &saturated)
	ceilingPost(r, "192.0.2.5:4000", nil)
	require.False(t, saturated, "an exemption installed after the ceiling was built applies")
	require.Same(t, l, s.loginCeiling(), "web and ABS logins share one ceiling")
	require.Same(t, s.loginThrottle(), s.loginThrottle(), "...and one failed-login throttle")
}
