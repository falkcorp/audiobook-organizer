// file: core/metrics/otlp.go
// version: 1.0.1
// guid: 92813fce-3a0a-4079-b970-9bea6e4e040a
// last-edited: 2026-11-07

package metrics

import (
	"context"
	"strings"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/grpc/credentials"
	grpcinsecure "google.golang.org/grpc/credentials/insecure"
)

// The OTLP push interval bounds (core.metrics.otlp.interval).
const (
	defaultOTLPInterval = 60 * time.Second
	minOTLPInterval     = 5 * time.Second
	maxOTLPInterval     = time.Hour
)

// clampInterval returns the effective push interval and a note when it
// differs from what was asked for.
func clampInterval(d time.Duration) (time.Duration, string) {
	switch {
	case d <= 0:
		return defaultOTLPInterval, "interval unset or invalid, using " + defaultOTLPInterval.String()
	case d < minOTLPInterval:
		return minOTLPInterval, "interval " + d.String() + " below minimum, using " + minOTLPInterval.String()
	case d > maxOTLPInterval:
		return maxOTLPInterval, "interval " + d.String() + " above maximum, using " + maxOTLPInterval.String()
	}
	return d, ""
}

// exporters are the OTLP exporter constructors a Provider uses. New always
// passes grpcExporters(); this package's tests pass an in-memory exporter (the
// flush and degrade tests). A per-call value, not a package variable, so no
// test can change what another Provider builds (03 R5).
type exporters struct {
	metric func(context.Context, ...otlpmetricgrpc.Option) (sdkmetric.Exporter, error)
	span   func(context.Context, ...otlptracegrpc.Option) (sdktrace.SpanExporter, error)
}

func grpcExporters() exporters {
	return exporters{
		metric: func(ctx context.Context, opts ...otlpmetricgrpc.Option) (sdkmetric.Exporter, error) {
			return otlpmetricgrpc.New(ctx, opts...)
		},
		span: func(ctx context.Context, opts ...otlptracegrpc.Option) (sdktrace.SpanExporter, error) {
			return otlptracegrpc.New(ctx, opts...)
		},
	}
}

// traceOptions builds the span exporter options for a validated endpoint.
// Options are applied after the environment, so a later one wins.
//
//   - http://: plaintext, pinned with insecure credentials as well as
//     WithInsecure, because credentials installed by
//     OTEL_EXPORTER_OTLP_[TRACES_]CERTIFICATE / CLIENT_CERTIFICATE beat the
//     insecure flag and would otherwise turn it into TLS.
//   - https://: TLS. WithEndpointURL sets Insecure=false (an
//     OTEL_EXPORTER_OTLP_INSECURE=true cannot downgrade it) and sets NO
//     credentials, so a CA or client certificate from the environment is
//     still honoured. The WithEndpoint after it replaces the dial target with
//     the explicit dns:///host:port (WithEndpointURL alone would dial the bare
//     host:port, which gRPC may read as a resolver scheme).
//   - bare host:port and dns:///: no transport stated, so the SDK's
//     environment decides (OTEL_EXPORTER_OTLP_[TRACES_]INSECURE), unless
//     insecure forces plaintext the way http:// does.
//
// The metric exporter differs on purpose: it ignores the certificate
// environment (see metricOptions).
func traceOptions(t otlpTarget, insecure bool) []otlptracegrpc.Option {
	plaintext := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint(t.GRPCTarget()),
		otlptracegrpc.WithInsecure(),
		otlptracegrpc.WithTLSCredentials(grpcinsecure.NewCredentials()),
	}
	switch {
	case strings.HasPrefix(t.URL, "http://"):
		return plaintext
	case strings.HasPrefix(t.URL, "https://"):
		return []otlptracegrpc.Option{
			otlptracegrpc.WithEndpointURL(t.URL),
			otlptracegrpc.WithEndpoint(t.GRPCTarget()),
		}
	case insecure:
		return plaintext
	}
	return []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(t.GRPCTarget())}
}

// metricPlaintext is the one rule for whether the metric push uses plaintext
// gRPC: an http:// URL is plaintext and an https:// URL is TLS (the URL
// decides; insecure is not consulted); a bare host:port or a dns:/// target is
// TLS unless insecure is true.
func metricPlaintext(t otlpTarget, insecure bool) bool {
	if t.URL != "" {
		return !strings.EqualFold(t.URL[:strings.Index(t.URL, "://")], "https")
	}
	return insecure
}

// metricOptions is the metric exporter's endpoint plus an EXPLICIT transport,
// in every branch.
//
// otlpmetricgrpc reads the generic OTEL_EXPORTER_OTLP_* environment (the trace
// exporter's own variables) before it applies options, so an unset transport
// would inherit it: OTEL_EXPORTER_OTLP_ENDPOINT=http://... downgrades a TLS
// metric endpoint to plaintext, and OTEL_EXPORTER_OTLP_CERTIFICATE or
// *_CLIENT_CERTIFICATE install credentials that beat even WithInsecure (the
// exporter prefers credentials over its insecure flag). Options win over the
// environment, and credentials win over the insecure flag, so the transport is
// pinned with credentials:
//
//   - plaintext (metricPlaintext): insecure credentials. No environment
//     variable can turn this back into TLS.
//   - otherwise: TLS with the system root CAs. OTEL_EXPORTER_OTLP_CERTIFICATE
//     and the client-certificate variables are therefore NOT honoured for
//     metrics; use SSL_CERT_FILE / SSL_CERT_DIR for a private CA.
//
// Headers, temporality and histogram aggregation are pinned in newOTLPReader.
func metricOptions(t otlpTarget, insecure bool) []otlpmetricgrpc.Option {
	opts := []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(t.GRPCTarget())}
	if metricPlaintext(t, insecure) {
		return append(opts, otlpmetricgrpc.WithInsecure(),
			otlpmetricgrpc.WithTLSCredentials(grpcinsecure.NewCredentials()))
	}
	return append(opts, otlpmetricgrpc.WithTLSCredentials(credentials.NewTLS(nil)))
}

// newOTLPReader builds the periodic OTLP metric reader. It returns the
// canonical endpoint (what is dialled and what may be logged) and a note when
// the interval was adjusted. The gRPC dial is non-blocking, so an unreachable
// collector costs nothing here.
func newOTLPReader(ctx context.Context, o *OTLPMetrics, newExporter func(context.Context, ...otlpmetricgrpc.Option) (sdkmetric.Exporter, error)) (sdkmetric.Reader, string, string, error) {
	target, err := parseOTLPEndpoint(keyMetricsEndpoint, o.Endpoint)
	if err != nil {
		return nil, "", "", err
	}
	interval, note := clampInterval(o.Interval)
	// The generic OTEL_EXPORTER_OTLP_* environment is read by the exporter
	// before these options, so what must not follow it is pinned here.
	// PINNED (the environment cannot change it):
	//   - transport and TLS material: see metricOptions.
	//   - headers: emptied, so the trace collector's credentials
	//     (OTEL_EXPORTER_OTLP_HEADERS) are never sent to the metric host.
	//     OTEL_EXPORTER_OTLP_METRICS_HEADERS is neutralised too: metric
	//     headers are not supported.
	//   - temporality: cumulative, whatever
	//     OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE says.
	//   - histogram aggregation: the SDK default, so
	//     OTEL_EXPORTER_OTLP_METRICS_DEFAULT_HISTOGRAM_AGGREGATION cannot switch
	//     histograms to exponential (05-metrics §5 rule 4).
	// STILL FOLLOWS THE ENVIRONMENT (benign, left alone on purpose):
	//   - OTEL_EXPORTER_OTLP_[METRICS_]COMPRESSION and _TIMEOUT: payload
	//     encoding and the per-request deadline; neither moves data elsewhere.
	//   - OTEL_METRIC_EXPORT_TIMEOUT (read by the SDK's periodic reader).
	opts := append(metricOptions(target, o.Insecure),
		otlpmetricgrpc.WithHeaders(map[string]string{}),
		otlpmetricgrpc.WithTemporalitySelector(sdkmetric.CumulativeTemporalitySelector),
		otlpmetricgrpc.WithAggregationSelector(sdkmetric.DefaultAggregationSelector))
	exp, err := newExporter(ctx, opts...)
	if err != nil {
		return nil, target.Display(), note, err
	}
	return sdkmetric.NewPeriodicReader(exp, sdkmetric.WithInterval(interval)), target.Display(), note, nil
}

// newTracerProvider builds the batching TracerProvider for o. It returns the
// canonical endpoint for logging. The span exporter goes into the batcher
// only through sr.wrap (05-metrics §3 span redaction), and a nil sr is
// refused, so no span leaves a Provider unredacted.
func newTracerProvider(ctx context.Context, o *OTLPTracing, res resourceOption, newExporter func(context.Context, ...otlptracegrpc.Option) (sdktrace.SpanExporter, error), sr *spanRedactor) (*sdktrace.TracerProvider, string, error) {
	if sr == nil {
		return nil, "", errNoRedactor
	}
	target, err := parseOTLPEndpoint(keyTraceEndpoint, o.Endpoint)
	if err != nil {
		return nil, "", err
	}
	exp, err := newExporter(ctx, traceOptions(target, o.Insecure)...)
	if err != nil {
		return nil, target.Display(), err
	}
	opts := []sdktrace.TracerProviderOption{sdktrace.WithBatcher(sr.wrap(exp)), sdktrace.WithResource(res)}
	if r := o.SampleRatio; r > 0 && r < 1 {
		opts = append(opts, sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(r))))
	}
	return sdktrace.NewTracerProvider(opts...), target.Display(), nil
}
