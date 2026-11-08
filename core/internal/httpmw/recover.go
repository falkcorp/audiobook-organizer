// file: core/internal/httpmw/recover.go
// version: 1.0.1
// guid: 502a11e1-d109-4b9b-945d-2f4bc6b0851c
// last-edited: 2026-11-08

package httpmw

import (
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"
	"syscall"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// recovery is stage 3 (05-http-api §4.2): a handler panic becomes problem
// 500 internal, logged with the redacted request line and the stack, the
// span marked as an error and mediacore_http_panics_total counted. A lost
// client connection (EPIPE, ECONNRESET) is logged at WARN and answered
// with nothing (HEAD's RedactingRecovery); http.ErrAbortHandler keeps its
// net/http meaning and aborts the response. A panic after the response
// started cannot become a 500, so it aborts the connection instead of
// leaving a truncated answer that looks complete.
//
// The panic unwound past stage 4, so this stage writes the request's access
// record on every path (emitAccess), as HEAD's gin logger, which ran
// outside recovery, did: the 500, or the status already sent. The request
// line is the access record's target (loggedTarget), so a credential after
// a "?" sent as %3F is masked here as it is there.
func (c *Chain) recovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hw := newHookWriter(w, r, nil)
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				c.emitAccess(r, hw.Status(), hw.n)
				panic(rec)
			}
			line := r.Method + " " + loggedTarget(c.deps.Redactor, r.URL) + " " + r.Proto
			if err, ok := rec.(error); ok && (errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET)) {
				c.deps.Log.WarnContext(r.Context(), "httpapi: client connection lost",
					"error", c.deps.Redactor.String("error", err.Error()), "request", line,
					"request_id", RequestID(r.Context()))
				c.emitAccess(r, hw.Status(), hw.n)
				return
			}
			st := From(r.Context())
			owner, route := "core", ""
			if st != nil {
				owner, route = st.owner(), st.Route()
			}
			c.deps.Log.ErrorContext(r.Context(), "httpapi: panic recovered",
				"panic", c.deps.Redactor.String("panic", fmt.Sprint(rec)), "request", line,
				"request_id", RequestID(r.Context()), "stack", string(debug.Stack()))
			if span := trace.SpanFromContext(r.Context()); span.IsRecording() {
				span.SetStatus(codes.Error, "panic")
			}
			c.inst.panicked(r.Context(), owner, route)
			if hw.wrote || hw.hijacked {
				c.emitAccess(r, hw.Status(), hw.n)
				panic(http.ErrAbortHandler)
			}
			// What the handler set for its own answer does not describe this
			// one; the perimeter's headers are completed as stage 5 would.
			h := hw.Header()
			for _, k := range []string{"Content-Length", "Content-Encoding", "Content-Disposition", "Content-Range", "Etag", "Last-Modified"} {
				h.Del(k)
			}
			c.ownAnswer(hw, r, http.StatusInternalServerError, "internal", nil)
			c.emitAccess(r, http.StatusInternalServerError, hw.n)
		}()
		next.ServeHTTP(hw, r)
	})
}
