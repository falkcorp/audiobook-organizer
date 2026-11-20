// file: core/ui/handler_test.go
// version: 0.1.0
// guid: 6d1a8f3e-0c27-4b95-a4e8-3f7b2d9c1e50
// last-edited: 2026-11-20

package ui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/falkcorp/mediacore/archtest"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/config/configtest"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// wantPolicy is 07 §7.6.2's default policy for the fixture bundle with the
// given connect-src hosts, written out by hand, not by buildPolicy.
func wantPolicy(hosts ...string) string {
	connect := "connect-src 'self'"
	for _, h := range hosts {
		connect += " https://" + h
	}
	return "script-src 'self' " + scriptHash(themeScript) + "; " + connect + "; " +
		"default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob: https:; " +
		"font-src 'self' data:; media-src 'self' blob:; object-src 'none'; base-uri 'none'; " +
		"form-action 'self'; frame-ancestors 'self'; report-uri /api/core/v1/ui/csp-report; report-to csp"
}

// TestHandler_AssetsImmutable: a hashed asset under /assets/ is served with
// its bytes, its MIME type and the immutable cache rule, GET and HEAD.
func TestHandler_AssetsImmutable(t *testing.T) {
	h := Handler(mustBundle(t), Options{})
	for _, m := range []string{http.MethodGet, http.MethodHead} {
		rec := do(h, m, "/assets/index-Bat1.js")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s asset = %d", m, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
			t.Errorf("%s asset Cache-Control = %q", m, got)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
			t.Errorf("%s asset Content-Type = %q", m, ct)
		}
		if m == http.MethodGet && rec.Body.String() != "console.log('Wayne Manor');" {
			t.Errorf("asset body = %q", rec.Body.String())
		}
		if rec.Header().Get("Content-Security-Policy") != "" {
			t.Errorf("%s asset carries the page CSP; stage 5 owns non-page responses", m)
		}
	}
}

// TestHandler_IndexNoStore: "/", any SPA path and GET /index.html itself are
// index.html with no-store and the full CSP, through PageMiddleware (FE4-11).
func TestHandler_IndexNoStore(t *testing.T) {
	var wrapped atomic.Int32
	mw := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			wrapped.Add(1)
			w.Header().Set("X-Oracle", "seen")
			next.ServeHTTP(w, r)
		})
	}
	h := Handler(mustBundle(t), Options{PageMiddleware: mw})
	index := indexHTML("a1b2c3d-1767225600", "batcave", "batcave.shell.themeMode dark")
	for i, p := range []string{"/", "/index.html", "/hello/greeting", "/settings/hello", "/users"} {
		rec := do(h, http.MethodGet, p)
		if rec.Code != http.StatusOK || rec.Body.String() != index {
			t.Fatalf("GET %s = %d %q", p, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("GET %s Cache-Control = %q, want no-store", p, got)
		}
		if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
			t.Errorf("GET %s Content-Type = %q", p, got)
		}
		if got := rec.Header().Get("Content-Security-Policy"); got != wantPolicy() {
			t.Errorf("GET %s CSP =\n%q\nwant\n%q", p, got, wantPolicy())
		}
		if got := rec.Header().Get("Reporting-Endpoints"); got != `csp="/api/core/v1/ui/csp-report"` {
			t.Errorf("GET %s Reporting-Endpoints = %q", p, got)
		}
		if rec.Header().Get("X-Oracle") != "seen" || int(wrapped.Load()) != i+1 {
			t.Errorf("GET %s did not run through PageMiddleware", p)
		}
	}
	rec := do(h, http.MethodHead, "/")
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("HEAD / = %d, %d body bytes, %q", rec.Code, rec.Body.Len(), rec.Header().Get("Cache-Control"))
	}
}

// TestHandler_CSPPolicyVerbatim: the default policy is §7.6.2's text, with
// the inline-script hash, the configured hosts and ConnectSrcFrom's hosts
// (normalised, malformed ones dropped, duplicates once), then the app's
// extra directives; a directive that would start another one is dropped.
func TestHandler_CSPPolicyVerbatim(t *testing.T) {
	h := Handler(mustBundle(t), Options{})
	if got := do(h, http.MethodGet, "/").Header().Get("Content-Security-Policy"); got != wantPolicy() {
		t.Fatalf("default CSP =\n%q\nwant\n%q", got, wantPolicy())
	}
	h = Handler(mustBundle(t), Options{
		ConnectSrcFrom: func() []string {
			return []string{"HTTPS://Gotham.CloudflareAccess.com/", "gotham.cloudflareaccess.com", "evil.example.test; script-src *", "*.wayne.test", "localhost", ""}
		},
		ExtraDirectives: []string{"worker-src 'self' blob:", "upgrade-insecure-requests; script-src *", "  ", "frame-src 'none'"}})
	want := wantPolicy("gotham.cloudflareaccess.com") + "; worker-src 'self' blob:; frame-src 'none'"
	if got := do(h, http.MethodGet, "/").Header().Get("Content-Security-Policy"); got != want {
		t.Fatalf("extended CSP =\n%q\nwant\n%q", got, want)
	}
}

// uiConfig is a config store holding the core.ui section, loaded and
// attached to an in-memory override store, so Apply changes it live.
func uiConfig(t *testing.T) (*config.Store, *config.Handle[Settings]) {
	t.Helper()
	ctx := context.Background()
	st, err := config.NewStore(config.Options{AppID: "batcave"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close(context.Background()) })
	h, err := config.Register(st.Registry(), config.Define("core.ui", DefaultSettings()))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Load(ctx); err != nil {
		t.Fatal(err)
	}
	mem := configtest.NewMemory()
	if _, err := st.Attach(ctx, mem, configtest.NewCommitter(mem)); err != nil {
		t.Fatal(err)
	}
	return st, h
}

// TestHandler_CSPHotSwap: core.ui.csp.connect_src and report_only change the
// next index response with no new Handler, and so does ConnectSrcFrom's
// answer; the handler is not rebuilt.
func TestHandler_CSPHotSwap(t *testing.T) {
	st, cfg := uiConfig(t)
	var team atomic.Pointer[string]
	h := Handler(mustBundle(t), Options{Config: cfg, ConnectSrcFrom: func() []string {
		if p := team.Load(); p != nil {
			return []string{*p}
		}
		return nil
	}})
	if got := do(h, http.MethodGet, "/").Header().Get("Content-Security-Policy"); got != wantPolicy() {
		t.Fatalf("before = %q", got)
	}
	actor := config.Actor{Subject: "alfred", Interactive: true, Can: func(string) bool { return true }}
	apply := func(path, raw string) {
		t.Helper()
		if _, err := st.Apply(context.Background(), config.Patch{Set: map[config.Path]json.RawMessage{config.Path(path): json.RawMessage(raw)}}, actor); err != nil {
			t.Fatalf("Apply %s: %v", path, err)
		}
	}
	apply("core.ui.csp.connect_src", `["oracle.example.test"]`)
	rec := do(h, http.MethodGet, "/batcomputer")
	if got := rec.Header().Get("Content-Security-Policy"); got != wantPolicy("oracle.example.test") {
		t.Fatalf("after connect_src = %q", got)
	}
	team.Store(new("gotham.cloudflareaccess.com"))
	if got := do(h, http.MethodGet, "/").Header().Get("Content-Security-Policy"); got != wantPolicy("oracle.example.test", "gotham.cloudflareaccess.com") {
		t.Fatalf("after ConnectSrcFrom = %q", got)
	}
	apply("core.ui.csp.report_only", `true`)
	rec = do(h, http.MethodGet, "/")
	if rec.Header().Get("Content-Security-Policy") != "" {
		t.Errorf("report_only still sends the enforcing header")
	}
	if got := rec.Header().Get("Content-Security-Policy-Report-Only"); got != wantPolicy("oracle.example.test", "gotham.cloudflareaccess.com") {
		t.Fatalf("report-only header = %q", got)
	}
	apply("core.ui.csp.report_only", `false`)
	if got := do(h, http.MethodGet, "/").Header().Get("Content-Security-Policy"); got == "" {
		t.Fatal("report_only false did not restore the enforcing header")
	}
}

// problemOf decodes a problem+json body, failing on any other content type.
func problemOf(t *testing.T, rec *httptest.ResponseRecorder) httpapi.Problem {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json (body %q)", ct, rec.Body.String())
	}
	var p httpapi.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestHandler_API404JSON: an unmatched /api/ path the NonSPA predicate
// claims answers 404 problem+json, never index.html.
func TestHandler_API404JSON(t *testing.T) {
	h := Handler(mustBundle(t), Options{NonSPA: probes})
	rec := do(h, http.MethodGet, "/api/batcave")
	if rec.Code != http.StatusNotFound || problemOf(t, rec).Code != "not_found" {
		t.Fatalf("GET /api/batcave = %d %q", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "<html") {
		t.Fatal("an API path got HTML")
	}
}

// TestHandler_Placeholder: the zero Bundle serves the API-only page at every
// SPA path, no-store, with its own locked-down policy; NonSPA paths and
// non-GET methods keep their problem answers.
func TestHandler_Placeholder(t *testing.T) {
	var b Bundle
	if !b.IsZero() || b.BuildID() != "" || b.AppID() != "" {
		t.Fatalf("zero Bundle: IsZero %v, BuildID %q, AppID %q", b.IsZero(), b.BuildID(), b.AppID())
	}
	if mustBundle(t).IsZero() {
		t.Fatal("a loaded Bundle reports IsZero")
	}
	h := Handler(b, Options{NonSPA: probes})
	for _, p := range []string{"/", "/hello/greeting", "/assets/index-Bat1.js"} {
		rec := do(h, http.MethodGet, p)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "no embedded web interface") {
			t.Fatalf("GET %s = %d %q", p, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Content-Security-Policy") != placeholderCSP {
			t.Errorf("GET %s headers = %v", p, rec.Header())
		}
	}
	if rec := do(h, http.MethodGet, "/health"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /health on the placeholder = %d", rec.Code)
	}
	if rec := do(h, http.MethodPost, "/"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST / on the placeholder = %d", rec.Code)
	}
}

// TestHandler_ServesRootPublicFile: a dist root file such as /favicon.svg is
// served with no-cache (FE3-14), before the extension rule.
func TestHandler_ServesRootPublicFile(t *testing.T) {
	h := Handler(mustBundle(t), Options{})
	for _, p := range []string{"/favicon.svg", "/robots.txt"} {
		rec := do(h, http.MethodGet, p)
		if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-cache" {
			t.Fatalf("GET %s = %d %q", p, rec.Code, rec.Header().Get("Cache-Control"))
		}
		if strings.Contains(rec.Body.String(), "<html") {
			t.Fatalf("GET %s got index.html", p)
		}
	}
	// A directory of the bundle is not a file: /assets is an SPA path, and
	// /assets/ with no file is a missing asset.
	if rec := do(h, http.MethodGet, "/assets/"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /assets/ = %d, want 404", rec.Code)
	}
}

// nonSPARouter is the kernel's predicate source: an httpapi Router with the
// infra probes and an ABO-shaped legacy mount's top segments, one shared.
func nonSPARouter(t *testing.T) func(*http.Request) bool {
	t.Helper()
	r := httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{})
	for _, p := range []string{"GET /livez", "GET /readyz", "GET /metrics", "GET /health"} {
		r.Infra(p, rbac.Public, http.NotFoundHandler())
	}
	if err := r.MountLegacy(httpapi.LegacyMount{Owner: "batcomputer", Handler: http.NotFoundHandler(), Auth: httpapi.LegacyAuthCore,
		TopSegments: []string{"socket.io", "ping", "login"}, SPAShared: []string{"login"}}); err != nil {
		t.Fatal(err)
	}
	return r.NonSPA()
}

// TestHandler_NonSPASegments: with the real Router.NonSPA() predicate, every
// case, double-slash and trailing-slash variant of a probe or server segment
// is a 404 problem, never HTML (FE2-6); a shared segment (login) and an
// ordinary page are the SPA.
func TestHandler_NonSPASegments(t *testing.T) {
	h := Handler(mustBundle(t), Options{NonSPA: nonSPARouter(t)})
	for _, p := range []string{"/health", "/health/", "/HEALTH", "/Health/x", "/metrics/", "/livez/", "/READYZ", "//livez",
		"/socket.io/", "/ping", "/api/core/v1/nope", "/API/v1/x"} {
		rec := do(h, http.MethodGet, p)
		if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "<html") {
			t.Errorf("GET %s = %d %q, want a 404 problem", p, rec.Code, rec.Body.String())
			continue
		}
		problemOf(t, rec)
	}
	for _, p := range []string{"/login", "/login/temp", "/activity", "/healthy-alfred"} {
		if rec := do(h, http.MethodGet, p); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<html") {
			t.Errorf("GET %s = %d, want index.html", p, rec.Code)
		}
	}
}

// TestHandler_NonGETRefused: every method but GET and HEAD is a 405 problem
// with Allow, whatever the path, and never runs PageMiddleware.
func TestHandler_NonGETRefused(t *testing.T) {
	var ran atomic.Bool
	mw := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { ran.Store(true); next.ServeHTTP(w, r) })
	}
	h := Handler(mustBundle(t), Options{PageMiddleware: mw})
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions, "BATARANG"} {
		for _, p := range []string{"/", "/assets/index-Bat1.js", "/hello/greeting"} {
			rec := do(h, m, p)
			if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
				t.Errorf("%s %s = %d Allow %q", m, p, rec.Code, rec.Header().Get("Allow"))
				continue
			}
			if problemOf(t, rec).Code != "method_not_allowed" {
				t.Errorf("%s %s code = %q", m, p, problemOf(t, rec).Code)
			}
		}
	}
	if ran.Load() {
		t.Fatal("PageMiddleware ran for a refused method")
	}
}

// TestHandler_MissingAssetIs404: a missing hashed chunk, and any unmatched
// path whose last segment has an extension, is a 404, never index.html, so
// a stale tab's chunk load fails cleanly instead of with a MIME error.
func TestHandler_MissingAssetIs404(t *testing.T) {
	h := Handler(mustBundle(t), Options{})
	for _, p := range []string{"/assets/index-Joker9.js", "/assets/old/chunk.css", "/hello/greeting.js", "/sourcemap.map", "/favicon.ico"} {
		rec := do(h, http.MethodGet, p)
		if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "<html") {
			t.Errorf("GET %s = %d %q", p, rec.Code, rec.Body.String())
		}
	}
}

// TestHandler_PageMiddlewareOnlyOnIndex: PageMiddleware runs for page loads
// only, never for an asset, a public file, a missing asset or a NonSPA path.
func TestHandler_PageMiddlewareOnlyOnIndex(t *testing.T) {
	var seen []string
	mw := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen = append(seen, r.URL.Path)
			next.ServeHTTP(w, r)
		})
	}
	h := Handler(mustBundle(t), Options{NonSPA: probes, PageMiddleware: mw})
	for _, p := range []string{"/assets/index-Bat1.js", "/favicon.svg", "/assets/gone.js", "/api/batcave", "/x.png", "/", "/index.html", "/system/status"} {
		do(h, http.MethodGet, p)
	}
	want := []string{"/", "/index.html", "/system/status"}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Fatalf("PageMiddleware saw %v, want %v", seen, want)
	}
	// A middleware that answers itself (CF recovery's redirect) replaces the
	// page, and the handler adds nothing after it.
	redirect := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/cdn-cgi/access/login", http.StatusFound)
		})
	}
	rec := do(Handler(mustBundle(t), Options{PageMiddleware: redirect}), http.MethodGet, "/")
	if rec.Code != http.StatusFound || strings.Contains(rec.Body.String(), "mediacore-build") {
		t.Fatalf("redirecting middleware: %d %q", rec.Code, rec.Body.String())
	}
}

// TestBundle_MissingMeta: FromFS refuses a bundle without index.html or
// without any of the three preset metas, including a legacy-style index.html
// with the build meta only (FE7-7) and one without the app meta (FE8-7), and
// refuses malformed values; a complete bundle loads with its IDs and the
// inline-script hash.
func TestBundle_MissingMeta(t *testing.T) {
	const build, app, theme = "a1b2c3d-1767225600", "batcave", "batcave.shell.themeMode dark"
	cases := []struct {
		name  string
		index string
	}{
		{"no build meta", indexHTML("", app, theme)},
		{"no app meta", indexHTML(build, "", theme)},
		{"no theme meta (legacy pre-shell build)", indexHTML(build, "", "")},
		{"build meta only", indexHTML(build, "", "")},
		{"no theme meta", indexHTML(build, app, "")},
		{"dev build ID", indexHTML("dev", app, theme)},
		{"DEV build ID", indexHTML("DEV", app, theme)},
		{"build ID with a quote-breaking character", indexHTML("a1b2 c3", app, theme)},
		{"app ID not 03 §3.6 shaped", indexHTML(build, "Batcave", theme)},
		{"theme mode not light or dark", indexHTML(build, app, "batcave.shell.themeMode dim")},
		{"theme key with a space", indexHTML(build, app, "bat cave dark")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := FromFS(fstest.MapFS{"dist/index.html": {Data: []byte(c.index)}}, "dist")
			if !errors.Is(err, ErrInvalidBundle) {
				t.Fatalf("FromFS = %v, want ErrInvalidBundle", err)
			}
		})
	}
	if _, err := FromFS(fstest.MapFS{"dist/assets/a.js": {Data: []byte("x")}}, "dist"); !errors.Is(err, ErrInvalidBundle) {
		t.Fatalf("no index.html: %v", err)
	}
	if _, err := FromFS(nil, "dist"); !errors.Is(err, ErrInvalidBundle) {
		t.Fatalf("nil fs: %v", err)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("MustFromFS did not panic on an invalid bundle")
			}
		}()
		MustFromFS(fstest.MapFS{}, "dist")
	}()
	b := MustFromFS(distFS(), "web/dist")
	if b.BuildID() != build || b.AppID() != app || len(b.scripts) != 1 || b.scripts[0] != scriptHash(themeScript) {
		t.Fatalf("Bundle = build %q app %q scripts %v", b.BuildID(), b.AppID(), b.scripts)
	}
}

// TestHandler_OutsideRouteChain (FE-25): mounted with Router.SPA, the handler
// is the "/" owner recorded as RouteInfo{Kind: SPA, Perm: Public, RateClass:
// RateExempt}, the archtest permission gate accepts it, and a cold load far
// past the rate limit is never refused, while a rate-limited API route of the
// same router is.
func TestHandler_OutsideRouteChain(t *testing.T) {
	r := httpapi.NewRouter(httpapi.Options{Limits: httpapi.LimitOptions{RateLimitEnabled: true, RateLimitPerMinute: 1}}, httpapi.Deps{})
	httpapi.HandleRaw(r.CoreGroup(), httpapi.Route{Method: http.MethodGet, Path: "/batsignal", Name: "getBatsignal",
		Perm: rbac.Public, RateClass: httpapi.RateDefault}, httpapi.RawDoc{},
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	r.AllowPublic("core", "getBatsignal")
	if err := r.SPA(Handler(mustBundle(t), Options{NonSPA: r.NonSPA()}), httpapi.SPAOptions{BasicExemptAssets: BasicExemptAssets}); err != nil {
		t.Fatal(err)
	}
	h, err := r.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var spa []httpapi.RouteInfo
	for ri := range r.Routes() {
		if ri.Kind == httpapi.RouteKindSPA {
			spa = append(spa, ri)
		}
	}
	if len(spa) != 1 || spa[0].Perm.Kind() != rbac.KindPublic || spa[0].RateClass != httpapi.RateExempt {
		t.Fatalf("SPA routes = %+v", spa)
	}
	archtest.RoutesHavePermissions(t, r.Routes())
	serve := func(p string) int {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		req.RemoteAddr = "192.0.2.7:4000"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	for i := range 60 {
		for _, p := range []string{"/", "/assets/index-Bat1.js", "/hello/greeting"} {
			if code := serve(p); code != http.StatusOK {
				t.Fatalf("cold load %d of %s = %d", i, p, code)
			}
		}
	}
	limited := 0
	for range 30 {
		if serve("/api/core/v1/batsignal") == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited == 0 {
		t.Fatal("the API route was never rate limited, so the SPA result proves nothing")
	}
}

// TestBasicExemptAssets_EqualsHEAD: the predicate accepts exactly HEAD's set
// (basicauth.go:40-46 @748fd787a), judged against a hand copy of that rule
// over a table of paths on both sides of each clause.
func TestBasicExemptAssets_EqualsHEAD(t *testing.T) {
	head := func(p string) bool {
		return strings.HasPrefix(p, "/assets/") || p == "/favicon.ico" ||
			strings.HasSuffix(p, ".js") || strings.HasSuffix(p, ".css") || strings.HasSuffix(p, ".png") ||
			strings.HasSuffix(p, ".svg") || strings.HasSuffix(p, ".woff2")
	}
	paths := []string{
		"/assets/", "/assets/index-Bat1.js", "/assets", "/ASSETS/x", "/x/assets/y",
		"/favicon.ico", "/favicon.icon", "/sub/favicon.ico",
		"/a.js", "/a.jsx", "/a.JS", "/a.css", "/a.png", "/a.svg", "/a.woff2", "/a.woff", "/a.gif", "/a.ico",
		"/", "/login", "/api/v1/audiobooks", "/api/health", "/batcave.js", "/robots.txt", "",
	}
	accepted := 0
	for _, p := range paths {
		if got, want := BasicExemptAssets(p), head(p); got != want {
			t.Errorf("BasicExemptAssets(%q) = %v, HEAD %v", p, got, want)
		}
		if head(p) {
			accepted++
		}
	}
	if accepted != 9 {
		t.Fatalf("the table holds %d HEAD-exempt paths, want 9: it no longer covers both sides", accepted)
	}
}

// TestHandler_CSPModesThroughRouterChain: through a built httpapi Router (the
// real stage-5 chain, 05-http-api §4.2), index.html carries exactly the
// handler's policy in each mode: enforcing gives one Content-Security-Policy
// with the shell policy; report_only gives the policy as
// Content-Security-Policy-Report-Only and no enforcing header at all, so
// stage 5's default-src 'none' never lands beside it (07 §7.6.2).
func TestHandler_CSPModesThroughRouterChain(t *testing.T) {
	st, cfg := uiConfig(t)
	r := httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{})
	if err := r.SPA(Handler(mustBundle(t), Options{Config: cfg, NonSPA: r.NonSPA()}), httpapi.SPAOptions{BasicExemptAssets: BasicExemptAssets}); err != nil {
		t.Fatal(err)
	}
	h, err := r.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	page := func() http.Header {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/batcomputer", nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<html") {
			t.Fatalf("GET /batcomputer = %d", rec.Code)
		}
		return rec.Header()
	}
	hdr := page()
	if got := hdr.Values("Content-Security-Policy"); len(got) != 1 || got[0] != wantPolicy() {
		t.Errorf("enforcing: Content-Security-Policy = %q, want only the shell policy", got)
	}
	if got := hdr.Values("Content-Security-Policy-Report-Only"); len(got) != 0 {
		t.Errorf("enforcing: Content-Security-Policy-Report-Only = %q, want none", got)
	}
	actor := config.Actor{Subject: "alfred", Interactive: true, Can: func(string) bool { return true }}
	if _, err := st.Apply(context.Background(), config.Patch{Set: map[config.Path]json.RawMessage{"core.ui.csp.report_only": json.RawMessage(`true`)}}, actor); err != nil {
		t.Fatal(err)
	}
	hdr = page()
	if got := hdr.Values("Content-Security-Policy-Report-Only"); len(got) != 1 || got[0] != wantPolicy() {
		t.Errorf("report_only: Content-Security-Policy-Report-Only = %q, want only the shell policy", got)
	}
	if got := hdr.Values("Content-Security-Policy"); len(got) != 0 {
		t.Errorf("report_only: Content-Security-Policy = %q, want none (an enforcing policy beside the report-only one)", got)
	}
}

// TestHandler_NonSPAOnCleanedPath: the predicate judges the path the handler
// serves, so a dot-segment spelling of a probe is a 404 problem, not
// index.html.
func TestHandler_NonSPAOnCleanedPath(t *testing.T) {
	h := Handler(mustBundle(t), Options{NonSPA: nonSPARouter(t)})
	for _, p := range []string{"/x/../health", "/batcave/../livez", "/./metrics", "/a/b/../../HEALTH/"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.URL.Path = p // httptest.NewRequest would not keep the dot segments
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "<html") {
			t.Errorf("GET %s = %d %q, want a 404 problem", p, rec.Code, rec.Body.String())
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.URL.Path = "/health/../activity"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<html") {
		t.Errorf("GET /health/../activity = %d, want index.html (it cleans to a page)", rec.Code)
	}
}

// TestHandler_DefaultSettingsPolicyCached: with no Handle, two page loads
// reuse one computed policy (one defaults pointer) instead of rebuilding it.
func TestHandler_DefaultSettingsPolicyCached(t *testing.T) {
	h := Handler(mustBundle(t), Options{}).(*handler)
	do(h, http.MethodGet, "/")
	first := h.policy.Load()
	do(h, http.MethodGet, "/batcave")
	if first == nil || h.policy.Load() != first || first.settings != h.def {
		t.Error("the policy was rebuilt for the second page load")
	}
}
