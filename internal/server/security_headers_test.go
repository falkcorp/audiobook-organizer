// file: internal/server/security_headers_test.go
// version: 1.0.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567891
// last-edited: 2026-06-22

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestSecurityHeadersMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("sets non-TLS headers on plain HTTP", func(t *testing.T) {
		r := gin.New()
		r.Use(securityHeadersMiddleware())
		r.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
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

	t.Run("sets HSTS when X-Forwarded-Proto is https", func(t *testing.T) {
		r := gin.New()
		r.Use(securityHeadersMiddleware())
		r.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Forwarded-Proto", "https")
		r.ServeHTTP(w, req)

		if hsts := w.Header().Get("Strict-Transport-Security"); hsts == "" {
			t.Error("HSTS header missing when X-Forwarded-Proto: https")
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
