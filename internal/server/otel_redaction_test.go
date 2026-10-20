// file: internal/server/otel_redaction_test.go
// version: 1.1.0
// guid: c57d8b27-c4fa-4a32-8074-be746f84c425
// last-edited: 2026-10-19

package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// TestOtelginSpansCarryNoQueryCredentials pins that the otelgin middleware the
// router installs records no query string on its spans. ABS clients send the
// bearer credential (JWT or abk_ API key) as ?token=; otelgin v0.71 records
// url.path only, and an upgrade that starts recording url.query / url.full /
// http.target would export live credentials to the trace backend. If this
// fails after an upgrade, add a span processor or WithSpanStartOptions override
// that redacts those attributes (see middleware.RedactQueryCredentials).
func TestOtelginSpansCarryNoQueryCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	r := gin.New()
	r.Use(otelgin.Middleware("audiobook-organizer", otelgin.WithTracerProvider(tp)))
	r.GET("/api/items/:id/file/:ino", func(c *gin.Context) { c.Status(http.StatusOK) })

	secret := "abk_" + "span-secret"
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet,
		"/api/items/a/file/b?token="+secret+"&api_key="+secret, nil))

	spans := rec.Ended()
	if len(spans) == 0 {
		t.Fatal("no span recorded; the test is not observing otelgin")
	}
	for _, s := range spans {
		for _, kv := range s.Attributes() {
			if strings.Contains(kv.Value.String(), "span-secret") {
				t.Fatalf("span %q attribute %s carries the query credential: %s", s.Name(), kv.Key, kv.Value.String())
			}
		}
	}
}

// TestTracingMiddlewareSpansCarryNoInviteToken pins that an invite token, a
// bearer secret that lives in the request PATH, reaches no span: otelgin records
// the raw path as url.path, so the router installs it through
// tracingMiddleware. It runs the production constructor, for each invite route
// plus the SPA page served by NoRoute, and checks every attribute of every span
// and the span name, while the handler still sees the real token.
func TestTracingMiddlewareSpansCarryNoInviteToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const tok = "span-invite-token-example"

	cases := []struct {
		name, method, target, wantPath string
	}{
		{"spa page", http.MethodGet, "/invite/" + tok, "/invite/[REDACTED]"},
		{"public lookup", http.MethodGet, "/api/v1/auth/invite/" + tok + "?x=1", "/api/v1/auth/invite/[REDACTED]"},
		{"revoke", http.MethodDelete, "/api/v1/users/invites/" + tok, "/api/v1/users/invites/[REDACTED]"},
		{"resend", http.MethodPost, "/api/v1/users/invites/" + tok + "/resend", "/api/v1/users/invites/[REDACTED]/resend"},
		{"list is untouched", http.MethodGet, "/api/v1/users/invites", "/api/v1/users/invites"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := tracetest.NewSpanRecorder()
			tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
			r := gin.New()
			r.Use(tracingMiddleware(otelgin.WithTracerProvider(tp))...)

			var sawPath, sawParam string
			h := func(c *gin.Context) {
				sawPath, sawParam = c.Request.URL.Path, c.Param("token")
				c.Status(http.StatusOK)
			}
			r.GET("/api/v1/auth/invite/:token", h)
			r.DELETE("/api/v1/users/invites/:token", h)
			r.POST("/api/v1/users/invites/:token/resend", h)
			r.GET("/api/v1/users/invites", h)
			r.NoRoute(h) // the SPA page /invite/:token is not a registered route

			path, _, _ := strings.Cut(tc.target, "?")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(tc.method, tc.target, nil))
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d", w.Code)
			}
			if sawPath != path {
				t.Fatalf("handler saw path %q, want the real %q", sawPath, path)
			}
			if strings.Contains(tc.target, tok) && tc.name != "spa page" && sawParam != tok {
				t.Fatalf("handler saw token param %q, want the real token", sawParam)
			}

			spans := rec.Ended()
			if len(spans) == 0 {
				t.Fatal("no span recorded; the test is not observing otelgin")
			}
			foundPath := false
			for _, s := range spans {
				if strings.Contains(s.Name(), tok) {
					t.Fatalf("span name %q carries the invite token", s.Name())
				}
				for _, kv := range s.Attributes() {
					v := kv.Value.Emit()
					if strings.Contains(v, tok) {
						t.Fatalf("span %q attribute %s carries the invite token: %s", s.Name(), kv.Key, v)
					}
					if string(kv.Key) == "url.path" {
						foundPath = true
						if v != tc.wantPath {
							t.Fatalf("url.path = %q, want %q", v, tc.wantPath)
						}
					}
				}
			}
			if !foundPath {
				t.Fatal("no url.path attribute recorded; the test is not observing the raw-path attribute")
			}
		})
	}
}
