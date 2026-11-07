// file: internal/telemetry/metric_otlp_test.go
// version: 2.0.0
// guid: 4c9e2a7d-1b63-4f08-a5d2-7e3b9c0f6a18
// last-edited: 2026-11-07

package telemetry

// The OTLP reader, endpoint-form, interval-clamp and resource tests moved to
// core/metrics with the code they test (R1-10). What stays is ABO's: which
// Config fields feed the metric push, that a bad push endpoint never fails
// InitOTEL, and the OTEL_METRIC_EXPORT_INTERVAL parser.

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"

	"github.com/falkcorp/mediacore/metrics"
)

func TestMetricsProviderOptions_OTLPOffByDefault(t *testing.T) {
	o := metricsProviderOptions(LoadConfig("test", ""))
	if !o.Prometheus || o.OTLP != nil || o.Tracing != nil {
		t.Fatalf("options = %+v, want Prometheus only", o)
	}
}

func TestMetricsEndpointNeverFallsBackToTraceEndpoint(t *testing.T) {
	o := metricsProviderOptions(LoadConfig("test", "http://192.0.2.10:4317")) // trace endpoint set, metrics empty
	if o.OTLP != nil {
		t.Fatalf("OTLP = %+v: the trace endpoint must never feed the metric push", o.OTLP)
	}
}

func TestMetricsProviderOptions_CarryThePushConfig(t *testing.T) {
	cfg := LoadConfig("test-svc", "", WithMetricsOTLP("http://192.0.2.10:4317", 5*time.Second, true), WithEnvironment("staging"))
	o := metricsProviderOptions(cfg)
	want := metrics.OTLPMetrics{Endpoint: "http://192.0.2.10:4317", Insecure: true, Interval: 5 * time.Second}
	if o.OTLP == nil || *o.OTLP != want {
		t.Fatalf("OTLP = %+v, want %+v", o.OTLP, want)
	}
	if o.ServiceName != "test-svc" || o.Environment != "staging" || o.MetricNamespace != metricNamespace || o.AppID != appID {
		t.Errorf("identity = %+v", o)
	}
}

// TestInitOTEL_BadMetricEndpointIsNotFatal: an unusable metrics endpoint costs
// the OTLP copy only; init succeeds and /metrics keeps serving.
func TestInitOTEL_BadMetricEndpointIsNotFatal(t *testing.T) {
	for _, ep := range []string{"://x", "ftp://h:1", "nohostport", "http://host"} {
		cfg := LoadConfig("test", "", WithMetricsOTLP(ep, time.Minute, false))
		shutdown, err := InitOTEL(context.Background(), cfg)
		if err != nil {
			t.Fatalf("InitOTEL(%q) = %v; a bad metrics endpoint must not fail init", ep, err)
		}
		if shutdown == nil {
			t.Fatalf("InitOTEL(%q) returned a nil shutdown", ep)
		}
		// The shutdown is deliberately not called: InitOTEL shares one
		// process-wide provider, and shutting it down here would starve the
		// later tests that scrape it (TestInitOTEL_NoEndpoint_StillInstallsMeterProvider).

		// The same options through a Provider of the test's own: the failure
		// is a degraded token, not an error, and the Prometheus reader still
		// serves.
		p, err := metrics.New(context.Background(), metricsProviderOptions(cfg))
		if err != nil {
			t.Fatalf("metrics.New(%q) = %v", ep, err)
		}
		if !slices.Contains(p.Degraded(), metrics.DegradedOTLPDisabled) {
			t.Errorf("Degraded for %q = %v, want %s", ep, p.Degraded(), metrics.DegradedOTLPDisabled)
		}
		c, _ := p.MeterProvider().Meter("bad-ep").Int64Counter("bad_ep_things")
		c.Add(context.Background(), 1)
		mfs, err := p.Gatherer().Gather()
		if err != nil {
			t.Fatal(err)
		}
		if !slices.ContainsFunc(mfs, func(mf *dto.MetricFamily) bool { return strings.HasPrefix(mf.GetName(), "bad_ep_things") }) {
			t.Errorf("/metrics lost the instrument after bad endpoint %q", ep)
		}
		_ = p.Shutdown(context.Background())
	}
}

func TestParseMetricsInterval(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"60s": 60 * time.Second, "": 0, "abc": 0, "-5s": 0, "0s": 0, "2m": 2 * time.Minute,
		// OTEL_METRIC_EXPORT_INTERVAL's standard form: integer milliseconds.
		"60000": 60 * time.Second, " 30000 ": 30 * time.Second, "5000": 5 * time.Second,
		"0": 0, "-1000": 0, "1.5": 0,
		"99999999999999999": 24 * time.Hour, // capped, not overflowed; core clamps it to 1h
	} {
		if got := ParseMetricsInterval(in); got != want {
			t.Errorf("ParseMetricsInterval(%q) = %v, want %v", in, got, want)
		}
	}
}
