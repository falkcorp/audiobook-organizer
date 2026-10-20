// file: internal/httputil/respond_redact_test.go
// version: 1.0.0
// guid: 0a4e7c12-8b35-4d6f-9e21-5c7b3f1a9d40
// last-edited: 2026-10-19

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
