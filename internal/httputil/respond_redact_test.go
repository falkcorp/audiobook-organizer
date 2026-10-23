// file: internal/httputil/respond_redact_test.go
// version: 1.1.0
// guid: 0a4e7c12-8b35-4d6f-9e21-5c7b3f1a9d40
// last-edited: 2026-10-22

package httputil

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/logger/logtest"
	"github.com/gin-gonic/gin"
)

// An error answer to an invite route (a used or expired token) is logged with
// the request path; the token in it must be masked.
func TestLogErrorWithContext_RedactsInviteToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const tok = "respond-invite-token-example"
	buf := logtest.Capture(t, slog.LevelDebug)

	r := gin.New()
	r.GET("/api/v1/auth/invite/:token", func(c *gin.Context) {
		RespondWithError(c, http.StatusGone, "invite expired", "INVITE_EXPIRED")
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/auth/invite/"+tok, nil))

	out := buf.String()
	if strings.Contains(out, tok) {
		t.Fatalf("error log leaked the invite token: %s", out)
	}
	if !strings.Contains(out, "/api/v1/auth/invite/[REDACTED]") {
		t.Fatalf("error log lost the readable path: %s", out)
	}
}

// %3F decodes to "?" in URL.Path; the error log must treat it as path text and
// mask the token behind it.
func TestLogErrorWithContext_RedactsTokenBehindAnEncodedQuestionMark(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const tok = "respond-question-mark-token"
	for _, target := range []string{
		"/api/v1/auth/invite/%3F" + tok,
		"/api/v1/users/invites/%3F" + tok + "/resend",
		"/invite/%3F" + tok,
		"/x%3F/../invite/" + tok,
	} {
		buf := logtest.Capture(t, slog.LevelDebug)
		r := gin.New()
		r.NoRoute(func(c *gin.Context) {
			RespondWithError(c, http.StatusGone, "invite expired", "INVITE_EXPIRED")
		})
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, target, nil))
		if out := buf.String(); strings.Contains(out, tok) || !strings.Contains(out, "[REDACTED]") {
			t.Fatalf("request line %q: error log = %s", target, out)
		}
	}
}
