// file: internal/telemetry/errorhandler.go
// version: 1.3.0
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

// exportErrorLogInterval is the minimum gap between two logged errors of the
// same class.
const exportErrorLogInterval = 10 * time.Minute

// maxErrorClasses bounds the per-class table.
const maxErrorClasses = 32

// rateLimitedErrorHandler is an otel.ErrorHandler that logs each distinct
// error class at least once per interval and drops repeats of a class inside
// it, saying how many it dropped.
//
// Why: a periodic OTLP export that fails (collector down, TLS mismatch) hands
// the SDK's error handler one error per attempt, and the default handler
// writes each to the stdlib log, which this server routes into the slog file.
// At the 5s minimum interval that is 17,280 lines a day for one dead
// collector. One shared bucket would let a dead metric collector hide a
// different trace auth/TLS error, so the limiter is keyed by error class: the
// redacted message with digit runs normalised (ports, counts, durations,
// addresses). The table holds at most maxErrorClasses classes (plus the one overflow bucket); when it is
// full, expired classes are dropped first and, if none is expired, new
// classes share one "other" bucket. The handler is process-global
// (otel.SetErrorHandler), so it also covers the trace exporter; nothing else
// in this repo sets one.
type rateLimitedErrorHandler struct {
	interval time.Duration
	now      func() time.Time
	log      func(level slog.Level, msg string, attrs ...any)

	mu      sync.Mutex
	classes map[string]*errorClass
}

type errorClass struct {
	last       time.Time
	suppressed int
}

func newRateLimitedErrorHandler(interval time.Duration, now func() time.Time, log func(slog.Level, string, ...any)) *rateLimitedErrorHandler {
	return &rateLimitedErrorHandler{interval: interval, now: now, log: log, classes: map[string]*errorClass{}}
}

var digitRunRE = regexp.MustCompile(`[0-9]+`)

// errorClassKey normalises a redacted error message into its class.
func errorClassKey(redacted string) string { return digitRunRE.ReplaceAllString(redacted, "#") }

// Handle implements otel.ErrorHandler.
func (h *rateLimitedErrorHandler) Handle(err error) {
	if err == nil {
		return
	}
	text := redactEndpointSecrets(err.Error())
	key := errorClassKey(text)

	h.mu.Lock()
	t := h.now()
	c, ok := h.classes[key]
	if !ok {
		if len(h.classes) >= maxErrorClasses {
			for k, v := range h.classes {
				if t.Sub(v.last) >= h.interval {
					delete(h.classes, k)
				}
			}
		}
		if len(h.classes) >= maxErrorClasses {
			key = "(other)"
			c, ok = h.classes[key]
		}
		if !ok {
			c = &errorClass{}
			h.classes[key] = c
			c.last = t.Add(-h.interval - time.Nanosecond) // log immediately
		}
	}
	if t.Sub(c.last) < h.interval {
		c.suppressed++
		h.mu.Unlock()
		return
	}
	suppressed := c.suppressed
	c.suppressed, c.last = 0, t
	h.mu.Unlock()

	h.log(slog.LevelError, "OpenTelemetry error (rate limited)",
		"error", text,
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
// parseOTLPEndpoint, which refuses such endpoints outright. This is only a
// backstop for third-party text (gRPC errors) that goes through the handler.
func redactEndpointSecrets(s string) string {
	return urlTokenRE.ReplaceAllStringFunc(s, func(tok string) string {
		i := strings.Index(tok, "://")
		head, rest := tok[:i+3], tok[i+3:]
		rest = strings.TrimLeft(rest, "/")
		head += strings.Repeat("/", len(tok)-len(head)-len(rest))
		if strings.EqualFold(strings.TrimRight(head, ":/"), "dns") {
			// dns:/// has no separate authority: the target name is the path.
			// Userinfo is everything through the last '@' (before any
			// query), then the query and fragment go.
			if qi := strings.IndexAny(rest, "?#"); qi >= 0 && qi > strings.LastIndex(rest, "@") {
				rest = rest[:qi]
			}
			if at := strings.LastIndex(rest, "@"); at >= 0 {
				rest = rest[at+1:]
			}
			if qi := strings.IndexAny(rest, "?#"); qi >= 0 {
				rest = rest[:qi]
			}
		} else {
			// Same rule as parseHTTPEndpoint: the authority ends at the first
			// '/', '?' or '#'; userinfo lives only inside it; everything
			// after host[:port] (path, query, fragment) can hold a token.
			if end := strings.IndexAny(rest, "/?#"); end >= 0 {
				rest = rest[:end]
			}
			if at := strings.LastIndex(rest, "@"); at >= 0 {
				rest = rest[at+1:]
			}
		}
		return head + rest
	})
}
