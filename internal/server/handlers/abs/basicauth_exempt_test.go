// file: internal/server/handlers/abs/basicauth_exempt_test.go
// version: 1.0.0
// guid: 1a51d292-2d49-43bb-b5d1-afc53fd96f5b
// last-edited: 2026-09-12

package abs_test

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// absUnauthenticatedRoutes is every ABS route Register mounts WITHOUT
// ABSRequireAuth. With Basic Auth enabled these must stay behind it, because it
// is their only gate. Changing this set is a security decision, not a test fix.
var absUnauthenticatedRoutes = map[string]bool{
	"GET /ping":                            true,
	"GET /status":                          true,
	"POST /login":                          true,
	"POST /auth/refresh":                   true,
	"GET /auth/openid":                     true,
	"GET /auth/openid/callback":            true,
	"GET /api/items/:id/cover":             true,
	"HEAD /api/items/:id/cover":            true,
	"GET /public/session/:id/track/:index": true,
}

// enableBasicAuthForTest turns the global Basic Auth flag on and restores the
// previous values afterwards, so no later test in this package inherits it.
func enableBasicAuthForTest(t *testing.T) {
	t.Helper()
	prevEnabled := config.AppConfig.BasicAuthEnabled
	prevUser := config.AppConfig.BasicAuthUsername
	prevPass := config.AppConfig.BasicAuthPassword
	t.Cleanup(func() {
		config.AppConfig.BasicAuthEnabled = prevEnabled
		config.AppConfig.BasicAuthUsername = prevUser
		config.AppConfig.BasicAuthPassword = prevPass
	})
	config.AppConfig.BasicAuthEnabled = true
	config.AppConfig.BasicAuthUsername = "admin"
	config.AppConfig.BasicAuthPassword = "secret"
}

// newBasicAuthGatedABS returns an engine shaped like production for this
// question: the global BasicAuth middleware first, then the real ABS Register
// table. The bearer token is minted on the harness's own (ungated) router
// before Basic Auth is switched on.
func newBasicAuthGatedABS(t *testing.T) (*gin.Engine, string) {
	t.Helper()
	h, _, token := newBrowseHarness(t)
	enableBasicAuthForTest(t)
	gated := gin.New()
	gated.Use(servermiddleware.BasicAuth())
	h.handler.Register(gated)
	return gated, token
}

func serveGated(r *gin.Engine, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func isBasicChallenge(w *httptest.ResponseRecorder) bool {
	return strings.HasPrefix(w.Header().Get("WWW-Authenticate"), "Basic ")
}

// TestBasicAuth_ExemptsExactlyTheTokenGatedABSRoutes walks every route the real
// Register table mounts (engine.Routes(), not a hand-kept list) and sends each
// one with no Basic Auth AND no ABS credential. Every route must refuse with
// 401. A Basic challenge means Basic Auth refused it (route not exempt); a 401
// without one means Basic Auth let it through and ABS token auth refused it.
// The Basic-challenged set must equal absUnauthenticatedRoutes exactly, so a
// future ABS route registered without ABSRequireAuth fails here loudly.
func TestBasicAuth_ExemptsExactlyTheTokenGatedABSRoutes(t *testing.T) {
	gated, _ := newBasicAuthGatedABS(t)

	placeholders := strings.NewReplacer(
		":libraryId", "b5e3a5b2-a76e-471f-b18b-915e4716d053",
		":id", "68929fc9-e296-4d25-b3aa-1c2930efd00d",
		":ino", "01JFILEIDABCDEFGHIJKLMNOP",
		":index", "1",
		":time", "100",
		":year", "2026",
		":bookId", "68929fc9-e296-4d25-b3aa-1c2930efd00d",
	)

	challenged := map[string]bool{}
	var exempt []string
	for _, route := range gated.Routes() {
		key := route.Method + " " + route.Path
		path := placeholders.Replace(route.Path)
		if strings.Contains(path, ":") || strings.Contains(path, "*") {
			t.Fatalf("%s: unsubstituted wildcard in %q; add it to the replacer", key, path)
		}
		w := serveGated(gated, route.Method, path, nil)
		if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
			t.Errorf("%s (%s) with no credentials = %d, want 401/403", key, path, w.Code)
			continue
		}
		if isBasicChallenge(w) {
			challenged[key] = true
			if !absUnauthenticatedRoutes[key] {
				t.Errorf("%s carries ABSRequireAuth but Basic Auth still challenged it (exemption missing)", key)
			}
			continue
		}
		if absUnauthenticatedRoutes[key] {
			t.Errorf("%s has no ABS token auth but Basic Auth let it through without credentials", key)
		}
		exempt = append(exempt, key)
	}
	for key := range absUnauthenticatedRoutes {
		if !challenged[key] {
			t.Errorf("expected unauthenticated ABS route %s to be registered and Basic-challenged", key)
		}
	}
	if len(exempt) == 0 {
		t.Fatal("no ABS route was exempted from Basic Auth; the exemption is not taking effect")
	}
	sort.Strings(exempt)
	t.Logf("%d routes exempt from Basic Auth and refused by ABS token auth; %d routes still Basic-challenged",
		len(exempt), len(challenged))
}

// TestBasicAuth_ABSRouteWithValidTokenPassesWithoutBasic is the positive half:
// a real ABS client holding a bearer token and sending no Basic credentials
// reaches token-gated ABS routes.
func TestBasicAuth_ABSRouteWithValidTokenPassesWithoutBasic(t *testing.T) {
	gated, token := newBasicAuthGatedABS(t)
	for _, path := range []string{"/api/me", "/api/libraries"} {
		w := serveGated(gated, http.MethodGet, path, bearer(token))
		if w.Code != http.StatusOK {
			t.Errorf("GET %s with valid ABS token, no Basic Auth = %d, want 200: %s", path, w.Code, w.Body.String())
		}
	}
}

// TestBasicAuth_ABSTokenDoesNotOpenNonExemptRoutes: a valid ABS token is not a
// Basic Auth credential, so it opens nothing outside the token-gated set:
// traversal spellings, unmatched paths and the auth-free ABS routes all stay
// Basic-challenged.
func TestBasicAuth_ABSTokenDoesNotOpenNonExemptRoutes(t *testing.T) {
	gated, token := newBasicAuthGatedABS(t)
	cases := []struct{ method, path string }{
		{http.MethodGet, "/api/me/../v1/config"},
		{http.MethodGet, "/api/me/%2e%2e/%2e%2e/api/v1/config"},
		{http.MethodGet, "/api/me/..%2f..%2fapi/v1/config"},
		{http.MethodGet, "/api/libraries/../../api/v1/audiobooks"},
		{http.MethodGet, "/api/meany"},
		{http.MethodGet, "/api/v1/config"},
		{http.MethodGet, "/api/items/68929fc9-e296-4d25-b3aa-1c2930efd00d/cover"},
		{http.MethodPost, "/login"},
		{http.MethodGet, "/ping"},
	}
	for _, tc := range cases {
		for _, hdrs := range []map[string]string{nil, bearer(token)} {
			w := serveGated(gated, tc.method, tc.path, hdrs)
			if w.Code != http.StatusUnauthorized || !isBasicChallenge(w) {
				t.Errorf("%s %s (token=%v) = %d challenge=%q, want 401 Basic challenge",
					tc.method, tc.path, hdrs != nil, w.Code, w.Header().Get("WWW-Authenticate"))
			}
		}
	}
}
