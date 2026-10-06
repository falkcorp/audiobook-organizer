// file: internal/server/middleware/recovery_redact_test.go
// version: 1.2.0
// guid: fac23317-790b-49a1-b9f3-81b598014604
// last-edited: 2026-10-06

package middleware

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/logger/logtest"
	"github.com/gin-gonic/gin"
)

// captureSlog captures slog.Default into a mutex-guarded buffer; logtest
// refuses to run under t.Parallel because the swap is process-global.
func captureSlog(t *testing.T) *logtest.Buffer {
	t.Helper()
	return logtest.Capture(t, slog.LevelDebug)
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
		// Wrapped without an OpError/SyscallError chain: only errors.Is on the
		// errno recognises these (re-review LOW #4).
		"wrapped epipe":      {fmt.Errorf("write body: %w", syscall.EPIPE), http.StatusOK},
		"wrapped econnreset": {fmt.Errorf("read: %w", syscall.ECONNRESET), http.StatusOK},
		"panic":              {"boom", http.StatusInternalServerError},
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
