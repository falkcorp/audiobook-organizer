// file: core/integrations/download/metrics_test.go
// version: 1.0.0
// guid: b956a1be-89d7-4987-8447-644c1610397f
// last-edited: 2026-11-11

package download_test

import (
	"context"
	"testing"

	"github.com/falkcorp/mediacore/integrations/download"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/metrics/metricstest"
)

// TestDownloadSeries_Golden (05-metrics §12): the download scope serves exactly
// the phase-1 families of testdata/series.golden, and each moves with what it
// counts.
func TestDownloadSeries_Golden(t *testing.T) {
	ctx := context.Background()
	p, err := metrics.New(ctx, metrics.Options{AppID: "gotham", Prometheus: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	sc, err := p.Core("download", "adapter", "instance")
	if err != nil {
		t.Fatal(err)
	}

	h := newHarness(t)
	f := h.addFake(seedbox, crateAdapter, 0)
	f.Add(item("aa", "Year One", "/remote/books", "completed", 1))
	f.Add(item("bb", "Hush", "/elsewhere", "levitating", 1))     // an unknown client state, and no mapping
	f.Add(item("cc", "Owls", "/elsewhere/too", "levitating", 1)) // a second of each
	pool := h.newPool(download.PoolOptions{Paths: standardMappings(), Metrics: sc})
	if _, err := pool.Items(ctx, seedbox, download.ItemFilter{}); err != nil {
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
			if m.GetCounter() != nil {
				values[key] = m.GetCounter().GetValue()
			}
		}
	}
	for key, want := range map[string]float64{
		"mediacore_download_unknown_state_total adapter=hello.batcrate": 2,
		"mediacore_download_unmapped_paths_total instance=seedbox":      2,
	} {
		if got, ok := values[key]; !ok || got != want {
			t.Errorf("%s = %v (present %v), want %v", key, got, ok, want)
		}
	}
}

// A Pool without a metrics scope records nothing and does not fail.
func TestPool_NoMetricsScope(t *testing.T) {
	h := newHarness(t)
	f := h.addFake(seedbox, crateAdapter, 0)
	f.Add(item("bb", "Hush", "/elsewhere", "levitating", 1))
	if _, err := h.newPool(download.PoolOptions{}).Items(context.Background(), seedbox, download.ItemFilter{}); err != nil {
		t.Fatal(err)
	}
}
