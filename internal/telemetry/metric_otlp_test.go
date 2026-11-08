// file: internal/telemetry/metric_otlp_test.go
// version: 3.0.0
// guid: 4c9e2a7d-1b63-4f08-a5d2-7e3b9c0f6a18
// last-edited: 2026-11-08

package telemetry

import (
	"testing"
	"time"
)

// The OTLP reader, endpoint-form, interval-clamp and resource tests live in
// core/metrics with the code they test; the install tests live with
// app.InstallProcessDefaults. What stays is ABO's OTEL_METRIC_EXPORT_INTERVAL
// parser.

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
