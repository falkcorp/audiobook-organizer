// file: internal/telemetry/views_test.go
// version: 1.0.0
// guid: 9206a485-484e-40b8-ac6e-dbc908ef8bdd
// last-edited: 2026-10-09

package telemetry

import (
	"context"
	"slices"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// TestViews_AppliedPerEntry: Views() yields one view per table entry, and an
// instrument of each entry's name gets exactly that entry's boundaries from a
// provider built with them. The golden-driven checks (every exported
// histogram has an entry; the entries equal today's live buckets) live in
// internal/telemetry/contract, which can scrape the seeded families without
// this package importing internal/metrics or internal/aidispatch.
func TestViews_AppliedPerEntry(t *testing.T) {
	if got, want := len(Views()), len(histogramBuckets); got != want {
		t.Fatalf("Views() has %d options, table has %d entries", got, want)
	}
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(append([]sdkmetric.Option{sdkmetric.WithReader(reader)}, Views()...)...)
	m := provider.Meter("views-test")
	for name, bounds := range histogramBuckets {
		if !slices.IsSorted(bounds) || len(slices.Compact(slices.Clone(bounds))) != len(bounds) {
			t.Errorf("%s: bounds %v must be strictly ascending", name, bounds)
		}
		h, err := m.Float64Histogram(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		h.Record(context.Background(), 1)
	}
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, sm := range rm.ScopeMetrics {
		for _, md := range sm.Metrics {
			h, ok := md.Data.(metricdata.Histogram[float64])
			if !ok || len(h.DataPoints) == 0 {
				t.Errorf("%s: no histogram data point", md.Name)
				continue
			}
			seen++
			if want := histogramBuckets[md.Name]; !slices.Equal(h.DataPoints[0].Bounds, want) {
				t.Errorf("%s: applied bounds %v, table %v", md.Name, h.DataPoints[0].Bounds, want)
			}
		}
	}
	if seen != len(histogramBuckets) {
		t.Errorf("collected %d histograms, want %d", seen, len(histogramBuckets))
	}
}

func TestHistogramBuckets_ReturnsCopy(t *testing.T) {
	got := HistogramBuckets()
	for name := range got {
		got[name][0] = -1
	}
	for name, b := range histogramBuckets {
		if b[0] == -1 {
			t.Fatalf("HistogramBuckets leaked the table: %s was mutated", name)
		}
	}
}
