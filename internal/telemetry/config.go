// file: internal/telemetry/config.go
// version: 2.2.0
// guid: 1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d
// last-edited: 2026-10-10

package telemetry

import "time"

// Config holds OpenTelemetry configuration. The two halves are independent:
// metrics need no endpoint and are on wherever telemetry is on at all; tracing
// needs an OTLP collector and is on only when one is configured.
type Config struct {
	// ExporterEndpoint is the OTLP/gRPC collector for traces: a URL
	// ("http://host:port" plaintext, "https://host:port" TLS) or a bare
	// "host:port" / "dns:///host:port" (see traceEndpointOption). Empty
	// means no tracing; one that cannot be used turns tracing off and is
	// logged, it never stops the server.
	ExporterEndpoint string
	ServiceName      string
	// MetricsEnabled starts the Prometheus-exporting OTel meter provider.
	MetricsEnabled bool
	// TracingEnabled starts the OTLP span exporter; it is ExporterEndpoint != "".
	TracingEnabled bool

	// MetricsOTLPEndpoint is the OTLP/gRPC collector for the optional metric
	// push reader (same accepted forms as ExporterEndpoint). Empty means no
	// push. It is NEVER derived from ExporterEndpoint: the trace collector is
	// not assumed to accept metrics.
	MetricsOTLPEndpoint string
	// MetricsOTLPInterval is the push period; zero means the 60s default.
	// Values are clamped to 5s..1h.
	MetricsOTLPInterval time.Duration
	// MetricsOTLPInsecure allows plaintext gRPC to a bare host:port or a
	// dns:/// target. A URL decides for itself: http:// is plaintext and
	// https:// is TLS, whatever this says.
	MetricsOTLPInsecure bool
	// Environment is the deployment.environment resource attribute; empty
	// means "prod".
	Environment string
}

// ConfigOption customises LoadConfig.
type ConfigOption func(*Config)

// WithMetricsOTLP enables the optional OTLP metric reader. An empty endpoint
// leaves it off.
func WithMetricsOTLP(endpoint string, interval time.Duration, insecure bool) ConfigOption {
	return func(c *Config) {
		c.MetricsOTLPEndpoint = endpoint
		c.MetricsOTLPInterval = interval
		c.MetricsOTLPInsecure = insecure
	}
}

// WithEnvironment sets the deployment.environment resource attribute.
func WithEnvironment(env string) ConfigOption {
	return func(c *Config) { c.Environment = env }
}

// LoadConfig builds the OTEL config for serviceName from the given exporter
// endpoint (config.AppConfig.OTelExporterOTLPEndpoint / OTEL_EXPORTER_OTLP_ENDPOINT
// at the caller). Telemetry stays free of an internal/config import; the caller
// owns config resolution. Metrics are always enabled; an empty endpoint
// disables only tracing.
func LoadConfig(serviceName, exporterEndpoint string, opts ...ConfigOption) *Config {
	cfg := &Config{
		ExporterEndpoint: exporterEndpoint,
		ServiceName:      serviceName,
		MetricsEnabled:   true,
		TracingEnabled:   exporterEndpoint != "",
	}
	for _, opt := range opts {
		opt(cfg)
	}
	return cfg
}
