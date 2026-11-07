// file: core/metrics/provider.go
// version: 1.0.0
// guid: a82f67cc-87d5-45c3-a73b-811125fbe1cd
// last-edited: 2026-11-07

// Package metrics is one App's telemetry (05-metrics): a private Prometheus
// registry, an OTel MeterProvider bound to it through the Prometheus exporter,
// an optional OTLP metric push, an optional OTLP TracerProvider, a propagator,
// and the naming scopes components and modules create their instruments in.
//
// Everything is injected (03 R5): nothing here names
// prometheus.DefaultRegisterer or DefaultGatherer or calls otel.Set* or
// otel.Get*. Core only returns providers; an app that wants the OTel globals
// to point at them installs them itself (app.InstallProcessDefaults, R1-15).
package metrics

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/otlptranslator"
	"go.opentelemetry.io/otel/attribute"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

// DefaultNamespace is the metric namespace when Options.MetricNamespace is
// empty (03 §3.6).
const DefaultNamespace = "mediacore"

// The degraded-health tokens a Provider can raise (05-metrics §7, §8). The
// health subsystem's core.metrics component reports them; until it exists
// the app reads them through Degraded.
const (
	// DegradedOTLPDisabled: the OTLP metric push was asked for and could not
	// be built (a refused endpoint or exporter). The Prometheus scrape is
	// unaffected.
	DegradedOTLPDisabled = "metrics_otlp_disabled"
	// DegradedTracingDisabled: tracing was asked for and could not be built;
	// the TracerProvider is the no-op one.
	DegradedTracingDisabled = "tracing_disabled"
	// DegradedFamilyTaken: a second owner of a Prometheus family was refused.
	DegradedFamilyTaken = "metrics_family_taken"
)

var (
	ErrInstrumentName = errors.New("metrics: instrument name violates naming rules")
	ErrScopeTaken     = errors.New("metrics: scope already registered")
	ErrFamilyTaken    = errors.New("metrics: Prometheus family already registered by another instrument or collector")
)

// segmentRE is one name segment: a namespace, a subsystem, a module ID or a
// dotted part of an instrument name.
func validSegment(s string) bool {
	for i, c := range []byte(s) {
		switch {
		case c >= 'a' && c <= 'z':
		case i > 0 && (c >= '0' && c <= '9' || c == '_'):
		default:
			return false
		}
	}
	return s != ""
}

// Provider is one App's telemetry. Safe for concurrent use. Held by the
// kernel only; components receive a *Scope.
type Provider struct {
	ns      string
	modules []string
	log     *slog.Logger

	reg     *prometheus.Registry
	regW    *familyRegisterer
	fam     *families
	sdkMP   *sdkmetric.MeterProvider
	mp      metric.MeterProvider
	sdkTP   *sdktrace.TracerProvider
	tp      trace.TracerProvider
	prop    propagation.TextMapPropagator
	handler http.Handler
	views   *viewTable

	mu       sync.Mutex
	scopes   map[string]bool
	degraded map[string]bool

	shutdownOnce sync.Once
	shutdownErr  error
}

// New builds a Provider. A Prometheus exporter or registry that cannot be
// built fails (05-metrics §8: the scrape is the primary signal). An OTLP
// metric push or tracer that cannot be built is degraded, not fatal: the
// Provider is returned, the matching token is in Degraded and the reason is
// logged through Options.Logger.
func New(ctx context.Context, o Options) (*Provider, error) {
	return newProvider(ctx, o, grpcExporters())
}

// newProvider is New with the exporter constructors as a parameter.
func newProvider(ctx context.Context, o Options, exp exporters) (*Provider, error) {
	ns := o.MetricNamespace
	if ns == "" {
		ns = DefaultNamespace
	}
	if !validSegment(ns) {
		return nil, fmt.Errorf("%w: MetricNamespace %q must match [a-z][a-z0-9_]*", ErrInstrumentName, ns)
	}
	for _, m := range o.Modules {
		if !validSegment(m) {
			return nil, fmt.Errorf("%w: module ID %q must match [a-z][a-z0-9_]*", ErrInstrumentName, m)
		}
	}
	serviceName := o.ServiceName
	if serviceName == "" {
		serviceName = o.AppID
	}
	if serviceName == "" {
		serviceName = ns
	}
	instanceID := o.InstanceID
	if instanceID == "" {
		instanceID = randomInstanceID()
	}
	logger := o.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	p := &Provider{
		ns:       ns,
		modules:  slices.Clone(o.Modules),
		log:      logger,
		reg:      prometheus.NewRegistry(),
		views:    &viewTable{byScope: map[[2]string][]float64{}},
		scopes:   map[string]bool{},
		degraded: map[string]bool{},
	}
	p.fam = newFamilies(p.familyTaken)
	p.regW = &familyRegisterer{reg: p.reg, f: p.fam}

	// The default registry had the Go and process collectors implicitly; the
	// private one gets them explicitly (process_resident_memory_bytes feeds
	// the memory alert).
	if err := p.regW.Register(collectors.NewGoCollector()); err != nil {
		return nil, fmt.Errorf("metrics: register the Go collector: %w", err)
	}
	if err := p.regW.Register(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{})); err != nil {
		return nil, fmt.Errorf("metrics: register the process collector: %w", err)
	}

	res := newResource(serviceName, o.ServiceVersion, instanceID, o.Environment)
	mpOpts := []sdkmetric.Option{sdkmetric.WithResource(res), sdkmetric.WithView(p.views.view)}
	if o.CardinalityLimit > 0 {
		mpOpts = append(mpOpts, sdkmetric.WithCardinalityLimit(o.CardinalityLimit))
	}
	if o.Prometheus {
		// Normative construction (05-metrics §3): the private registry, no
		// otel_scope_* labels, and the translation strategy pinned rather
		// than taken from the process-global validation scheme.
		exp, err := otelprom.New(
			otelprom.WithRegisterer(p.reg),
			otelprom.WithoutScopeInfo(),
			otelprom.WithTranslationStrategy(otlptranslator.UnderscoreEscapingWithSuffixes),
		)
		if err != nil {
			return nil, fmt.Errorf("metrics: build the Prometheus exporter: %w", err)
		}
		mpOpts = append(mpOpts, sdkmetric.WithReader(exp))
	}
	if o.OTLP != nil {
		reader, endpoint, note, err := newOTLPReader(ctx, o.OTLP, exp.metric)
		if note != "" {
			p.log.Warn("metrics: OTLP metric push interval adjusted", "note", note)
		}
		if err != nil {
			p.degrade(DegradedOTLPDisabled, "the OTLP metric push is off; the Prometheus scrape is unaffected", err)
		} else {
			mpOpts = append(mpOpts, sdkmetric.WithReader(reader))
			p.log.Info("metrics: OTLP metric push on", "endpoint", endpoint)
		}
	}
	p.sdkMP = sdkmetric.NewMeterProvider(mpOpts...)
	p.mp = &familyMeterProvider{mp: p.sdkMP, f: p.fam}

	p.tp = tracenoop.NewTracerProvider()
	if o.Tracing != nil {
		tp, endpoint, err := newTracerProvider(ctx, o.Tracing, res, exp.span)
		if err != nil {
			p.degrade(DegradedTracingDisabled, "tracing is off: the trace exporter could not be started", err)
		} else {
			p.sdkTP, p.tp = tp, tp
			p.log.Info("metrics: tracing on", "endpoint", endpoint)
		}
	}
	p.prop = propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})

	// promhttp on the private registry, wrapped so the
	// promhttp_metric_handler_* series exist; a collector that fails to
	// gather costs its own families, not the scrape.
	p.handler = promhttp.InstrumentMetricHandler(p.regW, promhttp.HandlerFor(p.reg, promhttp.HandlerOpts{
		ErrorHandling: promhttp.ContinueOnError,
		Registry:      p.regW,
	}))
	return p, nil
}

// degrade records a degraded token and logs why.
func (p *Provider) degrade(token, msg string, err error) {
	p.mu.Lock()
	p.degraded[token] = true
	p.mu.Unlock()
	p.log.Error("metrics: "+msg, "token", token, "error", err.Error())
}

// familyTaken is the family table's refusal hook.
func (p *Provider) familyTaken(family string, held, refused owner) {
	p.mu.Lock()
	p.degraded[DegradedFamilyTaken] = true
	p.mu.Unlock()
	p.log.Error("metrics: refused a second owner of a Prometheus family",
		"token", DegradedFamilyTaken, "family", family, "owner", held.String(), "refused", refused.String())
}

// Degraded returns the sorted degraded-health tokens this Provider has
// raised (DegradedOTLPDisabled, DegradedTracingDisabled, DegradedFamilyTaken).
// Empty means healthy.
func (p *Provider) Degraded() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Sorted(maps.Keys(p.degraded))
}

// MeterProvider is the family-checking MeterProvider: every instrument created
// through it, inside a Scope or not, has its Prometheus family recorded, and a
// second owner gets ErrFamilyTaken and a no-op instrument.
func (p *Provider) MeterProvider() metric.MeterProvider { return p.mp }

// TracerProvider is the OTLP TracerProvider, or the no-op one when tracing is
// off or degraded.
func (p *Provider) TracerProvider() trace.TracerProvider { return p.tp }

// Propagator is W3C trace context plus baggage.
func (p *Provider) Propagator() propagation.TextMapPropagator { return p.prop }

// Registerer is the escape hatch for third-party and legacy collectors: the
// private registry, behind the one-owner-per-family check.
func (p *Provider) Registerer() prometheus.Registerer { return p.regW }

// Gatherer is the private registry.
func (p *Provider) Gatherer() prometheus.Gatherer { return p.reg }

// Handler serves the private registry (/metrics).
func (p *Provider) Handler() http.Handler { return p.handler }

// Shutdown flushes the OTLP readers and the span batcher and stops them. It
// is idempotent and bounded by ctx: with a deadline, the tracer gets half of
// the remaining time and the meter provider the rest, so a tracer hung on an
// unreachable collector cannot starve the last metric flush.
func (p *Provider) Shutdown(ctx context.Context) error {
	p.shutdownOnce.Do(func() {
		var errs []error
		if p.sdkTP != nil {
			tctx, cancel := ctx, context.CancelFunc(func() {})
			if dl, ok := ctx.Deadline(); ok {
				tctx, cancel = context.WithTimeout(ctx, time.Until(dl)/2)
			}
			errs = append(errs, p.sdkTP.Shutdown(tctx))
			cancel()
		}
		errs = append(errs, p.sdkMP.Shutdown(ctx))
		p.shutdownErr = errors.Join(errs...)
	})
	return p.shutdownErr
}

// Core returns the scope of a core subsystem: prefix "<namespace>.<subsystem>".
func (p *Provider) Core(subsystem string, allowed ...attribute.Key) (*Scope, error) {
	if !validSegment(subsystem) {
		return nil, fmt.Errorf("%w: subsystem %q must match [a-z][a-z0-9_]*", ErrInstrumentName, subsystem)
	}
	return p.newScope("core:"+subsystem, p.ns+"."+subsystem, allowed)
}

// Module returns the scope of a module compiled into the app: prefix
// "<namespace>" when the app ships one module, "<namespace>.<module>" when it
// ships two or more (Options.Modules, never the enabled set).
func (p *Provider) Module(id string, allowed ...attribute.Key) (*Scope, error) {
	if !slices.Contains(p.modules, id) {
		return nil, fmt.Errorf("%w: module %q is not in Options.Modules %v", ErrInstrumentName, id, p.modules)
	}
	prefix := p.ns
	if len(p.modules) > 1 {
		prefix = p.ns + "." + id
	}
	return p.newScope("module:"+id, prefix, allowed)
}

func (p *Provider) newScope(key, prefix string, allowed []attribute.Key) (*Scope, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.scopes[key] {
		return nil, fmt.Errorf("%w: %s", ErrScopeTaken, key)
	}
	p.scopes[key] = true
	s := &Scope{p: p, prefix: prefix, allowed: slices.Clone(allowed)}
	s.meter = &scopeMeter{s: s, m: p.mp.Meter(prefix)}
	s.tracer = p.tp.Tracer(prefix)
	return s, nil
}

// viewTable holds the histogram buckets Scope.View declared, keyed by (meter
// scope name, instrument name). The one view function installed at New reads
// it when the SDK resolves an instrument: an SDK view can only be set at
// construction, so a late declaration is a table entry, not a late view.
type viewTable struct {
	mu      sync.RWMutex
	byScope map[[2]string][]float64
}

func (v *viewTable) set(scope, name string, buckets []float64) {
	v.mu.Lock()
	v.byScope[[2]string{scope, name}] = slices.Clone(buckets)
	v.mu.Unlock()
}

func (v *viewTable) has(scope, name string) bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	_, ok := v.byScope[[2]string{scope, name}]
	return ok
}

// view is the sdkmetric.View: an explicit-bucket histogram for a declared
// (scope, instrument), no match otherwise (so an instrument's own bucket
// advice, or the SDK default, applies).
func (v *viewTable) view(i sdkmetric.Instrument) (sdkmetric.Stream, bool) {
	if i.Kind != sdkmetric.InstrumentKindHistogram {
		return sdkmetric.Stream{}, false
	}
	v.mu.RLock()
	b, ok := v.byScope[[2]string{i.Scope.Name, i.Name}]
	v.mu.RUnlock()
	if !ok {
		return sdkmetric.Stream{}, false
	}
	return sdkmetric.Stream{
		Name:        i.Name,
		Description: i.Description,
		Unit:        i.Unit,
		Aggregation: sdkmetric.AggregationExplicitBucketHistogram{Boundaries: slices.Clone(b)},
	}, true
}
