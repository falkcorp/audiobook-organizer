// file: internal/config/otel_metrics_config_test.go
// version: 1.0.0
// guid: 8d1f5b3c-2e74-4a96-b0c8-6f3a9e1d7c52
// last-edited: 2026-10-10

package config

import (
	"testing"

	"github.com/spf13/viper"
)

var otelMetricsKeys = []string{
	"otel_metrics_otlp_endpoint", "otel_metrics_otlp_interval",
	"otel_metrics_otlp_insecure", "telemetry_environment",
}

// TestOTelMetricsConfig_DefaultsAreOff: with nothing set the OTLP metric push
// is off (empty endpoint), the interval is 60s and the environment is prod.
func TestOTelMetricsConfig_DefaultsAreOff(t *testing.T) {
	resetViper(t)
	for _, env := range []string{"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "OTEL_METRIC_EXPORT_INTERVAL", "OTEL_EXPORTER_OTLP_METRICS_INSECURE"} {
		t.Setenv(env, "")
	}
	InitConfig()
	defer func() { AppConfig = Config{} }()

	if got := viper.GetString("otel_metrics_otlp_endpoint"); got != "" {
		t.Errorf("otel_metrics_otlp_endpoint = %q, want empty", got)
	}
	if got := viper.GetString("otel_metrics_otlp_interval"); got != "60s" {
		t.Errorf("otel_metrics_otlp_interval = %q, want 60s", got)
	}
	if viper.GetBool("otel_metrics_otlp_insecure") {
		t.Error("otel_metrics_otlp_insecure defaulted to true")
	}
	if got := viper.GetString("telemetry_environment"); got != "prod" {
		t.Errorf("telemetry_environment = %q, want prod", got)
	}
}

func TestOTelMetricsConfig_EnvBindings(t *testing.T) {
	resetViper(t)
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "http://collector.example.invalid:4317")
	t.Setenv("OTEL_METRIC_EXPORT_INTERVAL", "30s")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_INSECURE", "true")
	InitConfig()
	defer func() { AppConfig = Config{} }()

	if got := viper.GetString("otel_metrics_otlp_endpoint"); got != "http://collector.example.invalid:4317" {
		t.Errorf("endpoint = %q", got)
	}
	if got := viper.GetString("otel_metrics_otlp_interval"); got != "30s" {
		t.Errorf("interval = %q", got)
	}
	if !viper.GetBool("otel_metrics_otlp_insecure") {
		t.Error("insecure not read from env")
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
