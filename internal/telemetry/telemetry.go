// file: internal/telemetry/telemetry.go
// version: 2.7.0
// guid: 2b3c4d5e-6f7a-8b9c-0d1e-2f3a4b5c6d7e
// last-edited: 2026-10-10

package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/grpc/credentials"
	grpcinsecure "google.golang.org/grpc/credentials/insecure"
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

	// Userinfo, query, fragment and (http/https) path are dropped from an
	// endpoint at parse time (gRPC never uses them). Say so once, naming the
	// key and the kind of thing dropped, never the value.
	var dropped []string
	for key, ep := range map[string]string{
		"otel_exporter_otlp_endpoint": cfg.ExporterEndpoint,
		"otel_metrics_otlp_endpoint":  cfg.MetricsOTLPEndpoint,
	} {
		if t, _ := parseOTLPEndpoint(ep); len(t.Dropped()) > 0 {
			dropped = append(dropped, key+"("+strings.Join(t.Dropped(), "+")+")")
		}
	}
	if len(dropped) > 0 {
		sort.Strings(dropped)
		emit(ctx, slog.LevelWarn, "OpenTelemetry endpoint extras were dropped: OTLP/gRPC does not use them", "keys", strings.Join(dropped, ","))
	}
	level, msg, attrs := initSummary(cfg, tracing, tracingErr, otlp)
	if otlp.Enabled {
		installExportErrorHandler()
	}
	emit(ctx, level, msg, attrs...)

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

// initSummary builds the one start-up log line for the whole init (this
// package is allowed one direct slog call, in emit: internal/logger's
// ratchet). Each half that could not be started adds its own clause to the
// message, so a start with both OFF says both. Endpoints and error text are
// redacted of URL userinfo.
func initSummary(cfg *Config, tracing bool, tracingErr error, otlp otlpStatus) (slog.Level, string, []any) {
	level, msg, attrs := slog.LevelInfo, "OpenTelemetry initialized", []any{
		"metrics", cfg.MetricsEnabled, "tracing", tracing, "endpoint", displayEndpoint(cfg.ExporterEndpoint),
		"otlp_metrics", otlp.Enabled}
	if cfg.MetricsOTLPEndpoint != "" {
		attrs = append(attrs, "otlp_metrics_endpoint", displayEndpoint(cfg.MetricsOTLPEndpoint))
	}
	var off []string
	if tracingErr != nil {
		off = append(off, "tracing OFF: the trace exporter could not be started")
		attrs = append(attrs, "tracing_error", redactEndpointSecrets(tracingErr.Error()))
	}
	if otlp.Err != nil {
		off = append(off, "the OTLP metric push OFF: /metrics is unaffected")
		attrs = append(attrs, "otlp_metrics_error", redactEndpointSecrets(otlp.Err.Error()))
	}
	if len(off) > 0 {
		level, msg = slog.LevelError, "OpenTelemetry initialized with "+strings.Join(off, "; ")
	}
	if otlp.IntervalNote != "" {
		attrs = append(attrs, "otlp_metrics_interval_note", otlp.IntervalNote)
	}
	return level, msg, attrs
}

// displayEndpoint is the loggable form of a configured endpoint: the parsed
// target's Display(), never the configured string. An endpoint that does not
// parse is reported as "(invalid)" rather than echoed.
func displayEndpoint(endpoint string) string {
	if strings.TrimSpace(endpoint) == "" {
		return ""
	}
	t, err := parseOTLPEndpoint(endpoint)
	if err != nil {
		return "(invalid)"
	}
	return t.Display()
}

// metricPlaintext is the one rule for whether the metric push uses plaintext
// gRPC:
//
//   - an http:// URL is plaintext and an https:// URL is TLS (the URL decides;
//     the insecure switch is not consulted);
//   - a bare host:port or a dns:/// target is TLS unless insecure
//     (otel_metrics_otlp_insecure) is true.
func metricPlaintext(t otlpTarget, insecure bool) bool {
	if t.URL != "" {
		return !strings.EqualFold(t.URL[:strings.Index(t.URL, "://")], "https")
	}
	return insecure
}

// metricEndpointOption is traceEndpointOption for the metric exporter: the
// endpoint plus an EXPLICIT transport, in every branch.
//
// otlpmetricgrpc reads the generic OTEL_EXPORTER_OTLP_* environment (the trace
// exporter's own variables) before it applies options, so an unset transport
// would inherit it: OTEL_EXPORTER_OTLP_ENDPOINT=http://... downgrades a TLS
// metric endpoint to plaintext, and OTEL_EXPORTER_OTLP_CERTIFICATE or
// *_CLIENT_CERTIFICATE install credentials that beat even WithInsecure (the
// exporter prefers credentials over its insecure flag). Options win over the
// environment, and credentials win over the insecure flag, so the transport is
// pinned with credentials, not with WithInsecure alone:
//
//   - plaintext (see metricPlaintext): insecure credentials. No environment
//     variable can turn this back into TLS.
//   - otherwise: TLS with the system root CAs. OTEL_EXPORTER_OTLP_CERTIFICATE
//     and the client-certificate variables are therefore NOT honoured for
//     metrics; use SSL_CERT_FILE / SSL_CERT_DIR for a private CA.
func metricEndpointOption(t otlpTarget, insecure bool) []otlpmetricgrpc.Option {
	var opts []otlpmetricgrpc.Option
	if t.URL != "" {
		opts = append(opts, otlpmetricgrpc.WithEndpointURL(t.URL))
	} else {
		opts = append(opts, otlpmetricgrpc.WithEndpoint(t.Target))
	}
	if metricPlaintext(t, insecure) {
		return append(opts, otlpmetricgrpc.WithInsecure(),
			otlpmetricgrpc.WithTLSCredentials(grpcinsecure.NewCredentials()))
	}
	return append(opts, otlpmetricgrpc.WithTLSCredentials(credentials.NewTLS(nil)))
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

// ParseMetricsInterval parses an OTEL_METRIC_EXPORT_INTERVAL value. The OTel
// standard for that variable is a bare integer count of MILLISECONDS ("60000"),
// and the SDK reads it too, so that form is accepted; a Go duration ("30s",
// "2m") is accepted as well. Empty, unparsable or non-positive input gives 0,
// which newMeterProvider treats as "use the 60s default".
func ParseMetricsInterval(s string) time.Duration {
	s = strings.TrimSpace(s)
	if ms, err := strconv.ParseInt(s, 10, 64); err == nil {
		const ceiling = 24 * time.Hour // keeps the multiplication from overflowing; clamped later
		if ms <= 0 {
			return 0
		}
		if ms > int64(ceiling/time.Millisecond) {
			return ceiling
		}
		return time.Duration(ms) * time.Millisecond
	}
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
	// Everything the generic OTEL_EXPORTER_OTLP_* environment could otherwise
	// change is pinned here (see metricEndpointOption for the transport):
	//   - headers: emptied, so the trace collector's credentials
	//     (OTEL_EXPORTER_OTLP_HEADERS) are never sent to the metric host.
	//     OTEL_EXPORTER_OTLP_METRICS_HEADERS is neutralised too: metric
	//     headers are not supported yet.
	//   - temporality: cumulative (owner decision D66), whatever
	//     OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE says.
	opts := append(metricEndpointOption(target, cfg.MetricsOTLPInsecure),
		otlpmetricgrpc.WithHeaders(map[string]string{}),
		otlpmetricgrpc.WithTemporalitySelector(metric.CumulativeTemporalitySelector))
	exp, err := otlpmetricgrpc.New(ctx, opts...)
	if err != nil {
		return nil, note, err
	}
	return metric.NewPeriodicReader(exp, metric.WithInterval(interval)), note, nil
}

// emit is this package's single direct slog call (internal/logger ratchet):
// InitOTEL's summary line and the export-error handler both go through it.
func emit(ctx context.Context, level slog.Level, msg string, attrs ...any) {
	slog.Log(ctx, level, msg, attrs...)
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
