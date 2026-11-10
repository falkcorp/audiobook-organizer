// file: core/audit/metrics_test.go
// version: 1.0.0
// guid: a1b7c3e9-5f24-4d68-9b0a-2c8e4f6d7a35
// last-edited: 2026-11-10

package audit

import (
	"context"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"

	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/metrics/metricstest"
)

// auditScope builds a Provider and the core "audit" scope the kernel gives the
// store (05-rbac §11).
func auditScope(t *testing.T) (*metrics.Provider, *metrics.Scope) {
	t.Helper()
	p, err := metrics.New(context.Background(), metrics.Options{AppID: "gotham", Prometheus: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	sc, err := p.Core("audit", "namespace", "outcome", "path")
	if err != nil {
		t.Fatal(err)
	}
	return p, sc
}

// value is the value of the series of family whose labels equal want (a
// subset match), and whether it exists.
func value(t *testing.T, p *metrics.Provider, family string, want map[string]string) (float64, bool) {
	t.Helper()
	mfs, err := p.Gatherer().Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != family {
			continue
		}
	next:
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, lp := range m.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			for k, v := range want {
				if got, ok := labels[k]; !ok || got != v {
					continue next
				}
			}
			return sampleOf(m), true
		}
	}
	return 0, false
}

func sampleOf(m *dto.Metric) float64 {
	switch {
	case m.GetCounter() != nil:
		return m.GetCounter().GetValue()
	case m.GetGauge() != nil:
		return m.GetGauge().GetValue()
	}
	return m.GetUntyped().GetValue()
}

// TestAuditSeries_Golden (05-rbac §11): the audit scope serves exactly the
// five families of testdata/series.golden, and each moves with what it counts.
func TestAuditSeries_Golden(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	p, sc := auditScope(t)
	// Built before the Provider's scope exists, as the kernel builds it (the
	// store is made before P4b, the Provider at P5): Instrument comes later,
	// and what was recorded before it is still counted.
	s := newStoreOn(t, e, Options{AsyncBuffer: 1, BatchWait: time.Millisecond}, Deps{})
	if err := s.Record(ctx, success("core.auth.login")); err != nil {
		t.Fatal(err)
	}
	if err := s.Instrument(sc); err != nil {
		t.Fatal(err)
	}
	if err := s.Instrument(sc); err == nil {
		t.Error("a second Instrument succeeded")
	}
	if err := s.Instrument(nil); err == nil {
		t.Error("Instrument(nil) succeeded")
	}
	metricstest.AssertSeries(t, p, sc, "testdata/series.golden")

	get := func(family string, labels map[string]string) float64 {
		t.Helper()
		v, ok := value(t, p, family, labels)
		if !ok {
			t.Fatalf("no %s series with labels %v", family, labels)
		}
		return v
	}
	if v := get("mediacore_audit_records_total", map[string]string{"namespace": "core", "outcome": "success"}); v != 1 {
		t.Errorf("records{core,success} = %v, want 1 (written before Instrument)", v)
	}
	for _, path := range []string{"sync", "async"} {
		if v := get("mediacore_audit_write_errors_total", map[string]string{"path": path}); v != 0 {
			t.Errorf("write_errors{%s} = %v, want a zero series", path, v)
		}
	}
	if v := get("mediacore_audit_pruned_total", nil); v != 0 {
		t.Errorf("pruned = %v, want 0", v)
	}

	// A failure and a denial of another namespace; two drops (queue of 1, the
	// writer not started); then the queue depth reads 1.
	if err := s.Record(ctx, Event{Action: "books.vehicle.merge", Outcome: Failure}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		s.Emit(ctx, Event{Action: "core.auth.login", Outcome: Failure})
	}
	if v := get("mediacore_audit_dropped_total", nil); v != 2 {
		t.Errorf("dropped = %v, want 2", v)
	}
	if v := get("mediacore_audit_queue_depth", nil); v != 1 {
		t.Errorf("queue_depth = %v, want 1", v)
	}
	if v := get("mediacore_audit_records_total", map[string]string{"namespace": "books", "outcome": "failure"}); v != 1 {
		t.Errorf("records{books,failure} = %v, want 1", v)
	}
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if v := get("mediacore_audit_queue_depth", nil); v != 0 {
		t.Errorf("queue_depth after Stop = %v, want 0", v)
	}
	if v := get("mediacore_audit_records_total", map[string]string{"namespace": "core", "outcome": "failure"}); v != 1 {
		t.Errorf("records{core,failure} = %v, want 1 (the drained event)", v)
	}

	// A cancelled sync write counts on the sync path.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.Record(cancelled, success("core.auth.logout")); err == nil {
		t.Fatal("Record under a cancelled context succeeded")
	}
	if v := get("mediacore_audit_write_errors_total", map[string]string{"path": "sync"}); v != 1 {
		t.Errorf("write_errors{sync} = %v, want 1", v)
	}
}

// TestAuditSeries_DepsMetrics: New instruments the store itself when
// Deps.Metrics is set.
func TestAuditSeries_DepsMetrics(t *testing.T) {
	e := newEnv(t)
	p, sc := auditScope(t)
	s := newStoreOn(t, e, Options{}, Deps{Metrics: sc})
	if err := s.Record(context.Background(), success("core.auth.login")); err != nil {
		t.Fatal(err)
	}
	metricstest.AssertSeries(t, p, sc, "testdata/series.golden")
	if err := s.Instrument(sc); err == nil {
		t.Error("Instrument after New with Deps.Metrics succeeded")
	}
}
