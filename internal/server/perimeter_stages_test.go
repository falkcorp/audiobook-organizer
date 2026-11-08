// file: internal/server/perimeter_stages_test.go
// version: 1.0.1
// guid: 5b1e7c3a-2d84-4f69-a0b7-8c6e3f1d9a42
// last-edited: 2026-11-08

package server

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// R1-17 moved HEAD's gin perimeter into core's stages 0-11. This file holds
// the helpers that serve a gin engine behind those stages as the kernel
// does, HEAD's perimeter frozen as the parity reference, and the tests of
// the stages on ABO's own routes.

// stagesOver serves engine as a legacy mount of its own routes behind core's
// stages, with books' stage 5 values (LegacyPerimeter), as the kernel serves
// books' engine; the mount serves the SPA, so an unrouted path reaches the
// engine's NoRoute. A route with no Perm needs AnyAuthenticated. The rate
// limit is off until the caller turns it on with SetPerimeter.
func stagesOver(t *testing.T, engine http.Handler, d httpapi.Deps, routes ...httpapi.LegacyRoute) (*httpapi.Router, http.Handler) {
	t.Helper()
	r := httpapi.NewRouter(httpapi.Options{}, d)
	claimed := map[string]bool{}
	var tops []string
	for i, lr := range routes {
		if top, _, _ := strings.Cut(strings.TrimPrefix(lr.GinPath, "/"), "/"); top != "api" && !slices.Contains(tops, top) {
			tops = append(tops, top)
		}
		if lr.Perm.IsZero() {
			routes[i].Perm = rbac.AnyAuthenticated
		}
		c := lr.GinPath
		if i := strings.IndexAny(c, ":*"); i >= 0 {
			c = c[:i] // the static subtree before the first parameter
		}
		for _, sub := range []string{"/api/v1/", "/api/v3/"} {
			if strings.HasPrefix(c, sub) {
				c = sub
			}
		}
		if !claimed[c] {
			claimed[c] = true
			require.NoError(t, r.Claim("books", c))
		}
	}
	csp, report, build := (&Server{}).LegacyPerimeter()
	require.NoError(t, r.MountLegacy(httpapi.LegacyMount{
		Owner: "books", Handler: engine, Auth: httpapi.LegacyAuthGin, ServesSPA: true,
		Routes: slices.Values(routes), TopSegments: tops,
		DefaultCSP: csp, CSPReport: report, BuildID: build,
	}))
	h, err := r.Build()
	require.NoError(t, err)
	return r, h
}

// headPerimeter is HEAD's gin perimeter in front of engine, frozen at the
// R1-17 base (server.go's router.Use chain; server_middleware.go's
// securityHeadersMiddleware and corsMiddleware) as the parity reference for
// core's stages 5 and 6. The Basic gate is still the engine's own. Not
// frozen: the access log and recovery (their output is not a response
// header), compression (TestCompressionExclusionsEqualHEAD compares HEAD's
// exclusion rule instead), tracing (stage 2 records what otelgin did, see
// the telemetry policy goldens) and the /api rate limiter (core's
// TestRateLimit_ClientBuckets).
func headPerimeter(engine http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headSetSecurityHeaders(w.Header(), r)
		origin, allowed := headSetCORSHeaders(w.Header(), r)
		if r.Method == http.MethodOptions {
			if origin != "" && !allowed {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		engine.ServeHTTP(w, r)
	})
}

// headSetSecurityHeaders is HEAD's setSecurityHeaders.
func headSetSecurityHeaders(h http.Header, r *http.Request) {
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "SAMEORIGIN")
	h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	h.Set("X-XSS-Protection", "0")
	h.Set("Content-Security-Policy", contentSecurityPolicy())
	h.Set("Reporting-Endpoints", cspReportGroup+"=\""+cspReportPath+"\"")
	h.Set("Permissions-Policy", permissionsPolicy)
	if r.TLS != nil || strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https") {
		h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
	}
}

// headSetCORSHeaders is HEAD's setCORSHeaders in release mode (the Vite
// origins it also allowed in gin's debug mode are not core's: core has no
// dev-mode origins, and the Vite dev server proxies the API same-origin).
func headSetCORSHeaders(h http.Header, r *http.Request) (origin string, allowed bool) {
	origin = strings.TrimSpace(r.Header.Get("Origin"))
	allowedOrigin := ""
	if origin != "" {
		if host := strings.TrimSpace(r.Host); host != "" && (origin == "http://"+host || origin == "https://"+host) {
			allowedOrigin = origin
		}
	}
	if allowedOrigin != "" {
		h.Set("Access-Control-Allow-Origin", allowedOrigin)
		h.Set("Vary", "Origin")
		h.Set("Access-Control-Allow-Credentials", "true")
		h.Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, Authorization, Cache-Control, X-Requested-With")
		h.Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE, PATCH")
	}
	return origin, allowedOrigin != ""
}

// headCompressionExcludedPaths and headCompressionExcludedRE are HEAD's
// compressionMiddleware exclusions (server.go, gin-contrib/gzip's
// WithExcludedPaths, a prefix match on URL.Path, and
// WithExcludedPathsRegexs).
var (
	headCompressionExcludedPaths = []string{"/api/events", "/metrics", "/api/v1/auth/accept-invite"}
	headCompressionExcludedRE    = regexp.MustCompile(`^/api/v1/operations/v2/[^/]+/logs/download$`)
)

func headCompressionExcluded(path string) bool {
	for _, p := range headCompressionExcludedPaths {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return headCompressionExcludedRE.MatchString(path)
}

// TestCompressionExclusionsEqualHEAD: on the real engine, ABS off and on,
// a manifest route is NoCompress exactly when HEAD's exclusion rule matched
// its path, and through core's stages each of HEAD's four exclusions is sent
// uncompressed to a gzip client while an ordinary JSON route is compressed
// once. Every text/event-stream response is never compressed, whatever its
// route (core's TestHandler_NoDoubleGzip), and HandleRaw streams are
// NoCompress by construction.
func TestCompressionExclusionsEqualHEAD(t *testing.T) {
	for _, abs := range absModes() {
		t.Run(absName(abs), func(t *testing.T) {
			f := legacyEngine(t, abs, false)
			manifest, err := f.srv.LegacyManifest()
			require.NoError(t, err)
			for _, lr := range manifest {
				want := headCompressionExcluded(legacyConcretePath(lr.GinPath))
				if lr.NoCompress != want {
					t.Errorf("%s %s: NoCompress = %v, HEAD excluded it: %v", lr.Method, lr.GinPath, lr.NoCompress, want)
				}
			}
		})
	}

	// Live, through the stages: HEAD's excluded paths and an ordinary one.
	engine := gin.New()
	big := strings.Repeat("Oracle relays the Bat-Signal. ", 200)
	h := func(c *gin.Context) { c.Data(http.StatusOK, "application/json", []byte(`{"x":"`+big+`"}`)) }
	engine.GET("/api/events", h)
	engine.GET("/metrics", h)
	engine.POST("/api/v1/auth/accept-invite", h)
	engine.GET("/api/v1/operations/v2/:id/logs/download", h)
	engine.GET("/api/v1/audiobooks", h)
	_, stack := stagesOver(t, engine, httpapi.Deps{},
		httpapi.LegacyRoute{Method: http.MethodGet, GinPath: "/api/events", NoCompress: true},
		httpapi.LegacyRoute{Method: http.MethodGet, GinPath: "/metrics", NoCompress: true},
		httpapi.LegacyRoute{Method: http.MethodPost, GinPath: "/api/v1/auth/accept-invite", Perm: rbac.Public, Public: true, NoCompress: true},
		httpapi.LegacyRoute{Method: http.MethodGet, GinPath: "/api/v1/operations/v2/:id/logs/download", NoCompress: true},
		httpapi.LegacyRoute{Method: http.MethodGet, GinPath: "/api/v1/audiobooks"},
	)
	for _, c := range []struct {
		method, path string
		gzip         bool
	}{
		{http.MethodGet, "/api/events", false},
		{http.MethodGet, "/metrics", false},
		{http.MethodPost, "/api/v1/auth/accept-invite", false},
		{http.MethodGet, "/api/v1/operations/v2/01JBATCAVE/logs/download", false},
		{http.MethodGet, "/api/v1/audiobooks", true},
	} {
		req := httptest.NewRequest(c.method, c.path, nil)
		req.Header.Set("Accept-Encoding", "gzip")
		w := httptest.NewRecorder()
		stack.ServeHTTP(w, req)
		if got := w.Header().Get("Content-Encoding") == "gzip"; got != c.gzip || isGzip(w.Body.Bytes()) != c.gzip {
			t.Errorf("%s %s: gzip = %v (body gzip %v), want %v", c.method, c.path, got, isGzip(w.Body.Bytes()), c.gzip)
		}
	}
}

// accessLog is an slog handler keeping every record's text form.
type accessLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (a *accessLog) Enabled(context.Context, slog.Level) bool { return true }
func (a *accessLog) Handle(ctx context.Context, r slog.Record) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slog.NewTextHandler(&a.buf, nil).Handle(ctx, r)
}
func (a *accessLog) WithAttrs([]slog.Attr) slog.Handler { return a }
func (a *accessLog) WithGroup(string) slog.Handler      { return a }

func (a *accessLog) String() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.buf.String()
}

// TestAccessLog_NoInviteToken: core's access log, which replaced gin's
// request log, never writes an invite token or a credential query value,
// for the invite routes, the SPA's invite page, an ABS ?token= request and
// an answer core writes itself (R1-15 review m6's case: core's group 404),
// while the handler still sees the real values. (The cases of HEAD's
// RedactingLogFormatter and RedactLogRequestPath tests.)
func TestAccessLog_NoInviteToken(t *testing.T) {
	const tok = "joker-card-invite-token"
	const cred = "abk_penguin-umbrella-key"
	engine := gin.New()
	var mu sync.Mutex
	var saw []string
	engine.Use(func(c *gin.Context) {
		mu.Lock()
		saw = append(saw, c.Request.URL.Path+"?"+c.Request.URL.RawQuery)
		mu.Unlock()
		c.Status(http.StatusOK)
	})
	ok := func(*gin.Context) {}
	engine.GET("/api/v1/auth/invite/:token", ok)
	engine.DELETE("/api/v1/users/invites/:id", ok)
	engine.POST("/api/v1/users/invites/:id/resend", ok)
	engine.GET("/api/v1/items", ok)
	engine.NoRoute(ok)
	logs := &accessLog{}
	_, stack := stagesOver(t, engine, httpapi.Deps{Log: slog.New(logs)},
		httpapi.LegacyRoute{Method: http.MethodGet, GinPath: "/api/v1/auth/invite/:token", Perm: rbac.Public, Public: true},
		httpapi.LegacyRoute{Method: http.MethodDelete, GinPath: "/api/v1/users/invites/:id"},
		httpapi.LegacyRoute{Method: http.MethodPost, GinPath: "/api/v1/users/invites/:id/resend"},
		httpapi.LegacyRoute{Method: http.MethodGet, GinPath: "/api/v1/items"},
	)
	for _, target := range []string{
		"/api/v1/auth/invite/" + tok,
		"/api/v1/auth/invite/" + tok + "?x=1",
		"/api/v1/users/invites/" + tok,
		"/api/v1/users/invites/" + tok + "/resend",
		"/api/v1/auth/invite/" + strings.ReplaceAll(tok, "-", "%2D"),
		"/api/v1/items?token=" + cred,
		"/api/v1/items?api_key=" + cred + "&batman=1",
		// HEAD's RedactLogRequestPath cases: a credential after a "?" that
		// arrived as %3F inside the path, and the SPA's invite page.
		"/api/v1/items%3Ftoken=" + cred,
		"/api/v1/items%3Fwidth=4&token=" + cred + "?w=1",
		"/invite/" + tok,
		"/invite/%3F" + tok,
		// Core answers this itself (its group's problem 404): gin never runs.
		"/api/core/v1/riddler?token=" + cred,
	} {
		method := http.MethodGet
		switch {
		case strings.HasPrefix(target, "/api/v1/users/invites/") && strings.HasSuffix(target, "/resend"):
			method = http.MethodPost
		case strings.HasPrefix(target, "/api/v1/users/invites/"):
			method = http.MethodDelete
		}
		w := httptest.NewRecorder()
		stack.ServeHTTP(w, httptest.NewRequest(method, target, nil))
		if routed := !strings.Contains(target, "%3F") && !strings.HasPrefix(target, "/invite/") && !strings.HasPrefix(target, "/api/core/"); routed {
			// The unrouted targets answer core's or gin's 404; only their
			// access line matters here.
			require.Equal(t, http.StatusOK, w.Code, target)
		}
	}
	out := logs.String()
	if n := strings.Count(out, `msg="http access"`); n != 12 {
		t.Fatalf("access records = %d, want 12:\n%s", n, out)
	}
	for _, secret := range []string{tok, "joker%2Dcard", cred} {
		if strings.Contains(out, secret) {
			t.Errorf("the access log carries %q:\n%s", secret, out)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.ContainsFunc(saw, func(s string) bool { return strings.Contains(s, tok) }) ||
		!slices.ContainsFunc(saw, func(s string) bool { return strings.Contains(s, cred) }) {
		t.Fatalf("the handler did not see the real values: %v", saw)
	}
}
