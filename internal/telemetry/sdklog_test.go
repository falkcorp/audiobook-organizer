// file: internal/telemetry/sdklog_test.go
// version: 1.1.0
// guid: 7c1a9e43-2b86-4d05-a3f7-d84e60b19c2a
// last-edited: 2026-10-10

package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	"go.opentelemetry.io/otel"
)

// capture collects what a logger or log func emits.
type capture struct {
	mu    sync.Mutex
	lines []string
}

func (c *capture) add(s string) { c.mu.Lock(); c.lines = append(c.lines, s); c.mu.Unlock() }
func (c *capture) all() string  { c.mu.Lock(); defer c.mu.Unlock(); return strings.Join(c.lines, "\n") }

// hostile sets the environment the SDK's own logger reports: an unparsable
// OTEL_EXPORTER_OTLP_ENDPOINT and a malformed OTEL_EXPORTER_OTLP_HEADERS,
// each carrying the sentinel.
func hostile(t *testing.T) {
	t.Helper()
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "https://user:p^ss_"+sentinel+"@tempo:4317")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "malformed_"+sentinel)
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_HEADERS", "malformed2_"+sentinel)
	// strconv's error for a bad duration quotes its input: this one catches a
	// sink that logs err.Error().
	t.Setenv("OTEL_EXPORTER_OTLP_TIMEOUT", "x"+sentinel)
}

func buildExporters(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cfg := LoadConfig("t", "127.0.0.1:4317", WithMetricsOTLP("http://127.0.0.1:4318", time.Minute, false))
	if tp, err := initTracing(ctx, cfg); err == nil {
		_ = tp.Shutdown(ctx)
	}
	mp, _, err := newMeterProvider(ctx, cfg, privateProm())
	if err != nil {
		t.Fatal(err)
	}
	_ = mp.Shutdown(ctx)
}

// Control: with a plain value-printing logger the SDK DOES print the raw env
// input. This proves the test below can see the leak it guards against.
func TestSDKLog_ControlRawLoggerLeaks(t *testing.T) {
	hostile(t)
	c := &capture{}
	otel.SetLogger(funcr.New(func(prefix, args string) { c.add(prefix + args) }, funcr.Options{Verbosity: 10}))
	// There is no getter for the previous logger; restore the discard logger.
	t.Cleanup(func() { otel.SetLogger(logr.Discard()) })
	buildExporters(t)
	if !strings.Contains(c.all(), sentinel) {
		t.Skipf("the SDK did not log the hostile input with a raw logger (%q); nothing to guard", c.all())
	}
}

func TestSDKLogSink_DropsValues(t *testing.T) {
	hostile(t)
	out := &capture{}
	h := newRateLimitedErrorHandler(time.Minute, time.Now, func(l slog.Level, m string, a ...any) {
		out.add(fmt.Sprint(l, m, a))
	})
	otel.SetLogger(newSDKLogger(h))
	t.Cleanup(func() { otel.SetLogger(logr.Discard()) })
	buildExporters(t)
	if strings.Contains(out.all(), sentinel) {
		t.Fatalf("the SDK log sink let the sentinel through:\n%s", out.all())
	}
	if !strings.Contains(out.all(), "value keys:") {
		t.Errorf("nothing from the SDK logger reached the sink (it should log message and key names):\n%s", out.all())
	}
}

func TestSDKLogSink_LevelsAndValues(t *testing.T) {
	out := &capture{}
	h := newRateLimitedErrorHandler(time.Minute, time.Now, func(l slog.Level, m string, a ...any) { out.add(fmt.Sprint(l, m, a)) })
	lg := newSDKLogger(h)
	lg.V(4).Info("debug chatter", "input", sentinel) // off at default verbosity
	lg.V(1).Info("a warning", "value", sentinel)
	lg.Error(fmt.Errorf("parse %s", sentinel), "boom", "input", sentinel)
	got := out.all()
	if strings.Contains(got, sentinel) || strings.Contains(got, "debug chatter") {
		t.Errorf("sink output wrong:\n%s", got)
	}
	if !strings.Contains(got, "a warning") || !strings.Contains(got, "boom") || !strings.Contains(got, "value keys: input") {
		t.Errorf("sink dropped the message or key names:\n%s", got)
	}
}

func TestRunShutdowns_SkipsNil(t *testing.T) {
	called := false
	err := runShutdowns(context.Background(), []func(context.Context) error{nil, func(context.Context) error { called = true; return nil }})
	if err != nil || !called {
		t.Errorf("err=%v called=%v, want nil/true", err, called)
	}
}

// InitOTEL itself installs the sink: the logger it hands to otel.SetLogger is
// ours, and installing happens before any exporter is built.
func TestInitOTEL_InstallsTheSDKLogSink(t *testing.T) {
	hostile(t)
	var installed []logr.Logger
	prevSet := setOTelLogger
	setOTelLogger = func(l logr.Logger) { installed = append(installed, l); prevSet(l) }
	installSDKLogOnce = sync.Once{} // other tests have already run InitOTEL
	t.Cleanup(func() {
		setOTelLogger = prevSet
		otel.SetLogger(logr.Discard())
	})

	if _, err := InitOTEL(context.Background(), LoadConfig("t", "")); err != nil {
		t.Fatal(err)
	}
	if len(installed) != 1 {
		t.Fatalf("InitOTEL installed %d SDK loggers, want 1", len(installed))
	}
	if _, ok := installed[0].GetSink().(*sdkLogSink); !ok {
		t.Fatalf("installed sink is %T, want *sdkLogSink", installed[0].GetSink())
	}
}
