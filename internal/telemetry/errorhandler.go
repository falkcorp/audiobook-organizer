// file: internal/telemetry/errorhandler.go
// version: 1.1.0
// guid: 9d3f6b21-7a48-4c05-b1e9-2f60c8a4d7e3
// last-edited: 2026-10-10

package telemetry

import (
	"context"
	"log/slog"
	"regexp"
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
		"error", redactUserinfo(err.Error()),
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

// userinfoRE matches a URL authority's userinfo as a backstop: after "://" and
// any slashes, everything in the token (no whitespace or quote) up to its
// LAST '@', so "dns:///u:p@h", a password containing '/' or '@', and several
// occurrences in one string are all covered. The primary defence is
// stripUserinfo at endpoint parse time.
var userinfoRE = regexp.MustCompile(`(://+)[^\s"'<>]*@`)

// redactUserinfo removes URL userinfo from a string so an endpoint, or an
// error that quotes it, can be logged.
func redactUserinfo(s string) string {
	return userinfoRE.ReplaceAllString(s, "$1")
}
