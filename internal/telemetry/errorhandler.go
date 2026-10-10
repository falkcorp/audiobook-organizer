// file: internal/telemetry/errorhandler.go
// version: 1.2.0
// guid: 9d3f6b21-7a48-4c05-b1e9-2f60c8a4d7e3
// last-edited: 2026-10-10

package telemetry

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
)

// exportErrorLogInterval is the minimum gap between two logged export errors.
const exportErrorLogInterval = 10 * time.Minute

// rateLimitedErrorHandler is an otel.ErrorHandler that logs the first error
// and then at most one per interval, saying how many it dropped in between.
//
// Why: a periodic OTLP export that fails (collector down, TLS mismatch) hands
// the SDK's error handler one error per attempt, and the default handler
// writes each to the stdlib log, which this server routes into the slog file.
// At the 5s minimum interval that is 17,280 lines a day for one dead
// collector. The handler is process-global (otel.SetErrorHandler), so it
// also covers errors from the trace exporter; nothing else in this repo sets
// one.
type rateLimitedErrorHandler struct {
	interval time.Duration
	now      func() time.Time
	log      func(level slog.Level, msg string, attrs ...any)

	mu         sync.Mutex
	last       time.Time
	logged     bool
	suppressed int
}

func newRateLimitedErrorHandler(interval time.Duration, now func() time.Time, log func(slog.Level, string, ...any)) *rateLimitedErrorHandler {
	return &rateLimitedErrorHandler{interval: interval, now: now, log: log}
}

// Handle implements otel.ErrorHandler.
func (h *rateLimitedErrorHandler) Handle(err error) {
	if err == nil {
		return
	}
	h.mu.Lock()
	t := h.now()
	if h.logged && t.Sub(h.last) < h.interval {
		h.suppressed++
		h.mu.Unlock()
		return
	}
	suppressed := h.suppressed
	h.suppressed, h.last, h.logged = 0, t, true
	h.mu.Unlock()

	h.log(slog.LevelError, "OpenTelemetry export error (rate limited)",
		"error", redactEndpointSecrets(err.Error()),
		"suppressed_since_last", suppressed,
		"min_interval", h.interval.String())
}

var installErrorHandlerOnce sync.Once

// installExportErrorHandler installs the rate-limited handler process-wide,
// once. InitOTEL calls it only when the OTLP metric push is on.
func installExportErrorHandler() {
	installErrorHandlerOnce.Do(func() {
		otel.SetErrorHandler(newRateLimitedErrorHandler(exportErrorLogInterval, time.Now,
			func(level slog.Level, msg string, attrs ...any) {
				emit(context.Background(), level, msg, attrs...)
			}))
	})
}

// urlTokenRE matches a URL-looking token (scheme:// followed by anything up
// to whitespace or a quote) in free text. Within it, redactEndpointSecrets
// removes the userinfo and the "?query" / "#fragment" tail.
var urlTokenRE = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s"'<>]*`)

// redactEndpointSecrets is the backstop for text that quotes an endpoint:
// for each URL-looking token it drops userinfo (after "://" and any slashes,
// up to the last '@' that precedes a '?'/'#' tail, so "dns:///u:p@h", a
// password containing '/' or '@' and several tokens per string are covered)
// and the "?query" and "#fragment". The primary defence is
// parseOTLPEndpoint, which strips the same things at parse time.
func redactEndpointSecrets(s string) string {
	return urlTokenRE.ReplaceAllStringFunc(s, func(tok string) string {
		i := strings.Index(tok, "://")
		head, rest := tok[:i+3], tok[i+3:]
		rest = strings.TrimLeft(rest, "/")
		head += strings.Repeat("/", len(tok)-len(head)-len(rest))
		if qi := strings.IndexAny(rest, "?#"); qi >= 0 && qi > strings.LastIndex(rest, "@") {
			rest = rest[:qi]
		}
		if at := strings.LastIndex(rest, "@"); at >= 0 {
			rest = rest[at+1:]
		}
		if qi := strings.IndexAny(rest, "?#"); qi >= 0 {
			rest = rest[:qi]
		}
		return head + rest
	})
}
