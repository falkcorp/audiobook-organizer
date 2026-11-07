// file: internal/telemetry/views_test.go
// version: 2.0.0
// guid: 9206a485-484e-40b8-ac6e-dbc908ef8bdd
// last-edited: 2026-11-07

package telemetry

import (
	"math"
	"slices"
	"testing"
)

// TestHistogramBuckets_TableSane: every entry is a non-empty, strictly
// ascending list of finite boundaries, the shape
// metric.WithExplicitBucketBoundaries accepts as advice (the SDK drops
// invalid advice and falls back to its defaults). Whether each served
// histogram actually carries its entry is checked against the seeded scrape
// in internal/telemetrypolicy (TestEveryHistogramHasAView), which can import
// the packages that create the instruments.
func TestHistogramBuckets_TableSane(t *testing.T) {
	if len(histogramBuckets) == 0 {
		t.Fatal("histogramBuckets is empty")
	}
	for name, bounds := range histogramBuckets {
		if len(bounds) == 0 {
			t.Errorf("%s: no boundaries", name)
			continue
		}
		for _, b := range bounds {
			if math.IsNaN(b) || math.IsInf(b, 0) {
				t.Errorf("%s: boundary %v is not finite", name, b)
			}
		}
		if !slices.IsSorted(bounds) || len(slices.Compact(slices.Clone(bounds))) != len(bounds) {
			t.Errorf("%s: bounds %v must be strictly ascending", name, bounds)
		}
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
