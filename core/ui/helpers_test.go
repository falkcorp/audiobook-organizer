// file: core/ui/helpers_test.go
// version: 0.1.0
// guid: 2f9e7c41-5a0b-4d36-8e1c-b4d7a3f60e92
// last-edited: 2026-11-20

package ui

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

// themeScript is the fixture's one inline script, the shape of the preset's
// theme boot script.
const themeScript = `(function(){try{var m=localStorage.getItem("batcave.shell.themeMode");document.documentElement.setAttribute("data-mui-color-scheme",m==="light"||m==="dark"?m:"dark")}catch(e){}})();`

// indexHTML is a shell build's index.html: the three preset metas, the theme
// boot script and the entry module.
func indexHTML(build, app, theme string) string {
	s := "<!doctype html>\n<html><head>\n<meta charset=\"utf-8\">\n"
	if build != "" {
		s += `<meta name="mediacore-build" content="` + build + "\">\n"
	}
	if app != "" {
		s += `<meta content="` + app + `" name="mediacore-app">` + "\n"
	}
	if theme != "" {
		s += `<meta name='mediacore-theme' content='` + theme + "'>\n"
	}
	s += "<script>" + themeScript + "</script>\n"
	s += `<script type="module" crossorigin src="/assets/index-Bat1.js"></script>` + "\n"
	return s + "</head><body><div id=\"root\"></div></body></html>\n"
}

// distFS is a built dist directory under web/dist.
func distFS() fstest.MapFS {
	return fstest.MapFS{
		"web/dist/index.html":           {Data: []byte(indexHTML("a1b2c3d-1767225600", "batcave", "batcave.shell.themeMode dark"))},
		"web/dist/assets/index-Bat1.js": {Data: []byte("console.log('Wayne Manor');")},
		"web/dist/assets/mui-Rob1.css":  {Data: []byte("body{}")},
		"web/dist/favicon.svg":          {Data: []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"/>")},
		"web/dist/robots.txt":           {Data: []byte("User-agent: *\n")},
	}
}

// mustBundle is FromFS over distFS.
func mustBundle(t testing.TB) Bundle {
	t.Helper()
	b, err := FromFS(distFS(), "web/dist")
	if err != nil {
		t.Fatalf("FromFS: %v", err)
	}
	return b
}

// scriptHash is the CSP hash source of s.
func scriptHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

// do serves one request.
func do(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

// probes is a NonSPA predicate over ABO-shaped segments, built by the real
// httpapi router predicate in TestHandler_NonSPASegments; this stand-in is
// for tests that need any predicate.
func probes(r *http.Request) bool {
	switch r.URL.Path {
	case "/api/batcave", "/health":
		return true
	}
	return false
}
