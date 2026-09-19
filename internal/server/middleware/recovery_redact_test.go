// file: internal/server/middleware/recovery_redact_test.go
// version: 1.0.0
// guid: fac23317-790b-49a1-b9f3-81b598014604
// last-edited: 2026-09-19

package middleware

import (
	"bytes"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/gin-gonic/gin"
)

func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// Review MEDIUM #2: gin.Recovery's request dump kept ?token=; the replacement
// must log the request with the credential masked, on both the broken-pipe path
// (every mode) and the panic path.
func TestRedactingRecovery_LogsNoQueryCredential(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := "abk_" + "recovery-secret"
	for name, tc := range map[string]struct {
		panicWith any
		wantCode  int
	}{
		"broken pipe": {&net.OpError{Op: "write", Err: &os.SyscallError{Syscall: "write", Err: syscall.EPIPE}}, http.StatusOK},
		"abort":       {http.ErrAbortHandler, http.StatusOK},
		"panic":       {"boom", http.StatusInternalServerError},
	} {
		t.Run(name, func(t *testing.T) {
			buf := captureSlog(t)
			r := gin.New()
			r.Use(RedactingRecovery())
			r.GET("/api/items/:id/file/:ino", func(c *gin.Context) { panic(tc.panicWith) })
			req := httptest.NewRequest(http.MethodGet, "/api/items/a/file/b?token="+secret, nil)
			req.Header.Set("Authorization", "Bearer "+secret)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", w.Code, tc.wantCode)
			}
			out := buf.String()
			if strings.Contains(out, "recovery-secret") {
				t.Fatalf("recovery log leaked the credential: %s", out)
			}
			if !strings.Contains(out, "token=REDACTED") {
				t.Fatalf("recovery log lost the request line: %s", out)
			}
		})
	}
}
