// file: internal/metrics/sort_by_requested_test.go
// version: 1.0.0
// guid: 4f81c2d7-6a3e-4b95-b0c8-2e7d9a1f5c36
// last-edited: 2026-09-11

package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestIncSortByRequested_CountsPerLabel(t *testing.T) {
	sortByRequestedTotal.Reset()
	t.Cleanup(sortByRequestedTotal.Reset)

	IncSortByRequested("author")
	IncSortByRequested("author")
	IncSortByRequested("default")

	if got := testutil.ToFloat64(sortByRequestedTotal.WithLabelValues("author")); got != 2 {
		t.Errorf("author = %v, want 2", got)
	}
	if got := testutil.ToFloat64(sortByRequestedTotal.WithLabelValues("default")); got != 1 {
		t.Errorf("default = %v, want 1", got)
	}
	if got := testutil.CollectAndCount(sortByRequestedTotal); got != 2 {
		t.Errorf("series = %d, want 2 (one per label used)", got)
	}
}
