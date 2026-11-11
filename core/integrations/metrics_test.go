// file: core/integrations/metrics_test.go
// version: 1.0.0
// guid: 6470e005-c798-4bff-a592-f6c0acba60de
// last-edited: 2026-11-11

package integrations_test

import (
	"context"
	"testing"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/metrics/metricstest"
)

// TestIntegrationsSeries_Golden (M1.8, 05-metrics §5 item 5): the
// integrations scope serves exactly the families of testdata/series.golden,
// and each moves with what it counts.
func TestIntegrationsSeries_Golden(t *testing.T) {
	ctx := context.Background()
	p, err := metrics.New(ctx, metrics.Options{AppID: "gotham", Prometheus: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	sc, err := p.Core("integrations", "kind", "adapter", "instance", "status")
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t)
	r := e.registry(t, nil, func(o *integrations.Options) { o.Metrics = sc })
	mustPut(t, admin(), r, signal("http://192.0.2.20/"), cred("alfred-1"))
	if h, _ := r.Health(ctx, signalRef); h.Status != integrations.HealthOK {
		t.Fatalf("Health = %+v", h)
	}
	if err := r.SetOverlay(ctx, overlay("integrations.notify.gcpd-roof.channel", "batcave")); err != nil {
		t.Fatal(err)
	}
	metricstest.AssertSeries(t, p, sc, "testdata/series.golden")
	metricstest.AssertNoOverflow(t, p)

	mfs, err := p.Gatherer().Gather()
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]float64{}
	for _, mf := range mfs {
		for _, m := range mf.GetMetric() {
			key := mf.GetName()
			for _, lp := range m.GetLabel() {
				key += " " + lp.GetName() + "=" + lp.GetValue()
			}
			switch {
			case m.GetGauge() != nil:
				values[key] = m.GetGauge().GetValue()
			case m.GetCounter() != nil:
				values[key] = m.GetCounter().GetValue()
			case m.GetHistogram() != nil:
				values[key] = float64(m.GetHistogram().GetSampleCount())
			}
		}
	}
	for key, want := range map[string]float64{
		"mediacore_integrations_instances adapter=hello.batsignal kind=notify status=ok":  1,
		"mediacore_integrations_health_status instance=gcpd-roof kind=notify":             float64(integrations.HealthOK),
		"mediacore_integrations_health_probe_seconds adapter=hello.batsignal kind=notify": 1,
		"mediacore_integrations_swaps_total instance=gcpd-roof kind=notify":               1,
	} {
		if got, ok := values[key]; !ok || got != want {
			t.Errorf("%s = %v (present %v), want %v", key, got, ok, want)
		}
	}
}
