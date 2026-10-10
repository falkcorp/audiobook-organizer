// file: internal/telemetry/sdklog.go
// version: 1.0.0
// guid: 5b7e2d90-14c8-4a3f-8d61-e9a0c3f72b58
// last-edited: 2026-10-10

package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel"
)

// The OTel SDK has its own logger (otel/internal/global), separate from the
// ErrorHandler. It reports bad configuration there with the RAW offending
// input as a key/value: an unparsable OTEL_EXPORTER_OTLP_ENDPOINT as
// ("parse url", "input", <value>), a malformed OTEL_EXPORTER_OTLP_HEADERS
// likewise. Left alone, the default stdr logger writes those values to stderr
// and from there into the journal, bypassing every redaction here.
//
// installSDKLogSink replaces it, process-wide and once, with a logr sink that
// keeps the message and the KEY NAMES and drops every value (and the error
// text, which url.Parse builds from its input). Verbosity: the SDK logs
// warnings at V(1), info at V(4) and debug at V(5), and errors through
// Error(). Only errors and warnings are logged, all at warn level; info and
// debug chatter stays off. Lines go through the same class-keyed limiter as
// the error handler.

var installSDKLogOnce sync.Once

func installSDKLogSink() {
	installSDKLogOnce.Do(func() {
		otel.SetLogger(newSDKLogger(newRateLimitedErrorHandler(exportErrorLogInterval, time.Now,
			func(level slog.Level, msg string, attrs ...any) {
				emit(context.Background(), level, msg, attrs...)
			})))
	})
}

func newSDKLogger(h *rateLimitedErrorHandler) logr.Logger {
	return logr.New(&sdkLogSink{h: h})
}

type sdkLogSink struct{ h *rateLimitedErrorHandler }

func (s *sdkLogSink) Init(logr.RuntimeInfo) {}

// Enabled: errors (V(0) in logr terms) and the SDK's warning level V(1).
func (s *sdkLogSink) Enabled(level int) bool { return level <= 1 }

func (s *sdkLogSink) Info(_ int, msg string, kv ...any) { s.log(msg, kv) }

func (s *sdkLogSink) Error(_ error, msg string, kv ...any) { s.log(msg, kv) }

func (s *sdkLogSink) WithValues(...any) logr.LogSink { return s }

func (s *sdkLogSink) WithName(string) logr.LogSink { return s }

// log keeps the message (capped, redacted) and the key names. Values and the
// error are never read.
func (s *sdkLogSink) log(msg string, kv []any) {
	var keys []string
	for i := 0; i < len(kv); i += 2 {
		if k, ok := kv[i].(string); ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	text := fmt.Sprintf("%s (value keys: %s)", capString(msg, 200), strings.Join(keys, ","))
	s.h.handleAt(slog.LevelWarn, "OpenTelemetry SDK log (values dropped, rate limited)", text)
}

func capString(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
