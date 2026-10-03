// file: internal/telemetry/telemetry.go
// version: 2.1.0
// guid: 2b3c4d5e-6f7a-8b9c-0d1e-2f3a4b5c6d7e
// last-edited: 2026-10-03

package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"
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

	// Tracing is started first and is never fatal. A wrong endpoint, or an
	// exporter that cannot be built, costs the traces and nothing else: it is
	// logged at error level and the server starts with tracing off. Until
	// 2026-10-03 the error was returned, cmd/root.go made it fatal, and
	// setting OTEL_EXPORTER_OTLP_ENDPOINT=127.0.0.1:4317 in prod (the form
	// this package's own comment promised to accept) put the server in a
	// crash loop for 75 seconds.
	tracing := false
	if cfg.TracingEnabled {
		tp, err := initTracing(ctx, cfg)
		if err != nil {
			slog.Error("OpenTelemetry tracing is OFF: the trace exporter could not be started",
				"endpoint", cfg.ExporterEndpoint, "error", err)
		} else {
			tracing = true
			shutdowns = append(shutdowns, tp.Shutdown)
		}
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
		"tracing", tracing,
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

// traceEndpointOption turns the configured endpoint into the exporter option
// that reaches it. Two forms are accepted:
//
//   - a URL, "http://host:port" or "https://host:port": the form the OTel
//     spec gives OTEL_EXPORTER_OTLP_ENDPOINT, which the SDK also reads for
//     itself. "http" is plaintext gRPC, "https" is TLS.
//   - a bare "host:port" (or a "dns:///host:port" gRPC target): TLS unless
//     OTEL_EXPORTER_OTLP_TRACES_INSECURE or OTEL_EXPORTER_OTLP_INSECURE is
//     true, which the SDK honours.
//
// The old check ran url.Parse on the bare form, which rejects "127.0.0.1:4317"
// ("first path segment in URL cannot contain colon") and reads "localhost" in
// "localhost:4317" as a scheme, so no bare endpoint ever passed; and a URL
// that did pass was handed to WithEndpoint, which wants host:port.
func traceEndpointOption(endpoint string) (otlptracegrpc.Option, error) {
	ep := strings.TrimSpace(endpoint)
	if scheme, rest, ok := strings.Cut(ep, "://"); ok {
		switch strings.ToLower(scheme) {
		case "http", "https":
			u, err := url.Parse(ep)
			if err != nil {
				return nil, fmt.Errorf("endpoint %q is not a URL: %w", endpoint, err)
			}
			if u.Hostname() == "" || u.Port() == "" {
				return nil, fmt.Errorf("endpoint %q must name a host and a port", endpoint)
			}
			return otlptracegrpc.WithEndpointURL(ep), nil
		case "dns":
			if strings.TrimLeft(rest, "/") == "" {
				return nil, fmt.Errorf("endpoint %q names no target", endpoint)
			}
			return otlptracegrpc.WithEndpoint(ep), nil
		}
		return nil, fmt.Errorf("endpoint %q: scheme %q is not http, https or dns", endpoint, scheme)
	}
	host, port, err := net.SplitHostPort(ep)
	if err != nil || host == "" || port == "" {
		return nil, fmt.Errorf("endpoint %q is neither a URL nor host:port", endpoint)
	}
	return otlptracegrpc.WithEndpoint(ep), nil
}

// initTracing builds the gRPC span exporter for the configured endpoint and
// installs the tracer provider globally.
func initTracing(ctx context.Context, cfg *Config) (*sdktrace.TracerProvider, error) {
	opt, err := traceEndpointOption(cfg.ExporterEndpoint)
	if err != nil {
		return nil, err
	}
	exporter, err := otlptracegrpc.New(ctx, opt)
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
