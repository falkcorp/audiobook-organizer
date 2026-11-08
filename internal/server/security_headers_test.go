// file: internal/server/security_headers_test.go
// version: 1.1.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567891
// last-edited: 2026-11-08

package server

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/mediacore/httpapi"
)

// TestSecurityHeaders is HEAD's TestSecurityHeadersMiddleware through core's
// stage 5, which sets the headers since R1-17.
func TestSecurityHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stack := func(t *testing.T) (*httpapi.Router, http.Handler) {
		r := gin.New()
		r.GET("/shell", func(c *gin.Context) { c.Status(http.StatusOK) })
		return stagesOver(t, r, httpapi.Deps{}, httpapi.LegacyRoute{Method: http.MethodGet, GinPath: "/shell"})
	}

	t.Run("sets non-TLS headers on plain HTTP", func(t *testing.T) {
		_, r := stack(t)
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/shell", nil)
		r.ServeHTTP(w, req)

		want := map[string]string{
			"X-Content-Type-Options": "nosniff",
			"X-Frame-Options":        "SAMEORIGIN",
			"Referrer-Policy":        "strict-origin-when-cross-origin",
			"X-Xss-Protection":       "0",
		}
		for h, v := range want {
			if got := w.Header().Get(h); got != v {
				t.Errorf("header %q: got %q, want %q", h, got, v)
			}
		}
		// HSTS must NOT be set on plain HTTP
		if hsts := w.Header().Get("Strict-Transport-Security"); hsts != "" {
			t.Errorf("HSTS should not be set on plain HTTP, got %q", hsts)
		}
	})

	// Stage 5 believes X-Forwarded-Proto only from a trusted proxy
	// (core.http.trusted_proxies, ABO's TUNNEL_CONNECTOR_ADDRS); HEAD believed
	// it from any peer (05-http-api §4.2 stage 5).
	t.Run("sets HSTS when a trusted proxy says X-Forwarded-Proto is https", func(t *testing.T) {
		router, r := stack(t)
		if err := router.SetPerimeter(httpapi.Options{TrustedProxies: []httpapi.TrustedPeer{{Addr: netip.MustParseAddr("192.0.2.53")}}}); err != nil {
			t.Fatal(err)
		}
		for peer, want := range map[string]string{"192.0.2.53:41000": "max-age=31536000; includeSubDomains", "192.0.2.99:41000": ""} {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/shell", nil)
			req.RemoteAddr = peer
			req.Header.Set("X-Forwarded-Proto", "https")
			r.ServeHTTP(w, req)
			if got := w.Header().Get("Strict-Transport-Security"); got != want {
				t.Errorf("peer %s: HSTS = %q, want %q", peer, got, want)
			}
		}
	})
}

func TestTempLoginURLBuilding(t *testing.T) {
	t.Run("uses externalURL when configured", func(t *testing.T) {
		s := &Server{externalURL: "https://books.example.com"}
		rel := "/auth/temp-login?token=abc123"
		want := "https://books.example.com" + rel
		got := s.externalURL + rel
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("returns relative path when externalURL is empty", func(t *testing.T) {
		s := &Server{externalURL: ""}
		rel := "/auth/temp-login?token=abc123"
		loginURL := rel
		if s.externalURL != "" {
			loginURL = s.externalURL + rel
		}
		if loginURL != rel {
			t.Errorf("expected relative path %q, got %q", rel, loginURL)
		}
	})
}
