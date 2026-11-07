// file: internal/telemetry/telemetry.go
// version: 3.0.0
// guid: 2b3c4d5e-6f7a-8b9c-0d1e-2f3a4b5c6d7e
// last-edited: 2026-11-07

package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"go.opentelemetry.io/otel"
	"google.golang.org/protobuf/proto"

	"github.com/falkcorp/mediacore/metrics"
)

// The provider construction, the OTLP endpoint validator, the exporter wiring
// and the resource are core's (mediacore/metrics, 05-metrics §13 item 1). This
// file is ABO's façade over it until the composition root (R1-15) replaces it
// with app.InstallProcessDefaults and the App's private registry: it builds
// core Providers from ABO's Config and keeps the PROCESS-GLOBAL installs core
// may not make (03 R5): otel.SetMeterProvider, otel.SetTracerProvider, the
// error handler and SDK logger (errorhandler.go, sdklog.go) and the bridge of
// the Provider's registry onto the default registry that /metrics serves.

// Identity values ABO hands core.
const (
	appID           = "abo"
	metricNamespace = "audiobook_organizer"
)

// InitOTEL initializes OpenTelemetry in two independent halves and returns one
// shutdown function covering whatever was started.
//
//   - Metrics (cfg.MetricsEnabled): a core Provider with the Prometheus
//     exporter, its registry bridged onto the process-wide Prometheus registry
//     that /metrics serves, and its MeterProvider installed as the OTel
//     global, so OTel-instrumented meters (otelgin, telemetry.Meter) land on
//     the same scrape as internal/metrics. This needs no endpoint and runs in
//     every deployment.
//   - Tracing (cfg.TracingEnabled, i.e. an OTLP endpoint is set): a second
//     core Provider carrying only the OTLP tracer, installed as the global
//     TracerProvider. Off in prod until a collector exists;
//     deploy/grafana/TRACING-RUNBOOK.md is how to stand one up.
//
// Until 2026-10-03 both halves were gated on the trace endpoint, so an empty
// OTEL_EXPORTER_OTLP_ENDPOINT -- the production default -- also disabled the
// meter provider, and no OTel metric instrument ever reached Prometheus.
//
// Nothing enabled returns a no-op shutdown. The shutdown is never nil, even
// alongside a metrics error.
func InitOTEL(ctx context.Context, cfg *Config) (func(context.Context) error, error) {
	// Before any exporter is built: the SDK's own logger prints raw env input
	// (see installSDKLogSink), and it is used while exporters are constructed.
	installSDKLogSink()

	var shutdowns []func(context.Context) error

	// Tracing is started first and is never fatal. A wrong endpoint, or an
	// exporter that cannot be built, costs the traces and nothing else: core
	// logs why at error level, raises tracing_disabled, and the server starts
	// with tracing off. Until 2026-10-03 the error was returned, cmd/root.go
	// made it fatal, and setting OTEL_EXPORTER_OTLP_ENDPOINT=127.0.0.1:4317 in
	// prod put the server in a crash loop for 75 seconds.
	tracing, tracingOff := false, false
	if cfg.TracingEnabled {
		tp, err := initTracing(ctx, cfg)
		switch {
		case err != nil:
			tracingOff = true
		case slices.Contains(tp.Degraded(), metrics.DegradedTracingDisabled):
			tracingOff = true
			shutdowns = append(shutdowns, tp.Shutdown)
		default:
			tracing = true
			shutdowns = append(shutdowns, tp.Shutdown)
		}
	}

	// The OTLP metric push is an optional copy of the Prometheus instruments
	// and, like tracing, is never fatal: core reports it as
	// metrics_otlp_disabled, not as an error, which is reserved for the
	// Prometheus reader itself.
	//
	// A Prometheus-reader failure is returned, but WITH a usable shutdown for
	// whatever did start (a running tracer). cmd runs this as the Degraded
	// startup step "InitOTEL" (internal/startup.Table): the server keeps
	// serving with metrics off and /health reports "metrics_disabled".
	var metricsErr error
	metricsOn, otlpOn, otlpOff := false, false, false
	if cfg.MetricsEnabled {
		p, shutdownMetrics, err := initMetrics(ctx, cfg)
		if err != nil {
			metricsErr = fmt.Errorf("initialize OpenTelemetry metrics: %w", err)
		} else {
			metricsOn = true
			otlpOff = slices.Contains(p.Degraded(), metrics.DegradedOTLPDisabled)
			otlpOn = cfg.MetricsOTLPEndpoint != "" && !otlpOff
			shutdowns = append(shutdowns, shutdownMetrics)
		}
	}

	// A metrics failure is logged by the caller (the startup step runner),
	// which owns the decision to continue; the summary line only reports
	// metrics=false.
	level, msg, attrs := initSummary(metricsOn, tracing, otlpOn, tracingOff, otlpOff)
	if otlpOn {
		installExportErrorHandler()
	}
	emit(ctx, level, msg, attrs...)

	return func(shutdownCtx context.Context) error { return runShutdowns(shutdownCtx, shutdowns) }, metricsErr
}

// runShutdowns runs fns in order, each with its own share of the caller's
// deadline: fn i of n gets the time remaining divided by the n-i functions
// still to run, so a tracer that hangs on an unreachable collector cannot
// starve the meter flush behind it. Without a deadline each gets the caller's
// context unchanged.
func runShutdowns(ctx context.Context, fns []func(context.Context) error) error {
	var errs []error
	for i, fn := range fns {
		if fn == nil {
			continue
		}
		fctx, cancel := ctx, context.CancelFunc(func() {})
		if dl, ok := ctx.Deadline(); ok {
			fctx, cancel = context.WithTimeout(ctx, time.Until(dl)/time.Duration(len(fns)-i))
		}
		if err := fn(fctx); err != nil {
			errs = append(errs, err)
		}
		cancel()
	}
	return errors.Join(errs...)
}

// initSummary builds the one start-up log line for the whole init (this
// package is allowed one direct slog call, in emit: internal/logger's
// ratchet). Each half that could not be started adds its own clause to the
// message, so a start with both OFF says both. The reasons, and the
// canonical endpoints, are core's own lines (Options.Logger, logged through
// emit too); configured endpoint strings are never logged here.
func initSummary(metricsOn, tracing, otlp, tracingOff, otlpOff bool) (slog.Level, string, []any) {
	level, msg := slog.LevelInfo, "OpenTelemetry initialized"
	attrs := []any{"metrics", metricsOn, "tracing", tracing, "otlp_metrics", otlp}
	var off []string
	if tracingOff {
		off = append(off, "tracing OFF: the trace exporter could not be started")
		attrs = append(attrs, "tracing_error", metrics.DegradedTracingDisabled)
	}
	if otlpOff {
		off = append(off, "the OTLP metric push OFF: /metrics is unaffected")
		attrs = append(attrs, "otlp_metrics_error", metrics.DegradedOTLPDisabled)
	}
	if len(off) > 0 {
		level, msg = slog.LevelError, "OpenTelemetry initialized with "+strings.Join(off, "; ")
	}
	return level, msg, attrs
}

// ParseMetricsInterval parses an OTEL_METRIC_EXPORT_INTERVAL value. The OTel
// standard for that variable is a bare integer count of MILLISECONDS ("60000"),
// and the SDK reads it too, so that form is accepted; a Go duration ("30s",
// "2m") is accepted as well. Empty, unparsable or non-positive input gives 0,
// which core treats as "use the 60s default".
func ParseMetricsInterval(s string) time.Duration {
	s = strings.TrimSpace(s)
	if ms, err := strconv.ParseInt(s, 10, 64); err == nil {
		const ceiling = 24 * time.Hour // keeps the multiplication from overflowing; core clamps later
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

// emit is this package's single direct slog call (internal/logger ratchet):
// InitOTEL's summary line, core's Provider lines (emitHandler) and the
// export-error handler all go through it.
func emit(ctx context.Context, level slog.Level, msg string, attrs ...any) {
	slog.Log(ctx, level, msg, attrs...)
}

// emitHandler is the slog.Handler behind the Logger handed to core: every
// record goes out through emit, so core's lines reach the process logger
// without a second direct slog call here.
type emitHandler struct{ attrs []slog.Attr }

func (h emitHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h emitHandler) Handle(ctx context.Context, r slog.Record) error {
	args := make([]any, 0, len(h.attrs)+r.NumAttrs())
	for _, a := range h.attrs {
		args = append(args, a)
	}
	r.Attrs(func(a slog.Attr) bool { args = append(args, a); return true })
	emit(ctx, r.Level, r.Message, args...)
	return nil
}

func (h emitHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return emitHandler{attrs: append(slices.Clone(h.attrs), as...)}
}

func (h emitHandler) WithGroup(string) slog.Handler { return h }

// metricsOptions is the core Options for cfg: ABO's identity (app ID, service
// name, the audiobook_organizer namespace, its one module), the build version
// and this process's instance ID. The halves are filled by the callers.
func metricsOptions(cfg *Config) metrics.Options {
	return metrics.Options{
		AppID:           appID,
		ServiceName:     cfg.ServiceName,
		MetricNamespace: metricNamespace,
		Modules:         []string{"books"},
		ServiceVersion:  Version(),
		InstanceID:      instanceID,
		Environment:     cfg.Environment,
		Logger:          slog.New(emitHandler{}),
	}
}

// initTracing builds a core Provider that carries only the OTLP tracer for the
// configured endpoint and, when the tracer came up, installs its
// TracerProvider globally. The ratio sampler is not set: 1.0 is the SDK
// default sampler HEAD has always used.
func initTracing(ctx context.Context, cfg *Config) (*metrics.Provider, error) {
	o := metricsOptions(cfg)
	o.Tracing = &metrics.OTLPTracing{Endpoint: cfg.ExporterEndpoint}
	p, err := metrics.New(ctx, o)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(p.Degraded(), metrics.DegradedTracingDisabled) {
		otel.SetTracerProvider(p.TracerProvider())
	}
	return p, nil
}

// metricsProviderOptions is the Options of the metrics half: the Prometheus
// exporter always, and the OTLP push only when its own endpoint is set. The
// trace endpoint never feeds the metric push.
func metricsProviderOptions(cfg *Config) metrics.Options {
	o := metricsOptions(cfg)
	o.Prometheus = true
	if cfg.MetricsOTLPEndpoint != "" {
		o.OTLP = &metrics.OTLPMetrics{
			Endpoint: cfg.MetricsOTLPEndpoint,
			Insecure: cfg.MetricsOTLPInsecure,
			Interval: cfg.MetricsOTLPInterval,
		}
	}
	return o
}

// The bridge registers with the default Prometheus registry, and a second
// registration is an error. The process-wide metrics Provider is therefore
// built once and reused by any later InitOTEL call (tests call it more than
// once; the server calls it once).
var (
	meterOnce       sync.Once
	metricsProvider *metrics.Provider
	meterErr        error

	// meterShutdownOnce makes the returned shutdown idempotent for the same
	// reason: two InitOTEL callers share one Provider.
	meterShutdownOnce sync.Once
	meterShutdownErr  error
)

// initMetrics builds (once) the process-wide metrics Provider, bridges its
// registry onto the default registry, installs its MeterProvider globally and
// returns the Provider and its idempotent shutdown.
//
//   - core builds the Prometheus exporter without scope labels (no
//     otel_scope_name / otel_scope_version, so an OTel family exports the
//     same label set as the client_golang family it replaces; target_info
//     stays on) and with the translation strategy pinned.
//   - The resource carries service.name/version/instance.id and
//     deployment.environment onto target_info.
//   - views.go's bucket table reaches each ABO histogram as creation advice
//     at its call site (metric.WithExplicitBucketBoundaries from
//     HistogramBuckets): a core Provider takes no app views (05-metrics §3).
//
// The first caller's cfg wins: the Provider is built once per process.
func initMetrics(ctx context.Context, cfg *Config) (*metrics.Provider, func(context.Context) error, error) {
	meterOnce.Do(func() {
		p, err := metrics.New(ctx, metricsProviderOptions(cfg))
		if err != nil {
			meterErr = err
			return
		}
		if err := prometheus.DefaultRegisterer.Register(&registryBridge{g: p.Gatherer()}); err != nil {
			meterErr = errors.Join(
				fmt.Errorf("bridge the metrics registry onto the default registry: %w", err),
				p.Shutdown(ctx),
			)
			return
		}
		metricsProvider = p
	})
	if meterErr != nil {
		return nil, nil, meterErr
	}
	otel.SetMeterProvider(metricsProvider.MeterProvider())
	p := metricsProvider
	return p, func(ctx context.Context) error {
		meterShutdownOnce.Do(func() { meterShutdownErr = p.Shutdown(ctx) })
		return meterShutdownErr
	}, nil
}

// bridgeSkipPrefixes are the families the default registry already serves
// itself (its own Go and process collectors, promhttp.Handler's
// promhttp_metric_handler_*): bridging the Provider's copies would put two
// samples of one series on /metrics.
var bridgeSkipPrefixes = []string{"go_", "process_", "promhttp_"}

// registryBridge re-exposes a core Provider's private registry on the default
// registry, which /metrics (promhttp.Handler()), the cache stats handler and
// the cache snapshotter read until R1-15 makes the App's registry the served
// one. It is an unchecked collector (Describe sends nothing), because OTel
// families appear as instruments are created, after registration. Each
// sample is copied unchanged, so names, labels, buckets and help text are
// byte-identical to what the exporter produced.
type registryBridge struct{ g prometheus.Gatherer }

func (b *registryBridge) Describe(chan<- *prometheus.Desc) {}

func (b *registryBridge) Collect(ch chan<- prometheus.Metric) {
	mfs, err := b.g.Gather()
	if err != nil {
		// What gathered is still served, as promhttp.ContinueOnError would;
		// the error goes to the OTel error handler, where the exporter's own
		// collection errors went when it sat on the default registry.
		otel.Handle(fmt.Errorf("gather the core metrics registry: %w", err))
	}
	for _, mf := range mfs {
		if slices.ContainsFunc(bridgeSkipPrefixes, func(p string) bool { return strings.HasPrefix(mf.GetName(), p) }) {
			continue
		}
		desc := bridgeDesc(mf.GetName(), mf.GetHelp())
		for _, m := range mf.GetMetric() {
			ch <- bridgedMetric{desc: desc, m: m}
		}
	}
}

// bridgeDesc is the bridge's one prometheus.NewDesc. It declares no family:
// its name is always a family the Provider already serves, read back from a
// gather, so the static family guards (TestDeclaredFamiliesInGolden and the
// client_golang ratchet in internal/telemetrypolicy) skip its body and refuse
// a call with a literal name. It goes with the bridge in R1-15.
func bridgeDesc(name, help string) *prometheus.Desc {
	return prometheus.NewDesc(name, help, nil, nil)
}

// bridgedMetric is one gathered sample, written back verbatim.
type bridgedMetric struct {
	desc *prometheus.Desc
	m    *dto.Metric
}

func (m bridgedMetric) Desc() *prometheus.Desc { return m.desc }

func (m bridgedMetric) Write(out *dto.Metric) error {
	proto.Merge(out, m.m)
	return nil
}
