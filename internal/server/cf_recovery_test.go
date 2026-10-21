// file: internal/server/cf_recovery_test.go
// version: 1.0.0
// guid: b2d8f4a6-1c73-4e95-9a0b-6f3e7d2c8a15
// last-edited: 2026-10-20

package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/oauth"
)

func TestIsCFRecoverySPAPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/", true},
		{"/library", true},
		{"/library/123", true},
		{"/settings/users", true},
		{"/authors", true}, // must not be captured by "/auth/"
		{"/api/v1/auth/me", false},
		{"/api", false},
		{"/api/events", false},
		{"/auth/openid", false},
		{"/auth/openid/browser", false},
		{"/auth/openid/callback", false},
		{"/auth/temp-login", false},
		{"/socket.io/", false},
		{"/ping", false},
		{"/status", false},
		{"/login", false},
		{"/logout", false},
		{"/public/session/x/track/1", false},
		{"/abs/x", false},
		{"/audiobookshelf/x", false},
		{"/health", false},
		{"/metrics", false},
		{"/cdn-cgi/access/logout", false},
		{"/assets/index-abc123.js", false},
		{"/favicon.ico", false},
		{"/sw.js", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := isCFRecoverySPAPath(tc.path); got != tc.want {
			t.Errorf("isCFRecoverySPAPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

type recoveryTestVerifier struct{}

func (recoveryTestVerifier) Verify(_ context.Context, raw string) (*oauth.IdentityClaims, error) {
	switch raw {
	case "svc":
		return nil, &oauth.NonIdentityAssertionError{CommonName: "abcdef0123456789.access"}
	case "person":
		return &oauth.IdentityClaims{Provider: oauth.ProviderCFAccess, Subject: "p", Email: "o@example.com", EmailVerified: true}, nil
	}
	return nil, errors.New("invalid")
}

// The middleware is installed on the NoRoute chain exactly as setupStaticFiles
// does, so it sees only unmatched paths: a registered route that happens to be
// under an eligible-looking path never reaches it.
func TestCFRecoveryMiddleware_OnNoRouteChain(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/registered/page", func(c *gin.Context) { c.String(http.StatusOK, "registered") })
	router.NoRoute(newCFRecoveryMiddleware(recoveryTestVerifier{}), func(c *gin.Context) { c.String(http.StatusOK, "spa") })

	do := func(path, assertion string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Sec-Fetch-Mode", "navigate")
		req.Header.Set("Sec-Fetch-Dest", "document")
		req.Header.Set(oauth.CFAccessHeader, assertion)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	if w := do("/library", "svc"); !strings.Contains(w.Body.String(), `http-equiv="refresh"`) || !strings.Contains(w.Body.String(), "cfreset=1") {
		t.Fatalf("SPA navigation with a service token must bounce, got %d %q", w.Code, w.Body.String())
	}
	if w := do("/library", "person"); w.Body.String() != "spa" {
		t.Fatalf("person navigation must reach the SPA, got %q", w.Body.String())
	}
	if w := do("/registered/page", "svc"); w.Body.String() != "registered" {
		t.Fatalf("a registered route must never be bounced, got %q", w.Body.String())
	}
	if w := do("/api/v1/nope", "svc"); strings.Contains(w.Body.String(), "http-equiv") {
		t.Fatalf("an unmatched API path must never be bounced, got %q", w.Body.String())
	}
}
