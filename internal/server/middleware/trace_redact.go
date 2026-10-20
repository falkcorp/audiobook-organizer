// file: internal/server/middleware/trace_redact.go
// version: 1.0.0
// guid: 93865e8e-5956-4a87-9a7d-ae37fd09a3c2
// last-edited: 2026-10-19

package middleware

import (
	"net/url"

	"github.com/falkcorp/audiobook-organizer/internal/logging"
	"github.com/gin-gonic/gin"
)

const traceOriginalURLKey = "middleware.trace_redact.original_url"

// TraceWithRedactedInvitePath wraps a tracing middleware (otelgin) so the
// request path it records carries no invite token.
//
// 🔴 WHY: otelgin records the raw request path as the url.path span attribute
// (otelgin v0.72 internal/semconv/server.go:202-203, from req.URL.Path), and a
// span attribute cannot be rewritten per request through otelgin's options:
// WithSpanStartOptions are static and trace.SpanStartOption is a sealed
// interface. The span NAME is fine (the default formatter uses the route
// template c.FullPath(), so /invite/:token never contains the value, and an
// unmatched SPA path is just "GET"), as are the metric attributes (method,
// route template, status, protocol).
//
// So the tracer sees a request whose URL.Path is redacted, and the original URL
// is put back before any later handler runs: routing has already happened, c.Param
// values are untouched, and the SPA fallback and every handler see the real
// path. The returned slice is ONE unit and must be installed together, in this
// order, with router.Use(TraceWithRedactedInvitePath(mw)...): [swap, tracing,
// restore]. Dropping the last element would leave handlers reading a redacted
// path.
//
// A request that is not an invite route passes through with no copy at all.
func TraceWithRedactedInvitePath(tracing gin.HandlerFunc) []gin.HandlerFunc {
	swap := func(c *gin.Context) {
		orig := c.Request.URL
		redacted := logging.RedactInvitePath(orig.Path)
		if redacted != orig.Path {
			req := *c.Request // shallow: the body and context are shared
			u := *orig
			u.Path = redacted
			u.RawPath = ""
			req.URL = &u
			c.Request = &req
			c.Set(traceOriginalURLKey, orig)
		}
		c.Next()
	}
	restore := func(c *gin.Context) {
		if v, ok := c.Get(traceOriginalURLKey); ok {
			if orig, ok := v.(*url.URL); ok {
				// The tracer may have replaced c.Request (WithContext); that copy
				// owns its URL pointer, so set it rather than mutating the shared
				// redacted URL.
				c.Request.URL = orig
			}
		}
		c.Next()
	}
	return []gin.HandlerFunc{swap, tracing, restore}
}
