// file: internal/server/middleware/cfaccess_verified_test.go
// version: 1.0.2
// guid: 0123b67f-04fc-421e-8413-4d4f53b5ece8
// last-edited: 2026-11-11

package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/cfaccess"
	"github.com/falkcorp/audiobook-organizer/internal/legacyoauth"
)

// markerVerifier answers by raw assertion: "person" verifies with an email,
// "service-token" verifies but names no person, anything else fails.
type markerVerifier struct{}

func (markerVerifier) Verify(_ context.Context, raw string) (*legacyoauth.IdentityClaims, error) {
	switch raw {
	case "person":
		return &legacyoauth.IdentityClaims{Provider: legacyoauth.ProviderCFAccess, Subject: "sub-1", Email: "guest@example.com", EmailVerified: true}, nil
	case "service-token":
		return nil, &cfaccess.NonIdentityAssertionError{Subject: "sub-2", CommonName: "token-id.access"}
	}
	return nil, errors.New("fake verifier: bad assertion")
}

// TestCloudflareAccessAuth_VerifiedMarker proves the marker the invite limiter
// trusts is set only by a verified assertion, and independent of whether the
// identity is admitted to a local user (the allowlist here is empty, so no
// identity is).
func TestCloudflareAccessAuth_VerifiedMarker(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, _, _ := setupAuthTestStore(t)
	cfg := legacyoauth.New(legacyoauth.Config{})

	tests := []struct {
		name    string
		a       *cfaccess.CFAccessAuthenticator
		headers map[string]string
		want    bool
	}{
		{"verified person, not admitted", cfaccess.NewCFAccessAuthenticatorWithVerifier(markerVerifier{}, cfg, store, CFAccessHooks(store)), map[string]string{cfaccess.CFAccessHeader: "person"}, true},
		{"verified service token, no identity", cfaccess.NewCFAccessAuthenticatorWithVerifier(markerVerifier{}, cfg, store, CFAccessHooks(store)), map[string]string{cfaccess.CFAccessHeader: "service-token"}, true},
		{"assertion that does not verify", cfaccess.NewCFAccessAuthenticatorWithVerifier(markerVerifier{}, cfg, store, CFAccessHooks(store)), map[string]string{cfaccess.CFAccessHeader: "forged"}, false},
		{"no assertion", cfaccess.NewCFAccessAuthenticatorWithVerifier(markerVerifier{}, cfg, store, CFAccessHooks(store)), nil, false},
		{"spoofed email header only", cfaccess.NewCFAccessAuthenticatorWithVerifier(markerVerifier{}, cfg, store, CFAccessHooks(store)), map[string]string{"Cf-Access-Authenticated-User-Email": "guest@example.com", "CF-Connecting-IP": "192.0.2.9"}, false},
		{"access not configured", cfaccess.NewCFAccessAuthenticatorWithVerifier(nil, cfg, store, CFAccessHooks(store)), map[string]string{cfaccess.CFAccessHeader: "person"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := gin.New()
			var got bool
			r.Use(cfaccess.CloudflareAccessAuth(tt.a))
			r.GET("/x", func(c *gin.Context) { got = cfaccess.CFAccessVerified(c); c.Status(http.StatusOK) })
			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d", w.Code)
			}
			if got != tt.want {
				t.Fatalf("cfaccess.CFAccessVerified = %v, want %v", got, tt.want)
			}
		})
	}
}
