// file: internal/server/otel_redaction_test.go
// version: 1.0.0
// guid: c57d8b27-c4fa-4a32-8074-be746f84c425
// last-edited: 2026-09-19

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
			if strings.Contains(kv.Value.Emit(), "span-secret") {
				t.Fatalf("span %q attribute %s carries the query credential: %s", s.Name(), kv.Key, kv.Value.Emit())
			}
		}
	}
}
