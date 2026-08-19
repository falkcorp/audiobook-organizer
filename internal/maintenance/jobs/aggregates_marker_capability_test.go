// file: internal/maintenance/jobs/aggregates_marker_capability_test.go
// version: 1.0.0
// guid: 2f6b8d15-7a34-49c0-8e72-1b5d3c9a06f8
// last-edited: 2026-08-19

package jobs

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

type markerStore struct {
	database.Store
}

func (markerStore) IsBookAggregatesBackfillDone() bool    { return true }
func (markerStore) MarkBookAggregatesBackfillDone() error { return nil }

type markerDecorator struct {
	database.Store
	inner database.Store
}

func (d markerDecorator) Unwrap() database.Store { return d.inner }

// TestResolveAggregatesBackfillMarkerThroughDecorator pins the short-circuit.
// Neither method is on database.Store (compile-probed), so a bare assertion
// fails through the production decorator and the job redoes the entire
// 40k-book backfill on every run instead of returning early.
func TestResolveAggregatesBackfillMarkerThroughDecorator(t *testing.T) {
	wrapped := markerDecorator{inner: markerStore{}}

	got := resolveAggregatesBackfillMarker(wrapped)
	if got == nil {
		t.Fatal("resolveAggregatesBackfillMarker returned nil through the decorator; the 40k-book backfill would rerun every time")
	}
	if !got.IsBookAggregatesBackfillDone() {
		t.Fatal("IsBookAggregatesBackfillDone() = false through the decorator, want true")
	}
}

func TestResolveAggregatesBackfillMarkerOnUncapableBackend(t *testing.T) {
	type plain struct{ database.Store }
	if got := resolveAggregatesBackfillMarker(plain{}); got != nil {
		t.Fatalf("resolveAggregatesBackfillMarker = %v without the sentinel, want nil", got)
	}
}
