// file: core/internal/httpmw/accesslog.go
// version: 1.0.1
// guid: 0a02b49d-7162-47dd-a1e6-c0a0254b3766
// last-edited: 2026-11-08

package httpmw

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/log"
)

// AccessMessage is the message of every access-log record stage 4 writes,
// so an app's log output can route the access log apart from its other
// lines (ABO keeps it out of the Activity feed, as HEAD's gin logger was).
const AccessMessage = "http access"

// skipAccessLog are the paths stage 4 does not log: HEAD's SkipPaths, the
// two operations streams, which a client holds open (COR3-6).
func skipAccessLog(path string) bool {
	return path == "/api/v1/operations/events" || path == "/api/v3/ops/events"
}

// accessLog is stage 4 (05-http-api §4.2): one slog record per request,
// with the URL under the kernel's Redactor, as stage 2's span. A refusal of
// stage 7 (and from F4 stage 13) carries security_event, the access log
// standing in for audit until the identity flip (SEC2-20).
func (c *Chain) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		hw := newHookWriter(w, r, nil)
		next.ServeHTTP(hw, r)
		st := From(r.Context())
		event := ""
		if st != nil {
			if p := st.event.Load(); p != nil {
				event = *p
			}
		}
		if event == "" && skipAccessLog(r.URL.Path) {
			return
		}
		attrs := []slog.Attr{
			slog.String("method", r.Method),
			slog.String("path", loggedTarget(c.deps.Redactor, r.URL)),
			slog.Int("status", hw.Status()),
			slog.Duration("duration", time.Since(start)),
			slog.Int64("bytes", hw.n),
			slog.String("proto", r.Proto),
		}
		if st != nil {
			if st.client.IsValid() {
				attrs = append(attrs, slog.String("client", st.client.String()))
			}
			attrs = append(attrs, slog.String("request_id", st.requestID))
			if st.clientRequestID != "" {
				attrs = append(attrs, slog.String("client_request_id", st.clientRequestID))
			}
			if route := st.Route(); route != "" {
				attrs = append(attrs, slog.String("route", route))
			}
		}
		level := slog.LevelInfo
		if event != "" {
			attrs = append(attrs, slog.String("security_event", event))
			level = slog.LevelWarn
		}
		c.deps.Log.LogAttrs(r.Context(), level, AccessMessage, attrs...)
	})
}

// securityEvent marks the request for the access log's security_event.
func securityEvent(r *http.Request, event string) {
	if st := From(r.Context()); st != nil {
		st.event.Store(&event)
	}
}

// redactedPath is a decoded request path under rd: the path rules (an
// invite token), then the URL rules over what follows a "?" in it. A "?"
// that arrived as %3F is path text once URL.Path decodes it, and a client
// can still put a credential after it (/x/cover%3Ftoken=...); the URL rules
// mask it as they would a real query's (HEAD's RedactLogRequestPath).
func redactedPath(rd *log.Redactor, p string) string {
	return rd.URL(rd.Path(p))
}

// loggedTarget is the request target stage 4 writes: the redacted decoded
// path with every "?" left in it written as %3F, so the line's first "?" is
// always the real query delimiter, then the raw query under rd.
func loggedTarget(rd *log.Redactor, u *url.URL) string {
	p := strings.ReplaceAll(redactedPath(rd, u.Path), "?", "%3F")
	if u.RawQuery == "" {
		return p
	}
	return rd.URL(p + "?" + u.RawQuery)
}
