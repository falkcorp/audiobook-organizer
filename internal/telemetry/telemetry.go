// file: internal/telemetry/telemetry.go
// version: 1.0.1
// guid: 2b3c4d5e-6f7a-8b9c-0d1e-2f3a4b5c6d7e

package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// InitOTEL initializes OpenTelemetry. If OTEL_EXPORTER_OTLP_ENDPOINT is not set,
// returns a no-op shutdown function. Otherwise, initializes OTLP tracer + Prometheus exporter.
func InitOTEL(ctx context.Context, cfg *Config) (func(context.Context) error, error) {
	if !cfg.Enabled {
		// No-op mode: endpoint not configured
		return func(context.Context) error { return nil }, nil
	}

	// Validate endpoint format: must be a valid gRPC endpoint (host:port, dns://, or http(s)://)
	parsedURL, err := url.Parse(cfg.ExporterEndpoint)
	if err != nil {
		return nil, err
	}
	// Allow hostnames with ports, or valid URL schemes for gRPC (http, https, dns)
	if parsedURL.Scheme != "" && parsedURL.Scheme != "http" && parsedURL.Scheme != "https" && parsedURL.Scheme != "dns" {
		return nil, fmt.Errorf("invalid endpoint scheme %q: must be http, https, dns, or omitted for host:port", parsedURL.Scheme)
	}

	// Initialize OTLP trace exporter.
	exporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpoint(cfg.ExporterEndpoint))
	if err != nil {
		return nil, err
	}

	// Initialize trace provider with the exporter.
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(NewResource(cfg.ServiceName)),
	)
	otel.SetTracerProvider(tp)

	// Initialize Prometheus exporter for metrics.
	prometheusExporter, err := prometheus.New()
	if err != nil {
		return nil, err
	}

	// Initialize meter provider with the Prometheus exporter.
	meterProvider := metric.NewMeterProvider(metric.WithReader(prometheusExporter))
	otel.SetMeterProvider(meterProvider)

	slog.Info("OpenTelemetry initialized", "endpoint", cfg.ExporterEndpoint)

	// Return shutdown function that closes both exporters.
	return func(shutdownCtx context.Context) error {
		if err := tp.Shutdown(shutdownCtx); err != nil {
			return err
		}
		if err := meterProvider.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return nil
	}, nil
}

// GlobalTracer returns the global OpenTelemetry tracer.
func GlobalTracer() any {
	return otel.Tracer("audiobook-organizer")
}

// GlobalMeter returns the global OpenTelemetry meter.
func GlobalMeter() any {
	return otel.Meter("audiobook-organizer")
}
