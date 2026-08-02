// file: internal/server/wire_abs_routes_test.go
// version: 1.3.0
// guid: 3ea1d764-95c8-4b02-8f31-6d70a5be2c49
// last-edited: 2026-08-02

package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestABSReservedPath_CoversTheABSSurfaceUnderAPI guards the /api/* → /api/v1/*
// compatibility redirect. The ABS protocol is UNVERSIONED, so GET /api/me is a real
// ABS endpoint; if the redirect applied to it, the client would 301 into /api/v1/me
// and get a completely different shape. The route would look implemented and behave
// broken, which is the worst failure mode for a compatibility surface.
func TestABSReservedPath_CoversTheABSSurfaceUnderAPI(t *testing.T) {
	reserved := []string{
		"/api/me",
		"/api/me/sessions",
		"/api/me/sessions/01JABCDEF",
	}
	for _, p := range reserved {
		if !absReservedPath(p) {
			t.Errorf("%s must be reserved for the ABS surface, or the /api/v1 redirect will swallow it", p)
		}
	}
}

// TestABSReservedPath_CoversEVERYRegisteredUnversionedRoute is the guard that scales:
// it derives its input from absRouteList() rather than a hand-kept list, so adding an
// ABS route without adding it to absReservedPath fails HERE instead of on a phone.
//
// That failure mode is the reason this test exists and is worth the reflection: a
// missing exclusion does not 404. The route registers, the startup log lists it, and a
// curl follows the 301 into /api/v1 and prints a 200 — so the endpoint looks
// implemented while the client silently receives the app API's shape or a 401.
func TestABSReservedPath_CoversEVERYRegisteredUnversionedRoute(t *testing.T) {
	// Concrete stand-ins for gin's wildcards: absReservedPath matches real request
	// paths, not route patterns.
	placeholders := map[string]string{
		":libraryId": "b5e3a5b2-a76e-471f-b18b-915e4716d053",
		":id":        "68929fc9-e296-4d25-b3aa-1c2930efd00d",
		":ino":       "01JFILEIDABCDEFGHIJKLMNOP",
		":index":     "1",
		// The bookmark delete surface addresses a bookmark by its TIME value, which
		// arrives as a bare path segment (real ABS keys a bookmark by (item, time),
		// not by an opaque id). Integer form on purpose: that is what AudioBooth
		// sends here even when it round-trips the same value as a Double elsewhere.
		":time": "100",
		// The year-stats path parameter. Any 4-digit year; the handler ignores it.
		":year": "2026",
	}

	checked := 0
	for _, entry := range absRouteList() {
		// Entries may carry a trailing " (note)" for the startup log.
		if paren := strings.Index(entry, " ("); paren >= 0 {
			entry = entry[:paren]
		}
		parts := strings.SplitN(entry, " ", 2)
		if len(parts) != 2 {
			t.Fatalf("malformed absRouteList entry %q", entry)
		}
		path := parts[1]
		// Only unversioned /api/ paths pass through the redirect middleware. Root paths
		// (/login, /ping, /status, /logout, /auth/refresh) and /public/* never do.
		if !strings.HasPrefix(path, "/api/") {
			continue
		}
		for pattern, value := range placeholders {
			path = strings.ReplaceAll(path, pattern, value)
		}
		if strings.Contains(path, ":") {
			t.Fatalf("route %q has a wildcard with no test placeholder — add one", path)
		}
		checked++
		if !absReservedPath(path) {
			t.Errorf("REGISTERED BUT NOT RESERVED: %s. Add its prefix to absReservedPathPrefixes "+
				"(or the exact path to absReservedPaths), or it will 301 into /api/v1 and look "+
				"implemented while behaving broken.", path)
		}
	}
	if checked == 0 {
		t.Fatal("no /api/ routes were checked — absRouteList or the parser above is wrong")
	}
}

// TestABSReservedPath_DoesNotCaptureAppRoutes: the exclusion must be narrow. Capturing
// an app route would stop it being redirected to its /api/v1 equivalent.
func TestABSReservedPath_DoesNotCaptureAppRoutes(t *testing.T) {
	notReserved := []string{
		"/api/audiobooks",
		"/api/health",
		"/api/events",
		"/api/metrics",
		"/api/v1/me",
		"/api/v1/audiobooks",
		"/api/members",   // shares the "/api/me" prefix but is a different path
		"/api/mediafile", // ditto
		"/api/",
		"/login",
		"/ping",
	}
	for _, p := range notReserved {
		if absReservedPath(p) {
			t.Errorf("%s must NOT be treated as an ABS path", p)
		}
	}
}

// TestABSRedirectExclusion_LeavesABSPathsAlone reproduces the real middleware chain in
// miniature and asserts that an ABS path is served rather than 301'd, while an app path
// still gets its /api/v1 redirect.
func TestABSRedirectExclusion_LeavesABSPathsAlone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// The same predicate as setupRoutes.
	r.Use(func(c *gin.Context) {
		path := c.Request.URL.Path
		if strings.HasPrefix(path, "/api/") &&
			!strings.HasPrefix(path, "/api/v1/") &&
			!strings.HasPrefix(path, "/api/health") &&
			!strings.HasPrefix(path, "/api/events") &&
			!strings.HasPrefix(path, "/api/metrics") &&
			!absReservedPath(path) {
			c.Redirect(http.StatusMovedPermanently, strings.Replace(path, "/api/", "/api/v1/", 1))
			c.Abort()
			return
		}
		c.Next()
	})
	r.GET("/api/me", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"abs": true}) })
	r.GET("/api/me/sessions", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"abs": true}) })
	r.GET("/api/audiobooks", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"app": true}) })

	for _, p := range []string{"/api/me", "/api/me/sessions"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
		if w.Code != http.StatusOK {
			t.Errorf("%s got %d, want 200 — the ABS surface must not be redirected to /api/v1", p, w.Code)
		}
		if !strings.Contains(w.Body.String(), `"abs":true`) {
			t.Errorf("%s did not reach the ABS handler: %s", p, w.Body.String())
		}
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/audiobooks", nil))
	if w.Code != http.StatusMovedPermanently {
		t.Fatalf("/api/audiobooks got %d — the app-route redirect must still work", w.Code)
	}
	if got := w.Header().Get("Location"); got != "/api/v1/audiobooks" {
		t.Fatalf("unexpected redirect target %q", got)
	}
}

// TestWireABSRoutes_DisabledByDefaultRegistersNothing pins the feature flag. An
// existing deployment that pulls this change must expose no new route at all.
func TestWireABSRoutes_DisabledByDefaultRegistersNothing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := &Server{router: gin.New()}
	s.wireABSRoutes() // config.AppConfig.ABSAPIEnabled is false by default

	for _, ri := range s.router.Routes() {
		for _, abs := range absRouteList() {
			if abs == ri.Method+" "+ri.Path {
				t.Fatalf("%s was registered with ABS_API_ENABLED off", abs)
			}
		}
	}
	if n := len(s.router.Routes()); n != 0 {
		t.Fatalf("expected no routes at all, got %d", n)
	}
}
