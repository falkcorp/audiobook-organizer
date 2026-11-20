// file: core/kernel/ui_test.go
// version: 0.1.0
// guid: 4c8e1a73-9d25-4f60-b3e7-2a5d0f9c6b18
// last-edited: 2026-11-20

package kernel

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/ui"
)

// shellBundle is a shell build's dist: the three preset metas and one asset.
func shellBundle(t *testing.T) ui.Bundle {
	t.Helper()
	index := `<!doctype html><html><head>` +
		`<meta name="mediacore-build" content="b4tc4v3-1767225600">` +
		`<meta name="mediacore-app" content="gotham">` +
		`<meta name="mediacore-theme" content="gotham.shell.themeMode dark">` +
		`</head><body><div id="root"></div></body></html>`
	b, err := ui.FromFS(fstest.MapFS{
		"index.html":          {Data: []byte(index)},
		"assets/index-Bat.js": {Data: []byte("1")},
	}, ".")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestUIBundle_OwnsRootInP8: a non-zero Config.UI makes ui.Handler the "/"
// owner: an SPA path is index.html with the shell policy and PageMiddleware,
// an asset is immutable, a probe variant is a problem 404, X-App-Build is the
// bundle's build ID on every response, and the route table records the owner
// as SPA/Public/RateExempt.
func TestUIBundle_OwnsRootInP8(t *testing.T) {
	c := testConfig(t, mod("batcave", nil))
	c.UI = shellBundle(t)
	var pages int
	c.UIOptions = ui.AppOptions{PageMiddleware: func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { pages++; next.ServeHTTP(w, r) })
	}}
	k := mustNew(t, c)
	serve := func(p string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		k.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		return rec
	}
	rec := serve("/batcave/gadgets")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `content="gotham"`) {
		t.Fatalf("GET /batcave/gadgets = %d %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" || !strings.HasPrefix(rec.Header().Get("Content-Security-Policy"), "script-src 'self'") {
		t.Errorf("index headers = %v", rec.Header())
	}
	if pages != 1 {
		t.Errorf("PageMiddleware ran %d times for one page load", pages)
	}
	if got := serve("/assets/index-Bat.js").Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("asset Cache-Control = %q", got)
	}
	if rec := serve("/HEALTH/"); rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "<html") {
		t.Errorf("GET /HEALTH/ = %d %q", rec.Code, rec.Body.String())
	}
	for _, p := range []string{"/", "/assets/gone.js", "/api/core/v1/nope"} {
		if got := serve(p).Header().Get("X-App-Build"); got != "b4tc4v3-1767225600" {
			t.Errorf("GET %s X-App-Build = %q, want the bundle's build ID", p, got)
		}
	}
	var spa int
	for ri := range k.Routes() {
		if ri.Kind == httpapi.RouteKindSPA {
			spa++
		}
	}
	if spa != 1 {
		t.Fatalf("%d SPA routes, want 1", spa)
	}
}

// TestUIBundle_ZeroMountsNothing: the zero Bundle leaves "/" unowned, as
// before the shell work: an SPA path is a 404 and X-App-Build is "dev".
func TestUIBundle_ZeroMountsNothing(t *testing.T) {
	k := mustNew(t, testConfig(t, mod("batcave", nil)))
	rec := httptest.NewRecorder()
	k.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/batcave/gadgets", nil))
	if rec.Code != http.StatusNotFound || rec.Header().Get("X-App-Build") != "dev" {
		t.Fatalf("GET /batcave/gadgets = %d, X-App-Build %q", rec.Code, rec.Header().Get("X-App-Build"))
	}
}

// TestUIBundle_WithServesSPAMountIsP7Problem: a non-zero UI and a ServesSPA
// legacy mount both claim "/": boot fails with ProblemSPAOwner (FE-1).
func TestUIBundle_WithServesSPAMountIsP7Problem(t *testing.T) {
	c := testConfig(t, mod("batcave", func(r *module.Registrar) error {
		r.MountLegacy(httpapi.LegacyMount{Handler: http.NotFoundHandler(), Auth: httpapi.LegacyAuthCore, ServesSPA: true})
		return nil
	}))
	c.UI = shellBundle(t)
	k, err := New(t.Context(), c)
	if k != nil || !hasKind(err, module.ProblemSPAOwner) {
		t.Fatalf("New = %v, %v; want ProblemSPAOwner", k, err)
	}
}
