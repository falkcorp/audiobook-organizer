// file: core/metrics/family.go
// version: 1.0.2
// guid: 750823bb-4a76-4075-95b0-2dc2bc337766
// last-edited: 2026-11-07

package metrics

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/otlptranslator"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/embedded"
	"go.opentelemetry.io/otel/metric/noop"
)

// One Prometheus family, one owner (05-metrics §3, R9-3). Module and core
// scopes share the app's namespace, and an app may still create instruments
// through the raw or process-global MeterProvider and register collectors
// through Registerer(), so two emitters could translate to one family name.
// families records every family's owner and refuses a second one with
// ErrFamilyTaken: a different instrument identity, or a collector for a name
// an instrument holds (and the reverse). Two collectors for one name are left
// to the registry, which reports them itself (AlreadyRegisteredError is what
// promhttp.InstrumentMetricHandler and similar callers rely on).

// instrumentID is the OTel SDK's own instrument identity: the meter's scope
// (name, version, schema URL) and the instrument's name, kind, number type,
// unit and description. Creating the identical instrument twice is the SDK's
// one instrument, not a second owner.
type instrumentID struct {
	scope, version, schemaURL string
	name, kind, number        string
	unit, description         string
}

func (id instrumentID) String() string {
	return fmt.Sprintf("instrument %q (%s %s, unit %q) in scope %q", id.name, id.number, id.kind, id.unit, id.scope)
}

// owner is a family's holder: an instrument, or a collector (collector set).
type owner struct {
	inst      instrumentID
	collector bool
}

func (o owner) String() string {
	if o.collector {
		return "a collector registered through Registerer()"
	}
	return o.inst.String()
}

// families is the per-Provider owner table.
type families struct {
	namer otlptranslator.MetricNamer
	// onTaken is told about every refusal (the Provider logs it and raises
	// metrics_family_taken).
	onTaken func(family string, held, refused owner)

	mu    sync.Mutex
	owner map[string]owner
}

func newFamilies(onTaken func(string, owner, owner)) *families {
	return &families{
		// The exporter's own namer for the pinned strategy (no namespace:
		// instrument names carry it), so a recorded name is the served one.
		namer:   otlptranslator.NewMetricNamer("", otlptranslator.UnderscoreEscapingWithSuffixes),
		onTaken: onTaken,
		owner:   map[string]owner{},
	}
}

// familyOf is the Prometheus family name the exporter will serve id as.
func (f *families) familyOf(id instrumentID, typ otlptranslator.MetricType) (string, error) {
	return f.namer.Build(otlptranslator.Metric{Name: id.name, Unit: id.unit, Type: typ})
}

// claimInstrument records id as the owner of its family, or refuses it. The
// returned release undoes a new claim (the SDK refused the instrument).
func (f *families) claimInstrument(id instrumentID, typ otlptranslator.MetricType) (release func(), err error) {
	name, err := f.familyOf(id, typ)
	if err != nil {
		return nil, fmt.Errorf("%w: %q: %v", ErrInstrumentName, id.name, err)
	}
	f.mu.Lock()
	held, ok := f.owner[name]
	switch {
	case !ok:
		f.owner[name] = owner{inst: id}
		f.mu.Unlock()
		return func() {
			f.mu.Lock()
			if cur, ok := f.owner[name]; ok && !cur.collector && cur.inst == id {
				delete(f.owner, name)
			}
			f.mu.Unlock()
		}, nil
	case !held.collector && held.inst == id:
		f.mu.Unlock()
		return func() {}, nil
	}
	f.mu.Unlock()
	refused := owner{inst: id}
	f.onTaken(name, held, refused)
	return nil, fmt.Errorf("%w: %s is held by %s; refused %s", ErrFamilyTaken, name, held, refused)
}

// claimCollector records names as collector-owned, or refuses them all when
// an instrument holds any of them.
func (f *families) claimCollector(names []string) error {
	f.mu.Lock()
	for _, n := range names {
		if held, ok := f.owner[n]; ok && !held.collector {
			f.mu.Unlock()
			f.onTaken(n, held, owner{collector: true})
			return fmt.Errorf("%w: %s is held by %s; refused a collector", ErrFamilyTaken, n, held)
		}
	}
	for _, n := range names {
		f.owner[n] = owner{collector: true}
	}
	f.mu.Unlock()
	return nil
}

// releaseCollector forgets collector-owned names (Unregister, or a registry
// that refused the collector).
func (f *families) releaseCollector(names []string) {
	f.mu.Lock()
	for _, n := range names {
		if cur, ok := f.owner[n]; ok && cur.collector {
			delete(f.owner, n)
		}
	}
	f.mu.Unlock()
}

// descName reads the fully-qualified name out of Desc.String(), whose format
// is Desc{fqName: "<quoted>", help: ...}: client_golang exposes no accessor.
func descName(d *prometheus.Desc) (string, bool) {
	rest, ok := strings.CutPrefix(d.String(), "Desc{fqName: ")
	if !ok {
		return "", false
	}
	q, err := strconv.QuotedPrefix(rest)
	if err != nil {
		return "", false
	}
	n, err := strconv.Unquote(q)
	return n, err == nil
}

// collectorFamilies lists the family names c describes. An unchecked
// collector (one that describes nothing) has none, so it can be checked only
// by the registry at gather time.
func collectorFamilies(c prometheus.Collector) []string {
	ch := make(chan *prometheus.Desc)
	go func() {
		c.Describe(ch)
		close(ch)
	}()
	var names []string
	for d := range ch {
		if n, ok := descName(d); ok {
			names = append(names, n)
		}
	}
	return names
}

// familyRegisterer is Provider.Registerer(): the private registry behind the
// family table.
type familyRegisterer struct {
	reg *prometheus.Registry
	f   *families
}

func (r *familyRegisterer) Register(c prometheus.Collector) error {
	names := collectorFamilies(c)
	if err := r.f.claimCollector(names); err != nil {
		return err
	}
	if err := r.reg.Register(c); err != nil {
		if _, ok := errors.AsType[prometheus.AlreadyRegisteredError](err); !ok {
			r.f.releaseCollector(names)
		}
		return err
	}
	return nil
}

func (r *familyRegisterer) MustRegister(cs ...prometheus.Collector) {
	for _, c := range cs {
		if err := r.Register(c); err != nil {
			panic(err)
		}
	}
}

func (r *familyRegisterer) Unregister(c prometheus.Collector) bool {
	if !r.reg.Unregister(c) {
		return false
	}
	r.f.releaseCollector(collectorFamilies(c))
	return true
}

// familyMeterProvider is Provider.MeterProvider(): every meter it hands out
// records its instruments' families.
type familyMeterProvider struct {
	embedded.MeterProvider
	mp metric.MeterProvider
	f  *families
}

func (p *familyMeterProvider) Meter(name string, opts ...metric.MeterOption) metric.Meter {
	cfg := metric.NewMeterConfig(opts...)
	return &familyMeter{
		m:     p.mp.Meter(name, opts...),
		f:     p.f,
		scope: instrumentID{scope: name, version: cfg.InstrumentationVersion(), schemaURL: cfg.SchemaURL()},
	}
}

type familyMeter struct {
	embedded.Meter
	m     metric.Meter
	f     *families
	scope instrumentID // scope fields only
}

// claim checks the family of one instrument, then creates it; a refused
// instrument is the no-op one, returned with the error.
func claim[T any](m *familyMeter, name, kind, number, unit, desc string, typ otlptranslator.MetricType,
	create func() (T, error), refused func() (T, error)) (T, error) {
	id := m.scope
	id.name, id.kind, id.number, id.unit, id.description = name, kind, number, unit, desc
	release, err := m.f.claimInstrument(id, typ)
	if err != nil {
		// The no-op meter does not fail today; join its error anyway so a
		// future one is reported rather than dropped.
		n, nerr := refused()
		return n, errors.Join(err, nerr)
	}
	inst, err := create()
	if err != nil {
		release()
	}
	return inst, err
}

func nop() noop.Meter { return noop.Meter{} }

const (
	kindCounter       = "counter"
	kindUpDownCounter = "updowncounter"
	kindHistogram     = "histogram"
	kindGauge         = "gauge"
	kindObsCounter    = "observable_counter"
	kindObsUpDown     = "observable_updowncounter"
	kindObsGauge      = "observable_gauge"
	numInt64          = "int64"
	numFloat64        = "float64"
)

func (m *familyMeter) Int64Counter(n string, o ...metric.Int64CounterOption) (metric.Int64Counter, error) {
	c := metric.NewInt64CounterConfig(o...)
	return claim(m, n, kindCounter, numInt64, c.Unit(), c.Description(), otlptranslator.MetricTypeMonotonicCounter,
		func() (metric.Int64Counter, error) { return m.m.Int64Counter(n, o...) },
		func() (metric.Int64Counter, error) { return nop().Int64Counter(n) })
}

func (m *familyMeter) Int64UpDownCounter(n string, o ...metric.Int64UpDownCounterOption) (metric.Int64UpDownCounter, error) {
	c := metric.NewInt64UpDownCounterConfig(o...)
	return claim(m, n, kindUpDownCounter, numInt64, c.Unit(), c.Description(), otlptranslator.MetricTypeNonMonotonicCounter,
		func() (metric.Int64UpDownCounter, error) { return m.m.Int64UpDownCounter(n, o...) },
		func() (metric.Int64UpDownCounter, error) { return nop().Int64UpDownCounter(n) })
}

func (m *familyMeter) Int64Histogram(n string, o ...metric.Int64HistogramOption) (metric.Int64Histogram, error) {
	c := metric.NewInt64HistogramConfig(o...)
	return claim(m, n, kindHistogram, numInt64, c.Unit(), c.Description(), otlptranslator.MetricTypeHistogram,
		func() (metric.Int64Histogram, error) { return m.m.Int64Histogram(n, o...) },
		func() (metric.Int64Histogram, error) { return nop().Int64Histogram(n) })
}

func (m *familyMeter) Int64Gauge(n string, o ...metric.Int64GaugeOption) (metric.Int64Gauge, error) {
	c := metric.NewInt64GaugeConfig(o...)
	return claim(m, n, kindGauge, numInt64, c.Unit(), c.Description(), otlptranslator.MetricTypeGauge,
		func() (metric.Int64Gauge, error) { return m.m.Int64Gauge(n, o...) },
		func() (metric.Int64Gauge, error) { return nop().Int64Gauge(n) })
}

func (m *familyMeter) Int64ObservableCounter(n string, o ...metric.Int64ObservableCounterOption) (metric.Int64ObservableCounter, error) {
	c := metric.NewInt64ObservableCounterConfig(o...)
	return claim(m, n, kindObsCounter, numInt64, c.Unit(), c.Description(), otlptranslator.MetricTypeMonotonicCounter,
		func() (metric.Int64ObservableCounter, error) { return m.m.Int64ObservableCounter(n, o...) },
		func() (metric.Int64ObservableCounter, error) { return nop().Int64ObservableCounter(n) })
}

func (m *familyMeter) Int64ObservableUpDownCounter(n string, o ...metric.Int64ObservableUpDownCounterOption) (metric.Int64ObservableUpDownCounter, error) {
	c := metric.NewInt64ObservableUpDownCounterConfig(o...)
	return claim(m, n, kindObsUpDown, numInt64, c.Unit(), c.Description(), otlptranslator.MetricTypeNonMonotonicCounter,
		func() (metric.Int64ObservableUpDownCounter, error) { return m.m.Int64ObservableUpDownCounter(n, o...) },
		func() (metric.Int64ObservableUpDownCounter, error) { return nop().Int64ObservableUpDownCounter(n) })
}

func (m *familyMeter) Int64ObservableGauge(n string, o ...metric.Int64ObservableGaugeOption) (metric.Int64ObservableGauge, error) {
	c := metric.NewInt64ObservableGaugeConfig(o...)
	return claim(m, n, kindObsGauge, numInt64, c.Unit(), c.Description(), otlptranslator.MetricTypeGauge,
		func() (metric.Int64ObservableGauge, error) { return m.m.Int64ObservableGauge(n, o...) },
		func() (metric.Int64ObservableGauge, error) { return nop().Int64ObservableGauge(n) })
}

func (m *familyMeter) Float64Counter(n string, o ...metric.Float64CounterOption) (metric.Float64Counter, error) {
	c := metric.NewFloat64CounterConfig(o...)
	return claim(m, n, kindCounter, numFloat64, c.Unit(), c.Description(), otlptranslator.MetricTypeMonotonicCounter,
		func() (metric.Float64Counter, error) { return m.m.Float64Counter(n, o...) },
		func() (metric.Float64Counter, error) { return nop().Float64Counter(n) })
}

func (m *familyMeter) Float64UpDownCounter(n string, o ...metric.Float64UpDownCounterOption) (metric.Float64UpDownCounter, error) {
	c := metric.NewFloat64UpDownCounterConfig(o...)
	return claim(m, n, kindUpDownCounter, numFloat64, c.Unit(), c.Description(), otlptranslator.MetricTypeNonMonotonicCounter,
		func() (metric.Float64UpDownCounter, error) { return m.m.Float64UpDownCounter(n, o...) },
		func() (metric.Float64UpDownCounter, error) { return nop().Float64UpDownCounter(n) })
}

func (m *familyMeter) Float64Histogram(n string, o ...metric.Float64HistogramOption) (metric.Float64Histogram, error) {
	c := metric.NewFloat64HistogramConfig(o...)
	return claim(m, n, kindHistogram, numFloat64, c.Unit(), c.Description(), otlptranslator.MetricTypeHistogram,
		func() (metric.Float64Histogram, error) { return m.m.Float64Histogram(n, o...) },
		func() (metric.Float64Histogram, error) { return nop().Float64Histogram(n) })
}

func (m *familyMeter) Float64Gauge(n string, o ...metric.Float64GaugeOption) (metric.Float64Gauge, error) {
	c := metric.NewFloat64GaugeConfig(o...)
	return claim(m, n, kindGauge, numFloat64, c.Unit(), c.Description(), otlptranslator.MetricTypeGauge,
		func() (metric.Float64Gauge, error) { return m.m.Float64Gauge(n, o...) },
		func() (metric.Float64Gauge, error) { return nop().Float64Gauge(n) })
}

func (m *familyMeter) Float64ObservableCounter(n string, o ...metric.Float64ObservableCounterOption) (metric.Float64ObservableCounter, error) {
	c := metric.NewFloat64ObservableCounterConfig(o...)
	return claim(m, n, kindObsCounter, numFloat64, c.Unit(), c.Description(), otlptranslator.MetricTypeMonotonicCounter,
		func() (metric.Float64ObservableCounter, error) { return m.m.Float64ObservableCounter(n, o...) },
		func() (metric.Float64ObservableCounter, error) { return nop().Float64ObservableCounter(n) })
}

func (m *familyMeter) Float64ObservableUpDownCounter(n string, o ...metric.Float64ObservableUpDownCounterOption) (metric.Float64ObservableUpDownCounter, error) {
	c := metric.NewFloat64ObservableUpDownCounterConfig(o...)
	return claim(m, n, kindObsUpDown, numFloat64, c.Unit(), c.Description(), otlptranslator.MetricTypeNonMonotonicCounter,
		func() (metric.Float64ObservableUpDownCounter, error) {
			return m.m.Float64ObservableUpDownCounter(n, o...)
		},
		func() (metric.Float64ObservableUpDownCounter, error) { return nop().Float64ObservableUpDownCounter(n) })
}

func (m *familyMeter) Float64ObservableGauge(n string, o ...metric.Float64ObservableGaugeOption) (metric.Float64ObservableGauge, error) {
	c := metric.NewFloat64ObservableGaugeConfig(o...)
	return claim(m, n, kindObsGauge, numFloat64, c.Unit(), c.Description(), otlptranslator.MetricTypeGauge,
		func() (metric.Float64ObservableGauge, error) { return m.m.Float64ObservableGauge(n, o...) },
		func() (metric.Float64ObservableGauge, error) { return nop().Float64ObservableGauge(n) })
}

// RegisterCallback forwards to the SDK meter without the refused (no-op)
// instruments: the SDK rejects an observable from another implementation, and
// one refused instrument must not cost the others their callback.
func (m *familyMeter) RegisterCallback(f metric.Callback, insts ...metric.Observable) (metric.Registration, error) {
	return m.m.RegisterCallback(f, withoutNoop(insts)...)
}

// withoutNoop drops the no-op observables a refusal handed out.
func withoutNoop(insts []metric.Observable) []metric.Observable {
	kept := make([]metric.Observable, 0, len(insts))
	for _, i := range insts {
		switch i.(type) {
		case noop.Int64ObservableCounter, noop.Int64ObservableUpDownCounter, noop.Int64ObservableGauge,
			noop.Float64ObservableCounter, noop.Float64ObservableUpDownCounter, noop.Float64ObservableGauge:
			continue
		}
		kept = append(kept, i)
	}
	return kept
}

// Compile-time interface checks.
var (
	_ prometheus.Registerer = (*familyRegisterer)(nil)
	_ metric.MeterProvider  = (*familyMeterProvider)(nil)
	_ metric.Meter          = (*familyMeter)(nil)
)
