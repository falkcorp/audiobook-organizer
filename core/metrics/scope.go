// file: core/metrics/scope.go
// version: 1.0.0
// guid: fc9abe1f-d731-4202-9cdf-0f1e54c27c2b
// last-edited: 2026-11-07

package metrics

import (
	"fmt"
	"slices"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/embedded"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// Scope is what a component or module receives: a naming prefix, a validating
// Meter, a Tracer and the declared attribute keys.
type Scope struct {
	p       *Provider
	prefix  string
	allowed []attribute.Key
	meter   *scopeMeter
	tracer  trace.Tracer
}

// Prefix is the scope's instrument-name prefix, e.g. "mediacore.ops" or, for
// the one module of an app with namespace audiobook_organizer,
// "audiobook_organizer".
func (s *Scope) Prefix() string { return s.prefix }

// Meter enforces the instrument rules (05-metrics §3) over the Provider's
// family-checking MeterProvider. A violation returns ErrInstrumentName and a
// no-op instrument, which the component surfaces so boot fails closed.
func (s *Scope) Meter() metric.Meter { return s.meter }

// Tracer is the tracer named Prefix().
func (s *Scope) Tracer() trace.Tracer { return s.tracer }

// Propagator is the Provider's propagator.
func (s *Scope) Propagator() propagation.TextMapPropagator { return s.p.prop }

// Allowed returns the attribute keys the scope declared; metricstest asserts
// them.
func (s *Scope) Allowed() []attribute.Key { return slices.Clone(s.allowed) }

// View declares a Scope histogram's buckets before the instrument is created.
// instrument is the full instrument name (under Prefix()); buckets must be
// non-empty and strictly increasing. A Scope histogram without a View fails
// ErrInstrumentName at creation.
func (s *Scope) View(instrument string, buckets ...float64) error {
	if err := s.checkPrefix(instrument); err != nil {
		return err
	}
	if len(buckets) == 0 {
		return fmt.Errorf("%w: %q: a view needs at least one bucket boundary", ErrInstrumentName, instrument)
	}
	for i := 1; i < len(buckets); i++ {
		if buckets[i] <= buckets[i-1] {
			return fmt.Errorf("%w: %q: bucket boundaries must be strictly increasing", ErrInstrumentName, instrument)
		}
	}
	s.p.views.set(s.prefix, instrument, buckets)
	return nil
}

// checkPrefix: name is "<prefix>.<seg>(.<seg>)*" with [a-z][a-z0-9_]* segments.
func (s *Scope) checkPrefix(name string) error {
	rest, ok := strings.CutPrefix(name, s.prefix+".")
	if !ok || rest == "" {
		return fmt.Errorf("%w: %q is not under the scope prefix %q", ErrInstrumentName, name, s.prefix)
	}
	for seg := range strings.SplitSeq(rest, ".") {
		if !validSegment(seg) {
			return fmt.Errorf("%w: %q: segment %q must match [a-z][a-z0-9_]*", ErrInstrumentName, name, seg)
		}
	}
	return nil
}

// bracketUnit reports a "{thing}" unit, which the exporter adds no suffix for.
func bracketUnit(unit string) bool {
	return len(unit) > 2 && strings.HasPrefix(unit, "{") && strings.HasSuffix(unit, "}")
}

// check applies the instrument rules for one kind:
//   - counters must not end in "total" (the exporter appends _total);
//   - histograms need a unit ("s", "By" or "{thing}") and a declared View;
//   - a gauge whose name ends in "total" uses a bracket unit, never "1"
//     (which would append _ratio), so its legacy name survives translation.
func (s *Scope) check(name, kind, unit string) error {
	if err := s.checkPrefix(name); err != nil {
		return err
	}
	last := name[strings.LastIndex(name, ".")+1:]
	switch kind {
	case kindCounter, kindObsCounter:
		if strings.HasSuffix(last, "total") {
			return fmt.Errorf("%w: counter %q must not end in total: the exporter appends _total", ErrInstrumentName, name)
		}
	case kindHistogram:
		if unit != "s" && unit != "By" && !bracketUnit(unit) {
			return fmt.Errorf("%w: histogram %q needs a unit of s, By or {thing}, got %q", ErrInstrumentName, name, unit)
		}
		if !s.p.views.has(s.prefix, name) {
			return fmt.Errorf("%w: histogram %q has no View (declare its buckets with Scope.View first)", ErrInstrumentName, name)
		}
	case kindGauge, kindObsGauge, kindUpDownCounter, kindObsUpDown:
		if strings.HasSuffix(last, "total") && !bracketUnit(unit) {
			return fmt.Errorf("%w: gauge %q ends in total and needs a {thing} unit", ErrInstrumentName, name)
		}
	}
	return nil
}

// scopeMeter is Scope.Meter(): the rules, then the family-checking meter.
type scopeMeter struct {
	embedded.Meter
	s *Scope
	m metric.Meter
}

// checked creates an instrument after the scope's rules pass; a violation is
// the no-op instrument and ErrInstrumentName.
func checked[T any](s *Scope, name, kind, unit string, create, refused func() (T, error)) (T, error) {
	if err := s.check(name, kind, unit); err != nil {
		n, _ := refused()
		return n, err
	}
	return create()
}

func (m *scopeMeter) Int64Counter(n string, o ...metric.Int64CounterOption) (metric.Int64Counter, error) {
	return checked(m.s, n, kindCounter, metric.NewInt64CounterConfig(o...).Unit(),
		func() (metric.Int64Counter, error) { return m.m.Int64Counter(n, o...) },
		func() (metric.Int64Counter, error) { return nop().Int64Counter(n) })
}

func (m *scopeMeter) Int64UpDownCounter(n string, o ...metric.Int64UpDownCounterOption) (metric.Int64UpDownCounter, error) {
	return checked(m.s, n, kindUpDownCounter, metric.NewInt64UpDownCounterConfig(o...).Unit(),
		func() (metric.Int64UpDownCounter, error) { return m.m.Int64UpDownCounter(n, o...) },
		func() (metric.Int64UpDownCounter, error) { return nop().Int64UpDownCounter(n) })
}

func (m *scopeMeter) Int64Histogram(n string, o ...metric.Int64HistogramOption) (metric.Int64Histogram, error) {
	return checked(m.s, n, kindHistogram, metric.NewInt64HistogramConfig(o...).Unit(),
		func() (metric.Int64Histogram, error) { return m.m.Int64Histogram(n, o...) },
		func() (metric.Int64Histogram, error) { return nop().Int64Histogram(n) })
}

func (m *scopeMeter) Int64Gauge(n string, o ...metric.Int64GaugeOption) (metric.Int64Gauge, error) {
	return checked(m.s, n, kindGauge, metric.NewInt64GaugeConfig(o...).Unit(),
		func() (metric.Int64Gauge, error) { return m.m.Int64Gauge(n, o...) },
		func() (metric.Int64Gauge, error) { return nop().Int64Gauge(n) })
}

func (m *scopeMeter) Int64ObservableCounter(n string, o ...metric.Int64ObservableCounterOption) (metric.Int64ObservableCounter, error) {
	return checked(m.s, n, kindObsCounter, metric.NewInt64ObservableCounterConfig(o...).Unit(),
		func() (metric.Int64ObservableCounter, error) { return m.m.Int64ObservableCounter(n, o...) },
		func() (metric.Int64ObservableCounter, error) { return nop().Int64ObservableCounter(n) })
}

func (m *scopeMeter) Int64ObservableUpDownCounter(n string, o ...metric.Int64ObservableUpDownCounterOption) (metric.Int64ObservableUpDownCounter, error) {
	return checked(m.s, n, kindObsUpDown, metric.NewInt64ObservableUpDownCounterConfig(o...).Unit(),
		func() (metric.Int64ObservableUpDownCounter, error) { return m.m.Int64ObservableUpDownCounter(n, o...) },
		func() (metric.Int64ObservableUpDownCounter, error) { return nop().Int64ObservableUpDownCounter(n) })
}

func (m *scopeMeter) Int64ObservableGauge(n string, o ...metric.Int64ObservableGaugeOption) (metric.Int64ObservableGauge, error) {
	return checked(m.s, n, kindObsGauge, metric.NewInt64ObservableGaugeConfig(o...).Unit(),
		func() (metric.Int64ObservableGauge, error) { return m.m.Int64ObservableGauge(n, o...) },
		func() (metric.Int64ObservableGauge, error) { return nop().Int64ObservableGauge(n) })
}

func (m *scopeMeter) Float64Counter(n string, o ...metric.Float64CounterOption) (metric.Float64Counter, error) {
	return checked(m.s, n, kindCounter, metric.NewFloat64CounterConfig(o...).Unit(),
		func() (metric.Float64Counter, error) { return m.m.Float64Counter(n, o...) },
		func() (metric.Float64Counter, error) { return nop().Float64Counter(n) })
}

func (m *scopeMeter) Float64UpDownCounter(n string, o ...metric.Float64UpDownCounterOption) (metric.Float64UpDownCounter, error) {
	return checked(m.s, n, kindUpDownCounter, metric.NewFloat64UpDownCounterConfig(o...).Unit(),
		func() (metric.Float64UpDownCounter, error) { return m.m.Float64UpDownCounter(n, o...) },
		func() (metric.Float64UpDownCounter, error) { return nop().Float64UpDownCounter(n) })
}

func (m *scopeMeter) Float64Histogram(n string, o ...metric.Float64HistogramOption) (metric.Float64Histogram, error) {
	return checked(m.s, n, kindHistogram, metric.NewFloat64HistogramConfig(o...).Unit(),
		func() (metric.Float64Histogram, error) { return m.m.Float64Histogram(n, o...) },
		func() (metric.Float64Histogram, error) { return nop().Float64Histogram(n) })
}

func (m *scopeMeter) Float64Gauge(n string, o ...metric.Float64GaugeOption) (metric.Float64Gauge, error) {
	return checked(m.s, n, kindGauge, metric.NewFloat64GaugeConfig(o...).Unit(),
		func() (metric.Float64Gauge, error) { return m.m.Float64Gauge(n, o...) },
		func() (metric.Float64Gauge, error) { return nop().Float64Gauge(n) })
}

func (m *scopeMeter) Float64ObservableCounter(n string, o ...metric.Float64ObservableCounterOption) (metric.Float64ObservableCounter, error) {
	return checked(m.s, n, kindObsCounter, metric.NewFloat64ObservableCounterConfig(o...).Unit(),
		func() (metric.Float64ObservableCounter, error) { return m.m.Float64ObservableCounter(n, o...) },
		func() (metric.Float64ObservableCounter, error) { return nop().Float64ObservableCounter(n) })
}

func (m *scopeMeter) Float64ObservableUpDownCounter(n string, o ...metric.Float64ObservableUpDownCounterOption) (metric.Float64ObservableUpDownCounter, error) {
	return checked(m.s, n, kindObsUpDown, metric.NewFloat64ObservableUpDownCounterConfig(o...).Unit(),
		func() (metric.Float64ObservableUpDownCounter, error) {
			return m.m.Float64ObservableUpDownCounter(n, o...)
		},
		func() (metric.Float64ObservableUpDownCounter, error) { return nop().Float64ObservableUpDownCounter(n) })
}

func (m *scopeMeter) Float64ObservableGauge(n string, o ...metric.Float64ObservableGaugeOption) (metric.Float64ObservableGauge, error) {
	return checked(m.s, n, kindObsGauge, metric.NewFloat64ObservableGaugeConfig(o...).Unit(),
		func() (metric.Float64ObservableGauge, error) { return m.m.Float64ObservableGauge(n, o...) },
		func() (metric.Float64ObservableGauge, error) { return nop().Float64ObservableGauge(n) })
}

func (m *scopeMeter) RegisterCallback(f metric.Callback, insts ...metric.Observable) (metric.Registration, error) {
	return m.m.RegisterCallback(f, insts...)
}

var _ metric.Meter = (*scopeMeter)(nil)
