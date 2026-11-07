// file: internal/telemetry/metric_env_isolation_test.go
// version: 1.6.0
// guid: c3a71e58-04bd-4f92-9e6a-5d18b2f7a0c4
// last-edited: 2026-11-07

package telemetry

// The OTLP environment-isolation tests moved to core/metrics/otlp_test.go
// with the exporter wiring they test (R1-10). What stays is ABO's: the
// process-wide error handler, its redaction backstop, the start-up summary
// and the shutdown fan-out.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"

	"github.com/falkcorp/mediacore/metrics"
)

func TestRateLimitedErrorHandler(t *testing.T) {
	type line struct {
		msg   string
		attrs []any
	}
	var lines []line
	now := time.Unix(1_000, 0)
	h := newRateLimitedErrorHandler(10*time.Minute, func() time.Time { return now },
		func(_ slog.Level, msg string, attrs ...any) { lines = append(lines, line{msg, attrs}) })

	h.Handle(nil) // ignored
	h.Handle(errors.New("export failed: dial https://user:pw@collector.example.invalid:4317/?k=SECRET"))
	for i := 0; i < 5; i++ {
		now = now.Add(time.Minute)
		// Same class: only the digits differ.
		h.Handle(errors.New("export failed: dial https://user:pw@collector.example.invalid:431" + string(rune('0'+i)) + "/?k=SECRET"))
	}
	if len(lines) != 1 {
		t.Fatalf("logged %d lines for one class inside the interval, want 1", len(lines))
	}
	if lines[0].msg != "OpenTelemetry error (rate limited)" {
		t.Errorf("msg = %q", lines[0].msg)
	}
	got := fmt.Sprint(lines[0].attrs...)
	if strings.Contains(got, "user:pw") || strings.Contains(got, "SECRET") {
		t.Errorf("secrets leaked into the log line: %s", got)
	}

	// A DIFFERENT class in the same window is logged at once.
	h.Handle(errors.New("rpc error: code = Unauthenticated desc = bad token"))
	if len(lines) != 2 {
		t.Fatalf("a distinct error class inside the window logged %d lines in total, want 2", len(lines))
	}

	now = now.Add(10 * time.Minute)
	h.Handle(errors.New("export failed: dial https://collector.example.invalid:4317"))
	if len(lines) != 3 {
		t.Fatalf("logged %d lines after the interval, want 3", len(lines))
	}
	if lines[2].attrs[2] != "suppressed_since_last" || lines[2].attrs[3] != 5 {
		t.Errorf("third line attrs = %v, want suppressed_since_last=5", lines[2].attrs)
	}
}

func TestRateLimitedErrorHandler_ClassTableIsBounded(t *testing.T) {
	n := 0
	now := time.Unix(1_000, 0)
	h := newRateLimitedErrorHandler(time.Hour, func() time.Time { return now },
		func(slog.Level, string, ...any) { n++ })
	for i := 0; i < maxErrorClasses+20; i++ {
		h.Handle(errors.New("distinct class " + strings.Repeat("x", i) + "y"))
	}
	if len(h.classes) > maxErrorClasses+1 { // the classes plus the overflow bucket
		t.Errorf("class table holds %d entries, want at most %d", len(h.classes), maxErrorClasses+1)
	}
	// 32 classes plus the shared overflow bucket's first line.
	if n != maxErrorClasses+1 {
		t.Errorf("logged %d lines, want %d", n, maxErrorClasses+1)
	}
}

func TestInitSummary_BothHalvesOffKeepsBothMessages(t *testing.T) {
	level, msg, attrs := initSummary(true, false, false, true, true)
	if level != slog.LevelError {
		t.Errorf("level = %v, want error", level)
	}
	if !strings.Contains(msg, "tracing OFF") || !strings.Contains(msg, "OTLP metric push OFF") {
		t.Errorf("msg = %q, want both clauses", msg)
	}
	got := map[string]any{}
	for i := 0; i+1 < len(attrs); i += 2 {
		got[attrs[i].(string)] = attrs[i+1]
	}
	// The reason is core's degraded token, never error text: the configured
	// endpoint (and any userinfo in it) cannot reach this line.
	if got["tracing_error"] != metrics.DegradedTracingDisabled || got["otlp_metrics_error"] != metrics.DegradedOTLPDisabled {
		t.Errorf("attrs = %v, want the degraded tokens", got)
	}
	if got["metrics"] != true || got["tracing"] != false || got["otlp_metrics"] != false {
		t.Errorf("attrs = %v", got)
	}

	level, msg, _ = initSummary(true, true, true, false, false)
	if level != slog.LevelInfo || msg != "OpenTelemetry initialized" {
		t.Errorf("all on: level %v msg %q", level, msg)
	}
}

func TestRedactUserinfo(t *testing.T) {
	for name, tc := range map[string][2]string{
		"url":                 {"https://u:p@h:4317", "https://h:4317"},
		"quoted-in-error":     {"endpoint \"http://u@h:1\" is bad", "endpoint \"http://h:1\" is bad"},
		"dns-triple-slash":    {"dns:///u:secret@h:4317", "dns:///h:4317"},
		"password-with-slash": {"dns:///u:pa/ss@h:4317", "dns:///h:4317"},
		"password-with-at":    {"https://u:p@ss@h:4317", "https://h:4317"},
		"two-occurrences":     {"a https://u:one@h:1 b dns:///v:two@g:2 c", "a https://h:1 b dns:///g:2 c"},
		"userinfo+query":      {"https://u:p@h:4317/?api_key=SECRET", "https://h:4317"},
		"fragment":            {"https://h:4317/#token=SECRET", "https://h:4317"},
		"query+fragment":      {"https://h:4317?a=SECRET#b=SECRET", "https://h:4317"},
		"all-three":           {"dns:///u:p@h:4317?k=SECRET#f=SECRET", "dns:///h:4317"},
		"query-with-at":       {"https://h:4317/?mail=a@b", "https://h:4317"},
		"path-token":          {"https://h:4317/v1/SECRET", "https://h:4317"},
		"unknown-scheme-path": {"grpc://h:4317/SECRET", "grpc://h:4317"},
		"dns-keeps-target":    {"dns:///h:4317/name", "dns:///h:4317/name"},
		"in-error-text":       {"dial \"https://u:p@h:4317/x?k=SECRET\": refused", "dial \"https://h:4317\": refused"},
		"control-bare":        {"h:4317", "h:4317"},
		"control-url":         {"https://h:4317", "https://h:4317"},
		"control-dns":         {"dns:///h:4317", "dns:///h:4317"},
		"control-text":        {"connection refused", "connection refused"},
	} {
		if got := redactEndpointSecrets(tc[0]); got != tc[1] {
			t.Errorf("%s: redactEndpointSecrets(%q) = %q, want %q", name, tc[0], got, tc[1])
		}
	}
}

// An endpoint with userinfo must never put the secret in what reaches the
// log: neither core's own lines (Options.Logger, which the façade routes
// through emit to the process logger) nor the process-wide error handler.
func TestOTLPMetrics_DialErrorNeverLogsUserinfo(t *testing.T) {
	var mu sync.Mutex
	var logged []string
	h := newRateLimitedErrorHandler(0, time.Now, func(_ slog.Level, msg string, attrs ...any) {
		mu.Lock()
		defer mu.Unlock()
		logged = append(logged, msg)
		for _, a := range attrs {
			logged = append(logged, fmt.Sprint(a))
		}
	})
	prev := otel.GetErrorHandler()
	otel.SetErrorHandler(h)
	t.Cleanup(func() { otel.SetErrorHandler(prev) })

	var buf syncBuffer
	prevLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prevLog) })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cfg := LoadConfig("test", "https://user:secret@192.0.2.1:4317", WithMetricsOTLP("dns:///user:secret@192.0.2.1:4317", time.Minute, true))
	o := metricsOptions(cfg)
	o.OTLP = &metrics.OTLPMetrics{Endpoint: cfg.MetricsOTLPEndpoint, Insecure: cfg.MetricsOTLPInsecure, Interval: cfg.MetricsOTLPInterval}
	o.Tracing = &metrics.OTLPTracing{Endpoint: cfg.ExporterEndpoint}
	p, err := metrics.New(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if d := p.Degraded(); len(d) != 2 {
		t.Errorf("Degraded = %v, want both OTLP halves refused", d)
	}
	sdErr := p.Shutdown(ctx)
	if sdErr != nil {
		if strings.Contains(sdErr.Error(), "secret") {
			t.Errorf("shutdown error leaks userinfo: %v", sdErr)
		}
		otel.Handle(sdErr) // through the real handler path as well
	}
	if out := buf.String(); strings.Contains(out, "secret") || !strings.Contains(out, "not a valid OTLP/gRPC endpoint") {
		t.Errorf("core's lines through emit = %q, want the fixed refusal and no userinfo", out)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, l := range logged {
		if strings.Contains(l, "secret") {
			t.Errorf("log output leaks userinfo: %q", l)
		}
	}
}

// syncBuffer is a bytes.Buffer safe for the concurrent writes of a logger.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestRunShutdowns_EachGetsItsShare(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	var secondErr error
	err := runShutdowns(ctx, []func(context.Context) error{
		func(c context.Context) error { <-c.Done(); return c.Err() }, // a hung tracer
		func(c context.Context) error { secondErr = c.Err(); return nil },
	})
	if secondErr != nil {
		t.Errorf("the second shutdown started with a dead context: %v", secondErr)
	}
	if err == nil {
		t.Error("the hung shutdown's error was swallowed")
	}
}
