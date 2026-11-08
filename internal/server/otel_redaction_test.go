// file: internal/server/otel_redaction_test.go
// version: 1.2.1
// guid: c57d8b27-c4fa-4a32-8074-be746f84c425
// last-edited: 2026-11-08

package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// TestStage2SpansCarryNoQueryCredentials pins that the span core's stage 2
// records for a request (gin's tracing middleware's job until R1-17) carries no query
// credential. ABS clients send the bearer credential (JWT or abk_ API key) as
// ?token=; stage 2 records url.query through log.Redactor, which masks those
// values.
func TestStage2SpansCarryNoQueryCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	engine := gin.New()
	engine.GET("/api/items/:id/file/:ino", func(c *gin.Context) { c.Status(http.StatusOK) })
	_, r := stagesOver(t, engine, httpapi.Deps{TracerProvider: tp},
		httpapi.LegacyRoute{Method: http.MethodGet, GinPath: "/api/items/:id/file/:ino"})

	secret := "abk_" + "span-secret"
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet,
		"/api/items/a/file/b?token="+secret+"&api_key="+secret, nil))
	// A credential after a "?" sent as %3F is path text in URL.Path, so it
	// reaches url.path rather than url.query.
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet,
		"/api/items/a/file/b%3Ftoken="+secret, nil))

	spans := rec.Ended()
	if len(spans) == 0 {
		t.Fatal("no span recorded; the test is not observing stage 2")
	}
	for _, s := range spans {
		for _, kv := range s.Attributes() {
			if strings.Contains(kv.Value.String(), "span-secret") {
				t.Fatalf("span %q attribute %s carries the query credential: %s", s.Name(), kv.Key, kv.Value.String())
			}
		}
	}
}

// TestStage2SpansCarryNoInviteToken pins that an invite token, a bearer secret
// that lives in the request PATH, reaches no span: stage 2 records url.path
// through log.Redactor and names the span after the route's pattern. It runs
// core's stages over a gin engine, for each invite route plus the SPA page
// served by NoRoute, and checks every attribute of every span and the span
// name, while the handler still sees the real token.
func TestStage2SpansCarryNoInviteToken(t *testing.T) {
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
			_, stack := stagesOver(t, r, httpapi.Deps{TracerProvider: tp},
				httpapi.LegacyRoute{Method: http.MethodGet, GinPath: "/api/v1/auth/invite/:token", Perm: rbac.Public, Public: true},
				httpapi.LegacyRoute{Method: http.MethodDelete, GinPath: "/api/v1/users/invites/:token"},
				httpapi.LegacyRoute{Method: http.MethodPost, GinPath: "/api/v1/users/invites/:token/resend"},
				httpapi.LegacyRoute{Method: http.MethodGet, GinPath: "/api/v1/users/invites"},
			)

			path, _, _ := strings.Cut(tc.target, "?")
			w := httptest.NewRecorder()
			stack.ServeHTTP(w, httptest.NewRequest(tc.method, tc.target, nil))
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
				t.Fatal("no span recorded; the test is not observing stage 2")
			}
			foundPath := false
			for _, s := range spans {
				if strings.Contains(s.Name(), tok) {
					t.Fatalf("span name %q carries the invite token", s.Name())
				}
				for _, kv := range s.Attributes() {
					v := kv.Value.String()
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
