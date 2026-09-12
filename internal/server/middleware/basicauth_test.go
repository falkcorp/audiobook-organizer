// file: internal/server/middleware/basicauth_test.go
// version: 1.1.0
// guid: b2c3d4e5-f6a7-8b9c-0d1e-2f3a4b5c6d7e
// last-edited: 2026-09-12

package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/gin-gonic/gin"
)

func setupBasicAuthRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(BasicAuth())
	r.GET("/api/v1/health", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})
	r.GET("/api/v1/audiobooks", func(c *gin.Context) {
		c.String(http.StatusOK, "books")
	})
	r.GET("/assets/main.js", func(c *gin.Context) {
		c.String(http.StatusOK, "js")
	})
	return r
}

func TestBasicAuth_Disabled(t *testing.T) {
	config.AppConfig.BasicAuthEnabled = false

	r := setupBasicAuthRouter()
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/audiobooks", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 when basic auth disabled, got %d", w.Code)
	}
}

func TestBasicAuth_NoCredentials(t *testing.T) {
	config.AppConfig.BasicAuthEnabled = true
	config.AppConfig.BasicAuthUsername = "admin"
	config.AppConfig.BasicAuthPassword = "secret"

	r := setupBasicAuthRouter()
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/audiobooks", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without credentials, got %d", w.Code)
	}
	if w.Header().Get("WWW-Authenticate") == "" {
		t.Error("expected WWW-Authenticate header")
	}
}

func TestBasicAuth_WrongCredentials(t *testing.T) {
	config.AppConfig.BasicAuthEnabled = true
	config.AppConfig.BasicAuthUsername = "admin"
	config.AppConfig.BasicAuthPassword = "secret"

	r := setupBasicAuthRouter()
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/audiobooks", nil)
	req.SetBasicAuth("admin", "wrong")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 with wrong password, got %d", w.Code)
	}
}

func TestBasicAuth_CorrectCredentials(t *testing.T) {
	config.AppConfig.BasicAuthEnabled = true
	config.AppConfig.BasicAuthUsername = "admin"
	config.AppConfig.BasicAuthPassword = "secret"

	r := setupBasicAuthRouter()
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/audiobooks", nil)
	req.SetBasicAuth("admin", "secret")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 with correct credentials, got %d", w.Code)
	}
}

func TestBasicAuth_HealthExempt(t *testing.T) {
	config.AppConfig.BasicAuthEnabled = true
	config.AppConfig.BasicAuthUsername = "admin"
	config.AppConfig.BasicAuthPassword = "secret"

	r := setupBasicAuthRouter()
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/health", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for health endpoint without auth, got %d", w.Code)
	}
}

func TestBasicAuth_StaticAssetsExempt(t *testing.T) {
	config.AppConfig.BasicAuthEnabled = true
	config.AppConfig.BasicAuthUsername = "admin"
	config.AppConfig.BasicAuthPassword = "secret"

	r := setupBasicAuthRouter()
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/assets/main.js", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for static asset without auth, got %d", w.Code)
	}
}

// enableBasicAuth switches Basic Auth on for one test and restores the previous
// global values afterwards.
func enableBasicAuth(t *testing.T) {
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

// setupABSBasicAuthRouter mirrors production's shape: BasicAuth global, one
// token-gated ABS route, the auth-free ABS routes, and a plain app API route.
func setupABSBasicAuthRouter(resolver *ABSIdentityResolver) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(BasicAuth())
	ok := func(c *gin.Context) { c.String(http.StatusOK, "ok") }
	r.GET("/api/me", ABSRequireAuth(resolver), ok)
	r.GET("/ping", ok)
	r.GET("/status", ok)
	r.POST("/login", ok)
	r.GET("/auth/openid", ok)
	r.GET("/api/v1/audiobooks", ok)
	return r
}

func serveBasic(r *gin.Engine, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func basicChallenged(w *httptest.ResponseRecorder) bool {
	return w.Code == http.StatusUnauthorized &&
		strings.HasPrefix(w.Header().Get("WWW-Authenticate"), "Basic ")
}

// The exemption keys on this name; if it ever resolved to "" the exemption would
// silently never fire (fail closed, but ABS would be locked out again).
func TestBasicAuth_ABSRequireAuthHandlerNameResolved(t *testing.T) {
	if !strings.Contains(absRequireAuthHandlerName, "ABSRequireAuth") {
		t.Fatalf("absRequireAuthHandlerName = %q, want the ABSRequireAuth closure name", absRequireAuthHandlerName)
	}
}

// TestBasicAuth_ABSPathsExempt: a token-gated ABS route with neither Basic Auth
// nor an ABS credential is passed through by BasicAuth and refused by ABS token
// auth (401 JSON, no Basic challenge) — exempt from one gate, not from both.
func TestBasicAuth_ABSPathsExempt(t *testing.T) {
	enableBasicAuth(t)
	r := setupABSBasicAuthRouter(nil)
	w := serveBasic(r, http.MethodGet, "/api/me", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/me with no credentials = %d, want 401 from ABS token auth", w.Code)
	}
	if basicChallenged(w) {
		t.Fatal("GET /api/me was challenged by Basic Auth; the ABS exemption did not apply")
	}
	if !strings.Contains(w.Body.String(), `"error"`) {
		t.Fatalf("expected the ABS JSON auth error body, got %q", w.Body.String())
	}
}

// TestBasicAuth_ABSRouteValidTokenNoBasic: a valid ABS bearer token alone, with
// no Basic credentials, reaches the token-gated route.
func TestBasicAuth_ABSRouteValidTokenNoBasic(t *testing.T) {
	h := newABSHarness(t, "jwt", nil)
	h.store.addUser(activeUser("u1", "owner"))
	h.store.addSession(liveABSSession("s1", "u1"))
	token := h.mintAccess(t, "u1", "s1")

	enableBasicAuth(t)
	r := setupABSBasicAuthRouter(h.resolver)
	w := serveBasic(r, http.MethodGet, "/api/me", map[string]string{"Authorization": "Bearer " + token})
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/me with valid ABS token and no Basic Auth = %d, want 200: %s", w.Code, w.Body.String())
	}
}

// TestBasicAuth_UnauthenticatedABSRoutesStillEnforced: ABS routes that carry no
// ABSRequireAuth have Basic Auth as their only gate and must stay challenged.
func TestBasicAuth_UnauthenticatedABSRoutesStillEnforced(t *testing.T) {
	enableBasicAuth(t)
	r := setupABSBasicAuthRouter(nil)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/ping"},
		{http.MethodGet, "/status"},
		{http.MethodPost, "/login"},
		{http.MethodGet, "/auth/openid"},
	} {
		if w := serveBasic(r, tc.method, tc.path, nil); !basicChallenged(w) {
			t.Errorf("%s %s = %d, want 401 Basic challenge", tc.method, tc.path, w.Code)
		}
	}
}

// TestBasicAuth_NonABSPathStillEnforced: the exemption does not leak to the app
// API, and Basic credentials still open it.
func TestBasicAuth_NonABSPathStillEnforced(t *testing.T) {
	enableBasicAuth(t)
	r := setupABSBasicAuthRouter(nil)
	if w := serveBasic(r, http.MethodGet, "/api/v1/audiobooks", nil); !basicChallenged(w) {
		t.Fatalf("GET /api/v1/audiobooks without Basic Auth = %d, want 401 Basic challenge", w.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks", nil)
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/audiobooks with Basic Auth = %d, want 200", w.Code)
	}
}

// TestBasicAuth_TraversalDoesNotReachExemption: paths that textually start with
// a token-gated ABS route but do not match it stay behind Basic Auth, with or
// without an ABS token.
func TestBasicAuth_TraversalDoesNotReachExemption(t *testing.T) {
	enableBasicAuth(t)
	r := setupABSBasicAuthRouter(nil)
	for _, path := range []string{
		"/api/me/../v1/audiobooks",
		"/api/me/%2e%2e/v1/audiobooks",
		"/api/me/..%2fv1/audiobooks",
		"/api/me%2f..%2fv1/audiobooks",
		"/api/meany",
		"/api/me/extra",
	} {
		for _, hdrs := range []map[string]string{nil, {"Authorization": "Bearer not-a-basic-credential"}} {
			if w := serveBasic(r, http.MethodGet, path, hdrs); !basicChallenged(w) {
				t.Errorf("GET %s (bearer=%v) = %d, want 401 Basic challenge", path, hdrs != nil, w.Code)
			}
		}
	}
}
