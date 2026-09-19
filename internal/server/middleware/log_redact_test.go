// file: internal/server/middleware/log_redact_test.go
// version: 1.0.0
// guid: 67c415d9-c181-4b1c-9481-527366cb1090
// last-edited: 2026-09-19

package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRedactQueryCredentials(t *testing.T) {
	secret := "abk_" + "test"
	for in, want := range map[string]string{
		"/api/items/x/file/y?token=" + secret:                  "/api/items/x/file/y?token=REDACTED",
		"/api/items/x/cover?width=400&token=jwt.a.b&format=jp": "/api/items/x/cover?width=400&token=REDACTED&format=jp",
		"/x?API_KEY=" + secret + "&q=needle":                   "/x?API_KEY=REDACTED&q=needle",
		"/cb?code=one-time&state=s":                            "/cb?code=REDACTED&state=s",
		"/x?access_token=a&refresh_token=b":                    "/x?access_token=REDACTED&refresh_token=REDACTED",
		"/x?q=token":                                           "/x?q=token",
		"/plain/path":                                          "/plain/path",
		"/x?tok%65n=" + secret:                                 "/x?tok%65n=REDACTED",
	} {
		if got := RedactQueryCredentials(in); got != want {
			t.Errorf("RedactQueryCredentials(%q) = %q, want %q", in, got, want)
		}
	}
}

// The formatter wired into the real request logger must not print the value.
func TestRedactingLogFormatter_RequestLogOmitsQueryCredential(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := "abk_" + "test-secret-value"
	var buf bytes.Buffer
	r := gin.New()
	r.Use(gin.LoggerWithConfig(gin.LoggerConfig{Formatter: RedactingLogFormatter, Output: &buf}))
	r.GET("/api/items/:id/file/:ino", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/items/a/file/b?token="+secret, nil))
	line := buf.String()
	if strings.Contains(line, "test-secret-value") {
		t.Fatalf("request log leaked the credential: %s", line)
	}
	if !strings.Contains(line, "token=REDACTED") || !strings.Contains(line, "/api/items/a/file/b") {
		t.Fatalf("request log lost its path/marker: %s", line)
	}
}
