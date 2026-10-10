// file: internal/telemetry/telemetry.go
// version: 2.4.0
// guid: 2b3c4d5e-6f7a-8b9c-0d1e-2f3a4b5c6d7e
// last-edited: 2026-10-10

package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
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
	var tracingErr error
	if cfg.TracingEnabled {
		tp, err := initTracing(ctx, cfg)
		if err != nil {
			tracingErr = err
		} else {
			tracing = true
			shutdowns = append(shutdowns, tp.Shutdown)
		}
	}

	// The OTLP metric push is an optional copy of the Prometheus instruments
	// and, like tracing, is never fatal: initMetrics reports it through
	// otlpStatus, not through the error, which is reserved for the Prometheus
	// reader itself.
	var otlp otlpStatus
	if cfg.MetricsEnabled {
		shutdownMetrics, status, err := initMetrics(ctx, cfg)
		if err != nil {
			return nil, err
		}
		otlp = status
		shutdowns = append(shutdowns, shutdownMetrics)
	}

	// One line for the whole init (this package is allowed one direct slog
	// call: internal/logger's ratchet). A trace exporter that could not be
	// started makes it an error-level line that says so.
	level, msg, attrs := slog.LevelInfo, "OpenTelemetry initialized", []any{
		"metrics", cfg.MetricsEnabled, "tracing", tracing, "endpoint", cfg.ExporterEndpoint,
		"otlp_metrics", otlp.Enabled}
	if tracingErr != nil {
		level, msg = slog.LevelError, "OpenTelemetry initialized with tracing OFF: the trace exporter could not be started"
		attrs = append(attrs, "tracing_error", tracingErr.Error())
	}
	if otlp.Err != nil {
		level = slog.LevelError
		msg = "OpenTelemetry initialized with the OTLP metric push OFF: /metrics is unaffected"
		attrs = append(attrs, "otlp_metrics_error", otlp.Err.Error())
	}
	if otlp.IntervalNote != "" {
		attrs = append(attrs, "otlp_metrics_interval_note", otlp.IntervalNote)
	}
	slog.Log(ctx, level, msg, attrs...)

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
	t, err := parseOTLPEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	if t.URL != "" {
		return otlptracegrpc.WithEndpointURL(t.URL), nil
	}
	return otlptracegrpc.WithEndpoint(t.Target), nil
}

// metricEndpointOption is traceEndpointOption for the metric exporter. Insecure
// (plaintext gRPC) applies only to a bare host:port: an http:// URL is already
// plaintext by the SDK's rule, an https:// URL is TLS, and a dns:/// target
// follows the SDK's own insecure environment switch.
func metricEndpointOption(t otlpTarget, insecure bool) []otlpmetricgrpc.Option {
	if t.URL != "" {
		return []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpointURL(t.URL)}
	}
	opts := []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(t.Target)}
	if t.Bare && insecure {
		opts = append(opts, otlpmetricgrpc.WithInsecure())
	}
	return opts
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
// process-wide meter provider is therefore built once and reused by any later
// InitOTEL call (tests call it more than once; the server calls it once).
// newMeterProvider itself has no such state, so tests build private providers.
var (
	meterOnce     sync.Once
	meterProvider *metric.MeterProvider
	meterOTLP     otlpStatus
	meterErr      error

	// meterShutdownOnce makes the returned shutdown idempotent for the same
	// reason: two InitOTEL callers share one provider, so the second Shutdown
	// must not report the reader as already shut down.
	meterShutdownOnce sync.Once
	meterShutdownErr  error
)

const (
	defaultMetricsInterval = 60 * time.Second
	minMetricsInterval     = 5 * time.Second
	maxMetricsInterval     = time.Hour
)

// otlpStatus reports what newMeterProvider did about the optional OTLP reader.
type otlpStatus struct {
	// Enabled: an OTLP periodic reader was installed.
	Enabled bool
	// Err: a non-fatal configuration or dial problem; the OTLP copy is off.
	Err error
	// Readers: total readers on the provider (1 = Prometheus alone).
	Readers int
	// IntervalNote: set when the configured interval was unset, invalid or
	// clamped.
	IntervalNote string
}

// ParseMetricsInterval parses an OTEL_METRIC_EXPORT_INTERVAL-style Go duration
// ("60s"). Empty, unparsable or non-positive input gives 0, which
// newMeterProvider treats as "use the 60s default".
func ParseMetricsInterval(s string) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

// clampInterval returns the effective push interval and a note when it
// differs from what was asked for.
func clampInterval(d time.Duration) (time.Duration, string) {
	switch {
	case d <= 0:
		return defaultMetricsInterval, "interval unset or invalid, using " + defaultMetricsInterval.String()
	case d < minMetricsInterval:
		return minMetricsInterval, "interval " + d.String() + " below minimum, using " + minMetricsInterval.String()
	case d > maxMetricsInterval:
		return maxMetricsInterval, "interval " + d.String() + " above maximum, using " + maxMetricsInterval.String()
	}
	return d, ""
}

// newMeterProvider builds a meter provider. Reader 1 is always the Prometheus
// exporter (promOpts lets tests give it a private registerer). Reader 2, an
// OTLP/gRPC periodic reader with cumulative temporality, exists only when
// cfg.MetricsOTLPEndpoint is set. That key is the only input: the trace
// endpoint (cfg.ExporterEndpoint) is deliberately never consulted.
//
// The OTLP reader is never fatal. A bad endpoint or an exporter that cannot be
// built is reported in otlpStatus.Err and the provider carries the Prometheus
// reader alone, with a nil error. Only the Prometheus reader failing returns
// an error. The gRPC dial is non-blocking, so an unreachable collector costs
// nothing at start-up.
func newMeterProvider(ctx context.Context, cfg *Config, promOpts ...prometheus.Option) (*metric.MeterProvider, otlpStatus, error) {
	exporter, err := prometheus.New(append([]prometheus.Option{prometheus.WithoutScopeInfo()}, promOpts...)...)
	if err != nil {
		return nil, otlpStatus{}, err
	}
	opts := append([]metric.Option{
		metric.WithReader(exporter),
		metric.WithResource(NewResourceWithEnvironment(cfg.ServiceName, cfg.Environment)),
	}, Views()...)
	status := otlpStatus{Readers: 1}

	if cfg.MetricsOTLPEndpoint != "" {
		reader, note, err := newOTLPReader(ctx, cfg)
		status.IntervalNote = note
		if err != nil {
			status.Err = err
		} else {
			opts = append(opts, metric.WithReader(reader))
			status.Enabled = true
			status.Readers = 2
		}
	}
	return metric.NewMeterProvider(opts...), status, nil
}

func newOTLPReader(ctx context.Context, cfg *Config) (metric.Reader, string, error) {
	target, err := parseOTLPEndpoint(cfg.MetricsOTLPEndpoint)
	if err != nil {
		return nil, "", err
	}
	interval, note := clampInterval(cfg.MetricsOTLPInterval)
	exp, err := otlpmetricgrpc.New(ctx, metricEndpointOption(target, cfg.MetricsOTLPInsecure)...)
	if err != nil {
		return nil, note, err
	}
	return metric.NewPeriodicReader(exp, metric.WithInterval(interval)), note, nil
}

// initMetrics builds (once) the process-wide meter provider, installs it
// globally and returns its idempotent shutdown and the OTLP status.
//
//   - WithoutScopeInfo: no otel_scope_name / otel_scope_version labels, so an
//     OTel family exports the same label set as the client_golang family it
//     replaces (spec 11 §3.3). target_info stays on.
//   - The resource carries service.name/version/instance.id and
//     deployment.environment onto target_info.
//   - Views() declares every histogram's buckets (views.go).
//
// The first caller's cfg wins: the provider is built once per process.
func initMetrics(ctx context.Context, cfg *Config) (func(context.Context) error, otlpStatus, error) {
	meterOnce.Do(func() {
		meterProvider, meterOTLP, meterErr = newMeterProvider(ctx, cfg)
	})
	if meterErr != nil {
		return nil, otlpStatus{}, meterErr
	}
	otel.SetMeterProvider(meterProvider)
	return func(ctx context.Context) error {
		meterShutdownOnce.Do(func() { meterShutdownErr = meterProvider.Shutdown(ctx) })
		return meterShutdownErr
	}, meterOTLP, nil
}
