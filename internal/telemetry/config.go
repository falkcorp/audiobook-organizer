// file: internal/telemetry/config.go
// version: 1.0.0
// guid: 1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d

package telemetry

// Config holds OpenTelemetry configuration.
type Config struct {
	ExporterEndpoint string
	ServiceName      string
	Enabled          bool
}

// LoadConfig builds the OTEL config for serviceName from the given exporter
// endpoint (config.AppConfig.OTelExporterOTLPEndpoint / OTEL_EXPORTER_OTLP_ENDPOINT
// at the caller). Telemetry stays free of an internal/config import; the caller
// owns config resolution. Empty endpoint disables OTEL.
func LoadConfig(serviceName, exporterEndpoint string) *Config {
	return &Config{
		ExporterEndpoint: exporterEndpoint,
		ServiceName:      serviceName,
		Enabled:          exporterEndpoint != "",
	}
}
