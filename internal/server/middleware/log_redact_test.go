// file: internal/server/middleware/log_redact_test.go
// version: 1.3.0
// guid: 67c415d9-c181-4b1c-9481-527366cb1090
// last-edited: 2026-10-22

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

// Invite tokens are bearer secrets in the PATH; the request log must print the
// route readable but never the token, for every invite route form.
func TestRedactingLogFormatter_RequestLogOmitsInviteToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const tok = "invite-token-example-4f9c"
	ok := func(c *gin.Context) { c.Status(http.StatusOK) }
	cases := []struct {
		name, method, route, target, wantLogged string
	}{
		{"spa page", http.MethodGet, "/invite/:token", "/invite/" + tok, "/invite/[REDACTED]"},
		{"public lookup", http.MethodGet, "/api/v1/auth/invite/:token", "/api/v1/auth/invite/" + tok + "?x=1", "/api/v1/auth/invite/[REDACTED]?x=1"},
		{"revoke", http.MethodDelete, "/api/v1/users/invites/:token", "/api/v1/users/invites/" + tok, "/api/v1/users/invites/[REDACTED]"},
		{"resend", http.MethodPost, "/api/v1/users/invites/:token/resend", "/api/v1/users/invites/" + tok + "/resend", "/api/v1/users/invites/[REDACTED]/resend"},
		{"list is untouched", http.MethodGet, "/api/v1/users/invites", "/api/v1/users/invites", "/api/v1/users/invites"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			r := gin.New()
			r.Use(gin.LoggerWithConfig(gin.LoggerConfig{Formatter: RedactingLogFormatter, Output: &buf}))
			r.Handle(tc.method, tc.route, ok)
			r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(tc.method, tc.target, nil))
			line := buf.String()
			if strings.Contains(line, tok) {
				t.Fatalf("request log leaked the invite token: %s", line)
			}
			if !strings.Contains(line, tc.wantLogged) {
				t.Fatalf("request log = %s, want it to contain %q", line, tc.wantLogged)
			}
		})
	}
}

// The access-log line is built from the request's path as parsed (URL.Path, one
// decoding of the request line), so /invite%252F<token>/.. reaches the formatter
// as /invite%2F<token>/.. and must still be masked.
func TestRedactingLogFormatter_DoublyEncodedTokenBeforeADotSegment(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const tok = "access-log-doubly-encoded-token"
	for _, target := range []string{"/invite%252F" + tok + "/..", "/invite%2F" + tok + "/..", "/x/../invite/" + tok + "/.."} {
		var buf bytes.Buffer
		r := gin.New()
		r.Use(gin.LoggerWithConfig(gin.LoggerConfig{Formatter: RedactingLogFormatter, Output: &buf}))
		r.NoRoute(func(c *gin.Context) { c.Status(http.StatusOK) })
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, target, nil))
		if line := buf.String(); strings.Contains(line, tok) || !strings.Contains(line, "[REDACTED]") {
			t.Fatalf("request line %q: log = %s", target, line)
		}
	}
}

// An unmatched path (the SPA /invite/<token> page is served by NoRoute) is
// logged through the same formatter.
func TestRedactingLogFormatter_NoRouteInvitePage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var buf bytes.Buffer
	r := gin.New()
	r.Use(gin.LoggerWithConfig(gin.LoggerConfig{Formatter: RedactingLogFormatter, Output: &buf}))
	r.NoRoute(func(c *gin.Context) { c.Status(http.StatusOK) })
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/invite/spa-token-example", nil))
	if line := buf.String(); strings.Contains(line, "spa-token-example") || !strings.Contains(line, "/invite/[REDACTED]") {
		t.Fatalf("request log = %s", line)
	}
}

func TestRedactLogPath_BothRedactions(t *testing.T) {
	got := RedactLogPath("/api/v1/auth/invite/some-token?token=abc&x=1")
	want := "/api/v1/auth/invite/[REDACTED]?token=REDACTED&x=1"
	if got != want {
		t.Fatalf("RedactLogPath = %q, want %q", got, want)
	}
}

// SF2: RequestURI keeps %2F escaped; the token behind an encoded slash is masked.
func TestRedactLogPath_EncodedSlashBeforeTheInviteToken(t *testing.T) {
	for in, want := range map[string]string{
		"/api/v1/auth/invite%2Fsome-token":           "/api/v1/auth/invite/[REDACTED]",
		"/api/v1/auth/invite%2fsome-token?token=abc": "/api/v1/auth/invite/[REDACTED]?token=REDACTED",
		"/api/v1/users/invites%2Fsome-token/resend":  "/api/v1/users/invites/[REDACTED]/resend",
		"/invite%2Fsome-token?x=1":                   "/invite/[REDACTED]?x=1",
		// A dot segment after the token must not walk the route away first.
		"/invite%2Fsome-token/..":              "/invite/[REDACTED]/[REDACTED]",
		"/x/../invite/some-token/..?token=abc": "/[REDACTED]?token=REDACTED",
		"/invite%252Fsome-token%252F..":        "/invite/[REDACTED]/[REDACTED]",
	} {
		if got := RedactLogPath(in); got != want {
			t.Fatalf("RedactLogPath(%q) = %q, want %q", in, got, want)
		}
	}
}
