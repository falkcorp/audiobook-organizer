// file: core/health/metrics_test.go
// version: 1.0.0
// guid: b7e3a5c9-4d12-4f68-9a0e-2c8d6f1b3a74
// last-edited: 2026-11-13

package health_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/metrics/metricstest"
)

func newMetrics(t *testing.T) (*metrics.Provider, *health.Metrics) {
	t.Helper()
	p, err := metrics.New(context.Background(), metrics.Options{AppID: "gotham", Prometheus: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	m, err := health.NewMetrics(p)
	if err != nil {
		t.Fatal(err)
	}
	return p, m
}

// gatherHealth is every series of the three scopes, sorted by family.
func gatherHealth(t *testing.T, p *metrics.Provider) []metricstest.Series {
	t.Helper()
	var all []metricstest.Series
	for _, prefix := range []string{"mediacore_health_", "mediacore_diagnostics_", "mediacore_shutdown_"} {
		got, err := metricstest.Gather(p.Gatherer(), prefix)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, got...)
	}
	slices.SortFunc(all, func(a, b metricstest.Series) int { return strings.Compare(a.Family, b.Family) })
	return all
}

// TestHealthSeries_Golden: the three scopes serve exactly the families of
// testdata/series.golden (05-health §5H.8), each fed by the code that owns it.
func TestHealthSeries_Golden(t *testing.T) {
	p, m := newMetrics(t)
	r := health.NewRegistryWith(health.Options{Metrics: m})

	rep, err := r.Register(health.Check{ID: "wayne.batcave", Readiness: true, Probe: func(context.Context) health.Result {
		return health.Result{Status: health.StatusDown, Reason: "bat_signal_dark"}
	}})
	if err != nil || rep == nil {
		t.Fatal(err)
	}
	su, err := health.NewStartup(r, nil, health.StartupStep{Name: "searchindex", Class: health.Degraded, Token: "search_index", Reason: "falls back"})
	if err != nil {
		t.Fatal(err)
	}
	if err := su.Run(context.Background(), "searchindex", func(context.Context) error { return errors.New("closed") }); err != nil {
		t.Fatal(err)
	}
	su.Complete()
	m.Bundle("ok")
	m.Redacted("pattern", 3)
	b := health.NewStopBudget(time.Second, health.StopLimits{}, m)
	if err := b.Run(context.Background(), health.PhaseHooks, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}

	want, err := metricstest.ReadGolden("testdata/series.golden")
	if err != nil {
		t.Fatal(err)
	}
	if d := metricstest.Diff(want, gatherHealth(t, p)); len(d) > 0 {
		t.Errorf("health scopes do not match testdata/series.golden:\n\t%s", strings.Join(d, "\n\t"))
	}
	metricstest.AssertNoOverflow(t, p)
}

// TestHealthMetrics_NilRecordsNothing: a Registry, Startup, StopBudget and
// diagnostics counters built without Metrics run unchanged.
func TestHealthMetrics_NilRecordsNothing(t *testing.T) {
	var m *health.Metrics
	m.Bundle("ok")
	m.Redacted("typed", 2)
	r := health.NewRegistry()
	if _, err := r.Register(health.Check{ID: "wayne.batcave", Probe: func(context.Context) health.Result { return health.Result{Status: health.StatusUp} }}); err != nil {
		t.Fatal(err)
	}
	if _, err := health.NewMetrics(nil); err == nil {
		t.Error("NewMetrics(nil) succeeded")
	}
}
