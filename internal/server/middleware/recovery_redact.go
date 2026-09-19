// file: internal/server/middleware/recovery_redact.go
// version: 1.1.0
// guid: 7ec1e8a2-beb7-4e4c-a883-d7bab38999f7
// last-edited: 2026-09-19

package middleware

import (
	"errors"
	"net/http"
	"runtime/debug"
	"strings"
	"syscall"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/gin-gonic/gin"
)

var recoveryLog = logger.New("http")

// RedactingRecovery replaces gin.Recovery.
//
// 🔴 WHY: gin.Recovery logs the request via its secureRequestDump, which masks
// only the Authorization HEADER. The request LINE keeps the query string, and
// ABS clients carry the bearer credential there (?token=, a JWT or an abk_ API
// key). gin logs that dump on every broken pipe / connection reset /
// http.ErrAbortHandler in every mode, and on every panic in debug mode — which
// is the build prod runs. So a client hanging up mid-download wrote a live
// credential into the journal.
//
// Behaviour otherwise mirrors gin: a broken connection is recorded on the
// context and aborted without a status (the client is gone); any other panic
// is logged with a stack and answered 500. The logged request line goes
// through RedactQueryCredentials and credential-bearing headers are masked.
func RedactingRecovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			brokenPipe := false
			if err, ok := rec.(error); ok {
				// errors.Is on the errno, as gin does: string matching an
				// OpError→SyscallError chain misses wrapped and platform-
				// specific forms of the same condition.
				brokenPipe = errors.Is(err, syscall.EPIPE) ||
					errors.Is(err, syscall.ECONNRESET) ||
					errors.Is(err, http.ErrAbortHandler)
			}
			line := RedactedRequestSummary(c.Request)
			if brokenPipe {
				recoveryLog.Warn("client connection lost: %v request=%s", rec, line)
				if err, ok := rec.(error); ok {
					_ = c.Error(err)
				}
				c.Abort()
				return
			}
			recoveryLog.Error("panic recovered: %v request=%s\n%s", rec, line, debug.Stack())
			c.AbortWithStatus(http.StatusInternalServerError)
		}()
		c.Next()
	}
}

// credentialHeaders are masked in RedactedRequestSummary.
var credentialHeaders = []string{"Authorization", "Cookie", "Proxy-Authorization",
	"Cf-Access-Jwt-Assertion", "Cf-Access-Client-Secret", "X-Api-Key"}

// RedactedRequestSummary renders "METHOD /path?query HTTP/x | headers" with
// credential query values and credential headers masked, for crash and error
// logs.
func RedactedRequestSummary(r *http.Request) string {
	if r == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(r.Method)
	b.WriteByte(' ')
	b.WriteString(RedactQueryCredentials(r.URL.RequestURI()))
	b.WriteByte(' ')
	b.WriteString(r.Proto)
	for name, vals := range r.Header {
		v := strings.Join(vals, ",")
		for _, ch := range credentialHeaders {
			if strings.EqualFold(name, ch) {
				v = RedactedValue
				break
			}
		}
		b.WriteString(" | ")
		b.WriteString(name)
		b.WriteString(": ")
		b.WriteString(v)
	}
	return b.String()
}
