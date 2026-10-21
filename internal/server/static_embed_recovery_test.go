//go:build embed_frontend

// file: internal/server/static_embed_recovery_test.go
// version: 1.0.0
// guid: c4a9e2d7-83f1-4b56-a0d8-5e7b1f9c3a62
// last-edited: 2026-10-20

package server

import (
	"embed"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/oauth"
)

// The REAL embedded-build wiring: setupStaticFiles(cfRecoveryMW) installs the
// recovery on the NoRoute chain, in front of the SPA fallback, and registered
// routes never reach it.
func TestStaticEmbed_NoRouteChainRunsCFRecovery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	SetEmbeddedFS(embed.FS{})
	s := &Server{router: gin.New()}
	s.router.GET("/registered/page", func(c *gin.Context) { c.String(http.StatusOK, "registered") })
	s.setupStaticFiles(newCFRecoveryMiddleware(recoveryTestVerifier{}), nil)

	do := func(path, assertion string, nav bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if nav {
			req.Header.Set("Sec-Fetch-Mode", "navigate")
			req.Header.Set("Sec-Fetch-Dest", "document")
		}
		req.Header.Set(oauth.CFAccessHeader, assertion)
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, req)
		return w
	}

	for _, path := range []string{"/library", "/login", "/"} {
		w := do(path, "svc", true)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `http-equiv="refresh"`) || !strings.Contains(w.Body.String(), "cfreset=1") {
			t.Errorf("%s: service-token navigation must bounce, got %d %q", path, w.Code, w.Body.String())
		}
	}
	if w := do("/library", "svc", false); strings.Contains(w.Body.String(), "http-equiv") {
		t.Errorf("a fetch (no Sec-Fetch) must not be bounced, got %q", w.Body.String())
	}
	if w := do("/library", "person", true); strings.Contains(w.Body.String(), "http-equiv") {
		t.Errorf("a person navigation must not be bounced, got %q", w.Body.String())
	}
	if w := do("/registered/page", "svc", true); w.Body.String() != "registered" {
		t.Errorf("registered route must never be bounced, got %q", w.Body.String())
	}
	if w := do("/api/v1/nope", "svc", true); strings.Contains(w.Body.String(), "http-equiv") || w.Code != http.StatusNotFound {
		t.Errorf("unmatched API path must 404 untouched, got %d %q", w.Code, w.Body.String())
	}
}
