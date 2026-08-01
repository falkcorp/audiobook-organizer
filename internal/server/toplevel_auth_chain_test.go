// file: internal/server/toplevel_auth_chain_test.go
// version: 2.0.0
// guid: 3e8b56d1-04af-42c7-9b18-5d7a2f0c6e94
// last-edited: 2026-08-01

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// boundUserKey mirrors the context key that middleware.CloudflareAccessAuth sets
// and middleware.RequireAuth early-outs on. The real constant is unexported in
// the middleware package; the contract it encodes — "an upstream stage may bind
// a user, and the auth guard must then admit without a session" — is locked
// independently by middleware/cfaccess_earlyout_test.go. These tests cover the
// other half: that /api/events actually HAS that upstream stage in its chain.
const boundUserKey = "user"

// stubCFMW stands in for the Cloudflare Access middleware: it binds a user when
// a verified assertion is present and otherwise passes through untouched
// (fail-open), exactly as the real middleware does.
func stubCFMW(c *gin.Context) {
	if c.GetHeader("Cf-Access-Jwt-Assertion") != "" {
		c.Set(boundUserKey, "cf-user")
	}
	c.Next()
}

// stubAuthGuard stands in for middleware.RequireAuth: admit if some upstream
// stage already bound a user, otherwise demand a session cookie and 401.
func stubAuthGuard(c *gin.Context) {
	if _, ok := c.Get(boundUserKey); ok {
		c.Next()
		return
	}
	if _, err := c.Cookie("session"); err != nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	c.Next()
}

func okHandler(c *gin.Context) { c.String(http.StatusOK, "stream") }

func serveEvents(t *testing.T, chain []gin.HandlerFunc, setHeader bool) int {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/events", chain...)

	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	if setHeader {
		req.Header.Set("Cf-Access-Jwt-Assertion", "verified-token")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code
}

// The regression this file exists for. A browser authenticated purely by
// Cloudflare Access has NO session cookie — identity is only in the assertion
// header — so /api/events must carry the CF middleware or it 401s forever and
// the UI shows a permanent "Connection lost" banner.
func TestBuildTopLevelAuthChain_CFAssertionAdmittedWithoutSessionCookie(t *testing.T) {
	chain := buildTopLevelAuthChain(stubCFMW, stubAuthGuard, okHandler)
	if got := serveEvents(t, chain, true); got != http.StatusOK {
		t.Fatalf("a verified Access assertion must be admitted without a session cookie; got %d, want %d", got, http.StatusOK)
	}
}

// Proves the above test actually detects the bug: drop cfMW from the chain (the
// pre-fix wiring) and the very same request 401s.
func TestBuildTopLevelAuthChain_WithoutCFMWTheAssertionIs401(t *testing.T) {
	chain := buildTopLevelAuthChain(nil, stubAuthGuard, okHandler)
	if got := serveEvents(t, chain, true); got != http.StatusUnauthorized {
		t.Fatalf("without cfMW an Access-only client must 401 (this is the bug being fixed); got %d, want %d", got, http.StatusUnauthorized)
	}
}

// cfMW is fail-open, not fail-closed: it must not admit a request that carries
// no assertion at all. Anonymous clients still get 401 (pen-test finding MED-2).
func TestBuildTopLevelAuthChain_AnonymousStill401(t *testing.T) {
	chain := buildTopLevelAuthChain(stubCFMW, stubAuthGuard, okHandler)
	if got := serveEvents(t, chain, false); got != http.StatusUnauthorized {
		t.Fatalf("anonymous clients must still be rejected; got %d, want %d", got, http.StatusUnauthorized)
	}
}

// When Cloudflare Access is unconfigured cfMW is nil, and a nil handler in a gin
// chain panics on dispatch. Assert it is omitted rather than appended.
func TestBuildTopLevelAuthChain_NilCFMWOmitted(t *testing.T) {
	if got := len(buildTopLevelAuthChain(nil, stubAuthGuard, okHandler)); got != 2 {
		t.Fatalf("nil cfMW must be omitted from the chain; got %d handlers, want 2", got)
	}
	if got := len(buildTopLevelAuthChain(stubCFMW, stubAuthGuard, okHandler)); got != 3 {
		t.Fatalf("a non-nil cfMW must be prepended; got %d handlers, want 3", got)
	}
}
