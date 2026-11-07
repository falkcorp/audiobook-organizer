// file: core/metrics/options.go
// version: 1.0.1
// guid: 671171f6-2188-4861-a716-61b088c5b563
// last-edited: 2026-11-07

package metrics

import (
	"log/slog"
	"time"

	"github.com/falkcorp/mediacore/log"
)

// Options configures one App's Provider (05-metrics §3). The kernel fills it
// from app.Options and the core.metrics / core.tracing config sections; until
// the kernel exists an app's own telemetry init fills it.
type Options struct {
	// AppID is app.Options.AppID, e.g. "abo"; the ServiceName default.
	AppID string
	// ServiceName is the resource's service.name (app.Options.ServiceName;
	// default AppID). It is also the fixed server.address of the http.server.*
	// series core's stage 2 records, never the request's Host header.
	ServiceName string
	// MetricNamespace is app.Options.MetricNamespace, [a-z][a-z0-9_]*; empty
	// means DefaultNamespace. Core scopes are "<namespace>.<subsystem>".
	MetricNamespace string
	// Modules are the IDs of the modules compiled into the app (the app.New
	// module list), enabled or not. With exactly one, module scopes use the
	// namespace alone; with two or more, "<namespace>.<module>". Never the
	// enabled set: enabling or disabling a module must rename nothing.
	Modules []string
	// ServiceVersion is the resource's service.version.
	ServiceVersion string
	// InstanceID is the resource's service.instance.id: a per-process random
	// ID (ABO passes a ULID), never a hostname. Empty draws a random one per
	// Provider.
	InstanceID string
	// Environment is the deployment.environment resource attribute; empty
	// means DefaultEnvironment.
	Environment string
	// Prometheus attaches the Prometheus exporter, bound to the Provider's
	// private registry, as a reader of the MeterProvider.
	Prometheus bool
	// OTLP, when non-nil, adds the OTLP/gRPC metric push reader.
	OTLP *OTLPMetrics
	// Tracing, when non-nil, builds an OTLP/gRPC span exporter and batching
	// TracerProvider; nil gives a no-op TracerProvider.
	Tracing *OTLPTracing
	// Redactor is from Logging.Redactor(); applied to span names and URL
	// attributes before export (05-metrics §3, SEC-22): every span exporter
	// the Provider builds is wrapped in a redacting exporter that runs them
	// through it. Required when Tracing is set (New fails without one: spans
	// would export credentials); unused when Tracing is nil.
	Redactor *log.Redactor
	// CardinalityLimit is the per-instrument series limit; 0 keeps the SDK
	// default (2000, core.metrics.cardinality_limit).
	CardinalityLimit int
	// Logger receives the Provider's own lines (a degraded OTLP reader or
	// tracer, a refused second owner of a family). Nil discards them.
	Logger *slog.Logger
}

// OTLPMetrics is the optional OTLP/gRPC metric push.
type OTLPMetrics struct {
	// Endpoint is host:port, http(s)://host:port or dns:///host:port; any
	// other form is refused and the push is off (degraded, not fatal).
	Endpoint string
	// Insecure allows plaintext to a bare host:port or a dns:/// target. A URL
	// decides for itself: http:// is plaintext, https:// is TLS.
	Insecure bool
	// Interval is the push period, clamped to [5s, 1h]; zero means 60s.
	Interval time.Duration
}

// OTLPTracing is the optional OTLP/gRPC span export.
type OTLPTracing struct {
	// Endpoint takes the same forms as OTLPMetrics.Endpoint. A bare host:port
	// or dns:/// target leaves the transport to the SDK's environment
	// (OTEL_EXPORTER_OTLP_[TRACES_]INSECURE and the certificate variables).
	Endpoint string
	// Insecure forces plaintext gRPC for a bare host:port or dns:/// target.
	Insecure bool
	// SampleRatio is the parent-based ratio sampler's ratio. Values <= 0 and
	// >= 1 mean 1.0, the SDK default sampler (every root span sampled).
	SampleRatio float64
}
