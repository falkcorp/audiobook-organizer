// file: internal/plugins/dedup/calibrate_embedding_thresholds_test.go
// version: 1.0.0
// guid: 8a7b6c5d-4e3f-4a2b-9c1d-0e9f8a7b6c5d
// last-edited: 2026-07-03

package dedup

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// fakeEmbGetter is a map-backed embeddingGetter for the calibration tests.
// A missing key returns (nil, nil), which the collector treats as a missing
// embedding (skippedMissing).
type fakeEmbGetter struct {
	m map[string]*database.Embedding
}

func (f fakeEmbGetter) Get(_ /*entityType*/, entityID string) (*database.Embedding, error) {
	return f.m[entityID], nil
}

// TestSweepThreshold_RecommendsBetweenClusters verifies that when true_dup
// cosines cluster high and not_dup cosines cluster low, the sweep recommends a
// cut-point between the two clusters at the target precision.
func TestSweepThreshold_RecommendsBetweenClusters(t *testing.T) {
	pairs := []calibrationPair{
		{label: "true_dup", cosine: 0.96},
		{label: "true_dup", cosine: 0.97},
		{label: "true_dup", cosine: 0.98},
		{label: "not_dup", cosine: 0.80},
		{label: "not_dup", cosine: 0.81},
		{label: "not_dup", cosine: 0.82},
	}

	rec := sweepThreshold(pairs, 0.98)
	if !rec.Met {
		t.Fatalf("expected a threshold to meet target 0.98, got Met=false")
	}
	// Recommendation must separate the clusters: above the highest not_dup
	// (0.82) and at/below the lowest true_dup (0.96).
	if rec.Threshold <= 0.82 || rec.Threshold > 0.96 {
		t.Fatalf("threshold %.4f not between clusters (0.82, 0.96]", rec.Threshold)
	}
	if rec.Precision < 0.98 {
		t.Fatalf("precision %.4f below target 0.98", rec.Precision)
	}
	// Lowest qualifying cut-point maximises recall; here all 3 true_dup are
	// captured with zero false positives → precision 1.0, recall 1.0.
	if rec.Precision != 1.0 || rec.Recall != 1.0 {
		t.Fatalf("expected precision=1.0 recall=1.0, got p=%.4f r=%.4f", rec.Precision, rec.Recall)
	}
}

// TestSweepThreshold_NoThresholdMeetsTarget verifies that when the classes
// overlap so no cut-point can reach the target precision, the sweep reports
// Met=false rather than fabricating a recommendation.
func TestSweepThreshold_NoThresholdMeetsTarget(t *testing.T) {
	// not_dup pairs sit ABOVE the true_dup pairs, so every cut-point that
	// captures a true_dup also captures a not_dup — precision can never reach
	// 0.98.
	pairs := []calibrationPair{
		{label: "true_dup", cosine: 0.88},
		{label: "true_dup", cosine: 0.89},
		{label: "not_dup", cosine: 0.90},
		{label: "not_dup", cosine: 0.91},
		{label: "not_dup", cosine: 0.92},
	}

	rec := sweepThreshold(pairs, 0.98)
	if rec.Met {
		t.Fatalf("expected Met=false (no separating cut-point), got threshold=%.4f p=%.4f", rec.Threshold, rec.Precision)
	}
}

// TestCollectCalibrationPairs_SkipsMismatchAndMissing verifies the DEDUP-3
// contamination guard: cross-model, dimension-mismatched, and missing-embedding
// pairs are skipped and counted, never scored — no panic, no precision
// inflation.
func TestCollectCalibrationPairs_SkipsMismatchAndMissing(t *testing.T) {
	const model = "bge-m3"
	vec3 := []float32{0.1, 0.2, 0.3}
	vec3b := []float32{0.3, 0.2, 0.1}
	vec2 := []float32{0.5, 0.5} // wrong dimension

	getter := fakeEmbGetter{m: map[string]*database.Embedding{
		// Valid same-model, same-dim pair → scored.
		"good_a": {Model: model, Vector: vec3},
		"good_b": {Model: model, Vector: vec3b},
		// Wrong model on one side → skippedMismatch.
		"wrongmodel_a": {Model: model, Vector: vec3},
		"wrongmodel_b": {Model: "text-embedding-3-large", Vector: vec3b},
		// Dimension mismatch (both tagged bge-m3 but different lengths) → skippedMismatch.
		"dim_a": {Model: model, Vector: vec3},
		"dim_b": {Model: model, Vector: vec2},
		// "missing_b" intentionally absent → skippedMissing.
		"missing_a": {Model: model, Vector: vec3},
	}}

	examples := []database.LabeledExample{
		{EntityAID: "good_a", EntityBID: "good_b", Label: "true_dup"},
		{EntityAID: "wrongmodel_a", EntityBID: "wrongmodel_b", Label: "true_dup"},
		{EntityAID: "dim_a", EntityBID: "dim_b", Label: "not_dup"},
		{EntityAID: "missing_a", EntityBID: "missing_b", Label: "true_dup"},
	}

	pairs, skippedMissing, skippedMismatch := collectCalibrationPairs(examples, getter, model)

	if len(pairs) != 1 {
		t.Fatalf("expected exactly 1 scored pair, got %d", len(pairs))
	}
	if pairs[0].label != "true_dup" {
		t.Fatalf("scored pair label = %q, want true_dup", pairs[0].label)
	}
	if skippedMismatch != 2 {
		t.Fatalf("skippedMismatch = %d, want 2 (wrong-model + wrong-dim)", skippedMismatch)
	}
	if skippedMissing != 1 {
		t.Fatalf("skippedMissing = %d, want 1", skippedMissing)
	}
}
