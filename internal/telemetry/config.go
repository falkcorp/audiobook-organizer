// file: internal/telemetry/config.go
// version: 2.0.1
// guid: 1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d
// last-edited: 2026-10-03

package telemetry

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
}

// LoadConfig builds the OTEL config for serviceName from the given exporter
// endpoint (config.AppConfig.OTelExporterOTLPEndpoint / OTEL_EXPORTER_OTLP_ENDPOINT
// at the caller). Telemetry stays free of an internal/config import; the caller
// owns config resolution. Metrics are always enabled; an empty endpoint
// disables only tracing.
func LoadConfig(serviceName, exporterEndpoint string) *Config {
	return &Config{
		ExporterEndpoint: exporterEndpoint,
		ServiceName:      serviceName,
		MetricsEnabled:   true,
		TracingEnabled:   exporterEndpoint != "",
	}
}
