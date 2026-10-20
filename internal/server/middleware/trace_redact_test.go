// file: internal/server/middleware/trace_redact_test.go
// version: 1.0.0
// guid: 23c9b5f4-65b1-4dfa-8251-669861371e45
// last-edited: 2026-10-19

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// The tracer must see the redacted path, every later handler must see the real
// one, and a request that is not an invite route must not be touched at all.
func TestTraceWithRedactedInvitePath_SwapsOnlyAroundTheTracer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const tok = "trace-token-example"

	var tracerSaw, handlerSaw, paramSaw string
	var tracerReq, handlerReq *http.Request
	tracer := func(c *gin.Context) {
		tracerSaw = c.Request.URL.Path
		tracerReq = c.Request
		c.Next()
	}
	r := gin.New()
	r.Use(TraceWithRedactedInvitePath(tracer)...)
	handler := func(c *gin.Context) {
		handlerSaw = c.Request.URL.Path
		handlerReq = c.Request
		paramSaw = c.Param("token")
		c.Status(http.StatusOK)
	}
	r.GET("/api/v1/auth/invite/:token", handler)
	r.GET("/api/v1/books", handler)

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/auth/invite/"+tok, nil))
	if tracerSaw != "/api/v1/auth/invite/[REDACTED]" {
		t.Fatalf("tracer saw %q", tracerSaw)
	}
	if handlerSaw != "/api/v1/auth/invite/"+tok || paramSaw != tok {
		t.Fatalf("handler saw path %q param %q, want the real values", handlerSaw, paramSaw)
	}

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/books", nil))
	if tracerSaw != "/api/v1/books" || handlerSaw != "/api/v1/books" {
		t.Fatalf("non-invite path altered: tracer %q handler %q", tracerSaw, handlerSaw)
	}
	if tracerReq != handlerReq {
		t.Fatal("a non-invite request must reach the handler as the very same *http.Request (no copy)")
	}
}
