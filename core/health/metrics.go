// file: core/health/metrics.go
// version: 1.0.0
// guid: 0f0f4a3c-6d8b-4b7e-9c58-2f8d0b8a4e11
// last-edited: 2026-11-13

package health

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/falkcorp/mediacore/metrics"
)

// Metrics are 05-health §5H.8's series. They live in three core scopes,
// because core fixes a scope's prefix at "<namespace>.<subsystem>" and the
// spec's names span three subsystems:
//
//	<ns>_health_component_status{component}           gauge 0 unknown .. 3 down
//	<ns>_health_check_duration_seconds{component}     histogram
//	<ns>_health_check_failures_total{component,reason} counter
//	<ns>_health_ready                                  gauge 0/1
//	<ns>_health_startup_degraded{step}                 gauge 0/1
//	<ns>_diagnostics_bundles_total{outcome}            counter
//	<ns>_diagnostics_redactions_total{layer}           counter
//	<ns>_shutdown_phase_seconds{phase}                 histogram
//
// A nil *Metrics records nothing, so a Registry, Startup or stop budget built
// without one runs unchanged.
type Metrics struct {
	checkDuration metric.Float64Histogram
	failures      metric.Int64Counter
	ready         metric.Int64Gauge
	startup       metric.Int64Gauge
	bundles       metric.Int64Counter
	redactions    metric.Int64Counter
	phase         metric.Float64Histogram
	// status is read by an observable callback over the registries that
	// attached themselves; see Metrics.attach.
	status metric.Int64ObservableGauge
	reg    *Registry
}

// ScopeAttributes are the label keys the health scope declares. A function,
// not a variable: core forbids package-level mutable state (03 R5).
func ScopeAttributes() []attribute.Key {
	return []attribute.Key{"component", "reason", "step"}
}

// checkBuckets are check_duration_seconds' boundaries: a check is an engine
// ping or an in-memory read, and the default timeout is 5 s.
func checkBuckets() []float64 {
	return []float64{0.0005, 0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 2.5, 5, 10}
}

// phaseBuckets are shutdown_phase_seconds' boundaries: the budget is 25 s.
func phaseBuckets() []float64 {
	return []float64{0.01, 0.05, 0.1, 0.5, 1, 2.5, 5, 10, 25, 60}
}

// NewMetrics registers the families on the provider's three core scopes:
// "health", "diagnostics" and "shutdown". A scope already taken is an error
// (metrics.ErrScopeTaken), so the kernel builds one Metrics per App.
func NewMetrics(p *metrics.Provider) (*Metrics, error) {
	if p == nil {
		return nil, fmt.Errorf("health: NewMetrics needs the metrics provider")
	}
	hs, err := p.Core("health", ScopeAttributes()...)
	if err != nil {
		return nil, fmt.Errorf("health: metrics scope: %w", err)
	}
	ds, err := p.Core("diagnostics", "outcome", "layer")
	if err != nil {
		return nil, fmt.Errorf("health: diagnostics metrics scope: %w", err)
	}
	ss, err := p.Core("shutdown", "phase")
	if err != nil {
		return nil, fmt.Errorf("health: shutdown metrics scope: %w", err)
	}
	m := &Metrics{}
	hm, hp := hs.Meter(), hs.Prefix()
	if err := hs.View(hp+".check_duration", checkBuckets()...); err != nil {
		return nil, fmt.Errorf("health: check duration view: %w", err)
	}
	if err := ss.View(ss.Prefix()+".phase", phaseBuckets()...); err != nil {
		return nil, fmt.Errorf("health: shutdown phase view: %w", err)
	}
	if m.checkDuration, err = hm.Float64Histogram(hp+".check_duration", metric.WithUnit("s"),
		metric.WithDescription("Time one component check took.")); err != nil {
		return nil, fmt.Errorf("health: check duration: %w", err)
	}
	if m.failures, err = hm.Int64Counter(hp+".check_failures",
		metric.WithDescription("Component checks that came back Down, by component and reason.")); err != nil {
		return nil, fmt.Errorf("health: check failures: %w", err)
	}
	if m.ready, err = hm.Int64Gauge(hp+".ready",
		metric.WithDescription("1 while the process is ready (the /readyz answer), else 0.")); err != nil {
		return nil, fmt.Errorf("health: ready gauge: %w", err)
	}
	if m.startup, err = hm.Int64Gauge(hp+".startup_degraded",
		metric.WithDescription("1 for a Degraded startup step that failed, 0 for one that ran clean.")); err != nil {
		return nil, fmt.Errorf("health: startup gauge: %w", err)
	}
	if m.status, err = hm.Int64ObservableGauge(hp+".component_status",
		metric.WithDescription("Component status: 0 unknown, 1 up, 2 degraded, 3 down.")); err != nil {
		return nil, fmt.Errorf("health: status gauge: %w", err)
	}
	if _, err = hm.RegisterCallback(m.observeStatus, m.status); err != nil {
		return nil, fmt.Errorf("health: status callback: %w", err)
	}
	dm, dp := ds.Meter(), ds.Prefix()
	if m.bundles, err = dm.Int64Counter(dp+".bundles",
		metric.WithDescription("Diagnostics bundles generated, by outcome.")); err != nil {
		return nil, fmt.Errorf("health: bundles counter: %w", err)
	}
	if m.redactions, err = dm.Int64Counter(dp+".redactions",
		metric.WithDescription("Values a diagnostics bundle redacted or left out, by layer (typed, pattern, permission).")); err != nil {
		return nil, fmt.Errorf("health: redactions counter: %w", err)
	}
	if m.phase, err = ss.Meter().Float64Histogram(ss.Prefix()+".phase", metric.WithUnit("s"),
		metric.WithDescription("Time one shutdown phase took.")); err != nil {
		return nil, fmt.Errorf("health: shutdown phase: %w", err)
	}
	return m, nil
}

// attach binds the registry the component_status gauge reads. One Metrics
// serves one Registry: a second attach replaces the first.
func (m *Metrics) attach(r *Registry) {
	if m == nil {
		return
	}
	m.reg = r
}

// observeStatus is the component_status callback.
func (m *Metrics) observeStatus(_ context.Context, o metric.Observer) error {
	r := m.reg
	if r == nil {
		return nil
	}
	for _, c := range r.Snapshot().Components {
		o.ObserveInt64(m.status, int64(c.Result.Status), metric.WithAttributes(attribute.String("component", string(c.ID))))
	}
	return nil
}

func (m *Metrics) recordCheck(id ComponentID, took time.Duration, res Result, failed bool) {
	if m == nil {
		return
	}
	ctx := context.Background()
	comp := attribute.String("component", string(id))
	m.checkDuration.Record(ctx, took.Seconds(), metric.WithAttributes(comp))
	if failed {
		reason := res.Reason
		if reason == "" {
			reason = "down"
		}
		m.failures.Add(ctx, 1, metric.WithAttributes(comp, attribute.String("reason", reason)))
	}
}

func (m *Metrics) recordReady(ready bool) {
	if m == nil {
		return
	}
	v := int64(0)
	if ready {
		v = 1
	}
	m.ready.Record(context.Background(), v)
}

func (m *Metrics) recordStartup(step string, degraded bool) {
	if m == nil {
		return
	}
	v := int64(0)
	if degraded {
		v = 1
	}
	m.startup.Record(context.Background(), v, metric.WithAttributes(attribute.String("step", step)))
}

// Bundle counts one diagnostics bundle by outcome ("ok", "error", "partial").
func (m *Metrics) Bundle(outcome string) {
	if m == nil {
		return
	}
	m.bundles.Add(context.Background(), 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}

// Redacted counts n values a bundle redacted or left out at layer ("typed",
// "pattern" or "permission").
func (m *Metrics) Redacted(layer string, n int) {
	if m == nil || n <= 0 {
		return
	}
	m.redactions.Add(context.Background(), int64(n), metric.WithAttributes(attribute.String("layer", layer)))
}

func (m *Metrics) recordPhase(phase string, d time.Duration) {
	if m == nil {
		return
	}
	m.phase.Record(context.Background(), d.Seconds(), metric.WithAttributes(attribute.String("phase", phase)))
}
