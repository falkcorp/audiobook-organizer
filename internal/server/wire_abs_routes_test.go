// file: internal/server/wire_abs_routes_test.go
// version: 1.8.0
// guid: 3ea1d764-95c8-4b02-8f31-6d70a5be2c49
// last-edited: 2026-08-12

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
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

// An ABS endpoint we do NOT implement must 404, not 301 into the app API.
//
// The /api/* -> /api/v1/* compatibility redirect used to catch these: a client
// probing /api/collections was sent to /api/v1/collections, met a different JSON
// shape or a 401, and concluded the feature was present and broken rather than
// absent. Contract §2.4 -- a misapplied non-404 silently disables a working client
// feature, and any non-2xx flips AudioBooth's connection indicator.
func TestUnimplementedABSNamespacesAre404NotRedirect(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()

	// Derived from absUnimplementedNamespaces rather than restated. The hardcoded copy
	// this replaces went stale the moment /api/users moved to absAppAPICollisions, and
	// failed CI asserting a 404 the fix had deliberately removed — a duplicated list is
	// a second source of truth that only ever disagrees with the first.
	require.NotEmpty(t, absUnimplementedNamespaces, "the unimplemented list must not be silently emptied")

	var paths []string
	for _, ns := range absUnimplementedNamespaces {
		paths = append(paths, ns, ns+"/abc123")
	}

	for _, path := range paths {
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))

		require.NotEqual(t, http.StatusMovedPermanently, w.Code,
			"%s must not 301 into the /api/v1 app API — the client would meet a foreign shape", path)
		require.Empty(t, w.Header().Get("Location"),
			"%s must not send the client anywhere", path)
		require.Equal(t, http.StatusNotFound, w.Code,
			"%s is unimplemented on the ABS surface and must say so honestly", path)
	}
}

// The redirect must still work for everything that is NOT an ABS namespace,
// otherwise this fix has quietly broken /api/* compatibility for the app API.
func TestNonABSPathsStillRedirect(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()

	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/audiobooks", nil))
	require.Equal(t, http.StatusMovedPermanently, w.Code,
		"/api/audiobooks is app-API surface and must keep redirecting to /api/v1")
	require.Equal(t, "/api/v1/audiobooks", w.Header().Get("Location"))
}

// TestCollidingNamespacesStillRedirect is the regression guard for the bug #2332
// introduced: authors, series and playlists are ABS namespaces we don't implement,
// but they are ALSO app-API namespaces we do. 404ing them to be honest about the ABS
// surface destroyed 46 working app routes' unversioned form on every deployment,
// because the redirect middleware is not gated on ABSAPIEnabled.
//
// The earlier version of this suite missed it by checking only /api/audiobooks — a
// path with no ABS meaning at all, so it could never have caught a collision. The
// test has to be driven by the collision list itself to be worth anything.
func TestCollidingNamespacesStillRedirect(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()

	require.NotEmpty(t, absAppAPICollisions, "the collision list must not be silently emptied")

	for _, base := range absAppAPICollisions {
		for _, path := range []string{base, base + "/123"} {
			w := httptest.NewRecorder()
			s.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))

			require.Equal(t, http.StatusMovedPermanently, w.Code,
				"%s has a live /api/v1 twin — 404ing it breaks a working app route to make an "+
					"unimplemented ABS one honest, on every deployment including ABS-disabled ones", path)
			require.Equal(t, strings.Replace(path, "/api/", "/api/v1/", 1), w.Header().Get("Location"))
		}
	}
}

// A namespace must never appear in both lists: one says 404, the other says redirect,
// and absReservedPath consults only the first — so a duplicate would silently win as a
// 404 while the collision list claimed otherwise.
func TestNamespaceListsAreDisjoint(t *testing.T) {
	for _, u := range absUnimplementedNamespaces {
		for _, c := range absAppAPICollisions {
			require.NotEqual(t, u, c, "%s is in both absUnimplementedNamespaces and absAppAPICollisions", u)
		}
	}
}

// TestUnimplementedNamespacesHaveNoAppAPITwin is the mechanical form of the check that
// has now failed BY HAND twice — in #2332 (authors/series/playlists) and again in #2333,
// whose fix missed /api/users.
//
// Both misses came from searching the SOURCE for route paths. gin builds a route's path
// from its RouterGroup at registration time, so a grouped route
// (`users := protected.Group("/users")`; `users.GET("", ...)`) has no literal "/users"
// path anywhere in the file — grep cannot see it, no matter how the pattern is written.
// This codebase registers both directly and through groups, so the source is simply not
// a sound oracle for "does /api/v1/<ns> exist".
//
// router.Routes() is: it returns the flattened, fully-resolved table gin will actually
// match against. Walking it means nobody has to remember the right grep again.
func TestUnimplementedNamespacesHaveNoAppAPITwin(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()

	routes := s.router.Routes()

	// Guard against a VACUOUS pass. If this server were wired without the app API,
	// every assertion below would succeed by finding nothing, and the test would read
	// as protection while providing none.
	var versioned int
	for _, r := range routes {
		if strings.HasPrefix(r.Path, "/api/v1/") {
			versioned++
		}
	}
	require.Greater(t, versioned, 50,
		"the route table has only %d /api/v1 routes — the app API is not wired into this "+
			"test server, so the twin checks below would pass by finding nothing", versioned)

	for _, ns := range absUnimplementedNamespaces {
		twin := strings.Replace(ns, "/api/", "/api/v1/", 1)
		var found []string
		for _, r := range routes {
			if r.Path == twin || strings.HasPrefix(r.Path, twin+"/") {
				found = append(found, r.Method+" "+r.Path)
			}
		}
		require.Empty(t, found,
			"%s is listed as unimplemented, so absReservedPath makes it 404 — but the app API "+
				"serves %d live route(s) under %s: %s.\nThat 404 applies on EVERY deployment, "+
				"including ABS-disabled ones, because the redirect middleware is not gated on "+
				"ABSAPIEnabled. Move %s to absAppAPICollisions.",
			ns, len(found), twin, strings.Join(found, ", "), ns)
	}
}

// TestCollisionNamespacesAreStillColliding is the inverse guard, and it exists so the
// collision list cannot rot into a lie. Each entry claims "we must not 404 this, because
// a live app-API twin exists" — if that twin is ever deleted or renamed, the entry stops
// describing reality and the honest-404 we gave up buying is no longer bought.
func TestCollisionNamespacesAreStillColliding(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()

	routes := s.router.Routes()

	for _, ns := range absAppAPICollisions {
		twin := strings.Replace(ns, "/api/", "/api/v1/", 1)
		var found int
		for _, r := range routes {
			if r.Path == twin || strings.HasPrefix(r.Path, twin+"/") {
				found++
			}
		}
		require.Greater(t, found, 0,
			"%s is excluded from the honest-404 list because %s was supposed to be live, but the "+
				"router has no route under it any more. Either the app route moved (update this "+
				"list) or it is gone (move %s to absUnimplementedNamespaces so it 404s honestly).",
			ns, twin, ns)
	}
}

// TestABSReservedPath_CoversEveryPathTheRealClientRequested closes the one direction
// the tests above structurally cannot.
//
// Every other guard on this surface derives from absRouteList() — that is, from what we
// IMPLEMENT. An endpoint the client actually requests but we have not built yet is
// absent from that list, so nothing checks it. If its prefix is not reserved it 301s
// into the app API and answers 200 in the app's shape, which is the failure mode this
// whole file exists to prevent: implemented-looking and broken.
//
// The golden fixtures are captures of a real Audiobookshelf answering the real target
// clients, so request.path is the only record in this repo of what the client ASKS FOR,
// independent of what we chose to build. Deriving from them means a newly captured
// endpoint is covered the moment its fixture lands, with no list to remember to update.
//
// This passes today — all 28 captured paths are reserved. It is a ratchet, not a bug
// report: it fails the day someone captures a fixture for an endpoint outside the four
// reserved sub-trees and does not reserve it.
func TestABSReservedPath_CoversEveryPathTheRealClientRequested(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "testdata", "abs-fixtures", "*.json"))
	require.NoError(t, err)
	require.Greater(t, len(files), 20,
		"found only %d fixtures — the capture directory moved or the glob is wrong, and this "+
			"test would pass by checking nothing", len(files))

	checked := 0
	for _, file := range files {
		raw, err := os.ReadFile(file)
		require.NoError(t, err)

		var fx struct {
			Request struct {
				Method string `json:"method"`
				Path   string `json:"path"`
			} `json:"request"`
		}
		require.NoError(t, json.Unmarshal(raw, &fx), "%s", file)
		require.NotEmpty(t, fx.Request.Path, "%s: fixture records no request path", file)

		reqPath := fx.Request.Path
		if q := strings.IndexByte(reqPath, '?'); q >= 0 {
			reqPath = reqPath[:q]
		}

		// Captures also include the login/probe endpoints and raw audio fetches, which
		// live outside /api entirely and are never seen by the /api → /api/v1 redirect.
		if !strings.HasPrefix(reqPath, "/api/") {
			continue
		}
		checked++

		require.True(t, absReservedPath(reqPath),
			"%s: the real client requested %s %s, but absReservedPath does not cover it, so the "+
				"/api → /api/v1 redirect swallows the call. Add its sub-tree to "+
				"absReservedPathPrefixes (or the exact path to absReservedPaths).",
			filepath.Base(file), fx.Request.Method, reqPath)
	}

	require.Greater(t, checked, 15,
		"only %d of %d fixtures had an /api path — the fixture shape changed and this test is "+
			"no longer reading what it thinks it is", checked, len(files))
}
