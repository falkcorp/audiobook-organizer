// file: internal/config/otel_metrics_config_test.go
// version: 1.0.0
// guid: 8d1f5b3c-2e74-4a96-b0c8-6f3a9e1d7c52
// last-edited: 2026-10-10

package config

import (
	"testing"
	"time"
)

var otelMetricsKeys = []string{
	"otel_metrics_otlp_endpoint", "otel_metrics_otlp_interval",
	"otel_metrics_otlp_insecure", "telemetry_environment",
}

// TestOTelMetricsConfig_DefaultsAreOff: with nothing set the OTLP metric push
// is off (empty endpoint), the interval is 60s and the environment is prod.
func TestOTelMetricsConfig_DefaultsAreOff(t *testing.T) {
	d := factoryDefaultConfigForTest()

	if got := d.OTelMetricsOTLPEndpoint; got != "" {
		t.Errorf("otel_metrics_otlp_endpoint = %q, want empty", got)
	}
	// A duration string: the projection spells core's typed default "1m",
	// HEAD's viper default was "60s"; the push loop parses either alike.
	if got, err := time.ParseDuration(d.OTelMetricsOTLPInterval); err != nil || got != time.Minute {
		t.Errorf("otel_metrics_otlp_interval = %q, want a 60s duration", d.OTelMetricsOTLPInterval)
	}
	if d.OTelMetricsOTLPInsecure {
		t.Error("otel_metrics_otlp_insecure defaulted to true")
	}
	if got := d.TelemetryEnvironment; got != "prod" {
		t.Errorf("telemetry_environment = %q, want prod", got)
	}
}

// TestOTelMetricsConfig_KeysAreClassified: all four keys carry an explicit
// protected_fields rule, none of them protected.
func TestOTelMetricsConfig_KeysAreClassified(t *testing.T) {
	for _, k := range otelMetricsKeys {
		rule, ok := configFieldRules[k]
		if !ok {
			t.Errorf("%s has no configFieldRules entry", k)
			continue
		}
		if rule.Class != FieldUnprotected {
			t.Errorf("%s classified %v, want FieldUnprotected", k, rule.Class)
		}
	}
}
