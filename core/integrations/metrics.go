// file: core/integrations/metrics.go
// version: 1.0.0
// guid: 20ea4a87-4f0e-4c8b-9a51-6f0d2c7b3e19
// last-edited: 2026-11-11

package integrations

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/falkcorp/mediacore/metrics"
)

// The core "integrations" scope (M1.8), labels kind, adapter, instance and
// status:
//
//	<ns>_integrations_instances{kind,adapter,status}       gauge
//	<ns>_integrations_health_status{kind,instance}         gauge (HealthStatus value)
//	<ns>_integrations_health_probe_seconds{kind,adapter}   histogram (no instance label: C5)
//	<ns>_integrations_swaps_total{kind,instance}           counter
//
// The two gauges are observed from the instance snapshot at collection, so
// nothing is recorded on the lease path. core.integrations.max_instances_per_kind
// bounds the instance label's cardinality.

// probeBuckets are health_probe's bucket boundaries, in seconds: a probe is
// one request bounded by core.integrations.probe_timeout (default 10s).
func probeBuckets() []float64 { return []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30} }

// statusUnknown is the status label of an instance not yet probed.
const statusUnknown = "unknown"

// instruments are the Registry's synchronous instruments.
type instruments struct {
	probe metric.Float64Histogram
	swaps metric.Int64Counter
}

// instrument creates the scope's instruments on sc, the core
// "integrations" scope.
func (r *Registry) instrument(sc *metrics.Scope) (*instruments, error) {
	m, prefix := sc.Meter(), sc.Prefix()
	probeName := prefix + ".health_probe"
	if err := sc.View(probeName, probeBuckets()...); err != nil {
		return nil, fmt.Errorf("integrations: health probe view: %w", err)
	}
	probe, err := m.Float64Histogram(probeName, metric.WithUnit("s"),
		metric.WithDescription("Duration of an instance's active health probe, by kind and adapter (05-metadata M1.8)."))
	if err != nil {
		return nil, fmt.Errorf("integrations: health probe histogram: %w", err)
	}
	swaps, err := m.Int64Counter(prefix+".swaps",
		metric.WithDescription("Instances swapped out by a change of their effective view (M1.4 step 4)."))
	if err != nil {
		return nil, fmt.Errorf("integrations: swaps counter: %w", err)
	}
	insts, err := m.Int64ObservableGauge(prefix+".instances",
		metric.WithDescription("Effective integration instances, by kind, adapter and health status."))
	if err != nil {
		return nil, fmt.Errorf("integrations: instances gauge: %w", err)
	}
	status, err := m.Int64ObservableGauge(prefix+".health_status",
		metric.WithDescription("Each instance's health status: 0 ok, 1 degraded, 2 down, 3 auth_failed, 4 misconfigured, 5 disabled."))
	if err != nil {
		return nil, fmt.Errorf("integrations: health status gauge: %w", err)
	}
	if _, err := m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		r.observeGauges(o, insts, status)
		return nil
	}, insts, status); err != nil {
		return nil, fmt.Errorf("integrations: register gauges: %w", err)
	}
	return &instruments{probe: probe, swaps: swaps}, nil
}

// instanceKey is one series of the instances gauge.
type instanceKey struct{ kind, adapter, status string }

// observeGauges observes both gauges from the current snapshot.
func (r *Registry) observeGauges(o metric.Observer, insts, status metric.Int64Observable) {
	counts := map[instanceKey]int64{}
	for ref, s := range r.snap.Load().slots {
		st, known := r.statusOf(s)
		label := statusUnknown
		if known {
			label = st.String()
			o.ObserveInt64(status, int64(st), metric.WithAttributes(
				attribute.String("kind", string(ref.Kind)), attribute.String("instance", string(ref.Instance))))
		}
		counts[instanceKey{kind: string(ref.Kind), adapter: string(s.cfg.Adapter), status: label}]++
	}
	for k, n := range counts {
		o.ObserveInt64(insts, n, metric.WithAttributes(
			attribute.String("kind", k.kind), attribute.String("adapter", k.adapter), attribute.String("status", k.status)))
	}
}

// statusOf is s's status without a probe: a static finding, else the last
// probe's answer; false when neither exists yet.
func (r *Registry) statusOf(s *slot) (HealthStatus, bool) {
	switch {
	case r.settings().Disabled:
		return HealthDisabled, true
	case s.problem != "":
		return HealthMisconfigured, true
	case !s.cfg.Enabled:
		return HealthDisabled, true
	}
	h, ok := r.lastHealth(s.cfg.Ref)
	return h.Status, ok
}

// probed records one active probe's duration.
func (m *instruments) probed(ref Ref, adapter AdapterID, d time.Duration) {
	if m == nil {
		return
	}
	m.probe.Record(context.Background(), d.Seconds(), metric.WithAttributes(
		attribute.String("kind", string(ref.Kind)), attribute.String("adapter", string(adapter))))
}

// swap counts one swapped-out instance.
func (m *instruments) swap(ref Ref) {
	if m == nil {
		return
	}
	m.swaps.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String("kind", string(ref.Kind)), attribute.String("instance", string(ref.Instance))))
}
