// file: internal/telemetry/telemetry.go
// version: 2.0.0
// guid: 2b3c4d5e-6f7a-8b9c-0d1e-2f3a4b5c6d7e
// last-edited: 2026-10-03

package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// InitOTEL initializes OpenTelemetry in two independent halves and returns one
// shutdown function covering whatever was started.
//
//   - Metrics (cfg.MetricsEnabled): an OTel meter provider backed by the
//     Prometheus exporter, which registers with the process-wide Prometheus
//     registry that /metrics already serves. This needs no endpoint and runs in
//     every deployment, so OTel-instrumented meters (otelgin, anything using
//     otel.Meter) land on the same scrape as internal/metrics.
//   - Tracing (cfg.TracingEnabled, i.e. an OTLP endpoint is set): an OTLP/gRPC
//     span exporter and tracer provider. Off in prod until a collector exists;
//     deploy/grafana/TRACING-RUNBOOK.md is how to stand one up.
//
// Until 2026-10-03 both halves were gated on the trace endpoint, so an empty
// OTEL_EXPORTER_OTLP_ENDPOINT -- the production default -- also disabled the
// meter provider, and no OTel metric instrument ever reached Prometheus.
//
// Nothing enabled returns a no-op shutdown.
func InitOTEL(ctx context.Context, cfg *Config) (func(context.Context) error, error) {
	var shutdowns []func(context.Context) error

	// Tracing is validated and started first: a bad endpoint fails the whole
	// init before any registry-global side effect (the Prometheus exporter
	// registration) has happened.
	if cfg.TracingEnabled {
		tp, err := initTracing(ctx, cfg)
		if err != nil {
			return nil, err
		}
		shutdowns = append(shutdowns, tp.Shutdown)
	}

	if cfg.MetricsEnabled {
		shutdownMetrics, err := initMetrics()
		if err != nil {
			return nil, err
		}
		shutdowns = append(shutdowns, shutdownMetrics)
	}

	slog.Info("OpenTelemetry initialized",
		"metrics", cfg.MetricsEnabled,
		"tracing", cfg.TracingEnabled,
		"endpoint", cfg.ExporterEndpoint)

	return func(shutdownCtx context.Context) error {
		var errs []error
		for _, fn := range shutdowns {
			if err := fn(shutdownCtx); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}, nil
}

// initTracing validates the OTLP endpoint, builds the gRPC span exporter and
// installs the tracer provider globally.
func initTracing(ctx context.Context, cfg *Config) (*sdktrace.TracerProvider, error) {
	// Validate endpoint format: must be a valid gRPC endpoint (host:port, dns://, or http(s)://)
	parsedURL, err := url.Parse(cfg.ExporterEndpoint)
	if err != nil {
		return nil, err
	}
	// Allow hostnames with ports, or valid URL schemes for gRPC (http, https, dns)
	if parsedURL.Scheme != "" && parsedURL.Scheme != "http" && parsedURL.Scheme != "https" && parsedURL.Scheme != "dns" {
		return nil, fmt.Errorf("invalid endpoint scheme %q: must be http, https, dns, or omitted for host:port", parsedURL.Scheme)
	}

	exporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpoint(cfg.ExporterEndpoint))
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(NewResource(cfg.ServiceName)),
	)
	otel.SetTracerProvider(tp)
	return tp, nil
}

// The Prometheus exporter registers a collector with the default Prometheus
// registry, and a second registration of the same collector is an error. The
// meter provider is therefore built once per process and reused by any later
// InitOTEL call (tests call it more than once; the server calls it once).
var (
	meterOnce     sync.Once
	meterProvider *metric.MeterProvider
	meterErr      error

	// meterShutdownOnce makes the returned shutdown idempotent for the same
	// reason: two InitOTEL callers share one provider, so the second Shutdown
	// must not report the reader as already shut down.
	meterShutdownOnce sync.Once
	meterShutdownErr  error
)

// initMetrics builds (once) the Prometheus-exporting meter provider, installs
// it globally and returns its idempotent shutdown.
func initMetrics() (func(context.Context) error, error) {
	meterOnce.Do(func() {
		exporter, err := prometheus.New()
		if err != nil {
			meterErr = err
			return
		}
		meterProvider = metric.NewMeterProvider(metric.WithReader(exporter))
	})
	if meterErr != nil {
		return nil, meterErr
	}
	otel.SetMeterProvider(meterProvider)
	return func(ctx context.Context) error {
		meterShutdownOnce.Do(func() { meterShutdownErr = meterProvider.Shutdown(ctx) })
		return meterShutdownErr
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
