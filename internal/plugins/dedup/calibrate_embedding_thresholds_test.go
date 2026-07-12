// file: internal/plugins/dedup/calibrate_embedding_thresholds_test.go
// version: 1.2.0
// guid: 8a7b6c5d-4e3f-4a2b-9c1d-0e9f8a7b6c5d
// last-edited: 2026-07-11

package dedup

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// captureReporter is an sdk.Reporter whose Logger() writes JSON records to an
// in-memory buffer, so a test can inspect the structured report fields emitted
// by an op (here: rows_in / pairs_out on the "…report" line).
type captureReporter struct {
	buf    *bytes.Buffer
	logger *slog.Logger
}

func newCaptureReporter() *captureReporter {
	buf := &bytes.Buffer{}
	return &captureReporter{buf: buf, logger: slog.New(slog.NewJSONHandler(buf, nil))}
}

func (r *captureReporter) UpdateProgress(_, _ int, _ string) error          { return nil }
func (r *captureReporter) Log(_ slog.Level, _ string, _ ...slog.Attr) error { return nil }
func (r *captureReporter) Logger() *slog.Logger                             { return r.logger }
func (r *captureReporter) Checkpoint(_ any) error                           { return nil }
func (r *captureReporter) IsCanceled() bool                                 { return false }
func (r *captureReporter) RunPhase(_ context.Context, _ string, fn func(context.Context, sdk.Reporter) error) error {
	return fn(context.Background(), r)
}
func (r *captureReporter) Trigger(_ context.Context, _ string, _ any) error { return nil }
func (r *captureReporter) SetCurrentItem(_ string)                          {}

// TestCalibrationReport_RowsInGePairsOut runs the calibration op against a real
// EmbeddingStore holding two true_dup rows for the SAME book-pair plus one row
// for a distinct pair, and asserts the structured report line carries rows_in
// and pairs_out with rows_in >= pairs_out (the INIT-1 T3 pair-collapse is
// observable). Embeddings are intentionally absent, so every pair is skipped as
// missing — the report counts still emit.
func TestCalibrationReport_RowsInGePairsOut(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	es := database.NewEmbeddingStore(pebble.DB())

	rows := []database.LabeledExample{
		{CandidateID: 1, EntityAID: "book-a", EntityBID: "book-b", Label: "true_dup", LabelSource: "rule", DecidedAt: "2026-07-01T00:00:00Z"},
		{CandidateID: 2, EntityAID: "book-a", EntityBID: "book-b", Label: "true_dup", LabelSource: "human", DecidedAt: "2026-07-05T00:00:00Z"},
		{CandidateID: 3, EntityAID: "book-c", EntityBID: "book-d", Label: "true_dup", LabelSource: "rule", DecidedAt: "2026-07-01T00:00:00Z"},
	}
	for _, ex := range rows {
		if err := es.UpsertLabeledExample(ex); err != nil {
			t.Fatalf("upsert labeled example %d: %v", ex.CandidateID, err)
		}
	}

	p := &Plugin{store: pebble, embeddingStore: es}
	rep := newCaptureReporter()
	if err := p.runCalibrateEmbeddingThresholds(context.Background(), json.RawMessage(`{"model":"bge-m3"}`), rep); err != nil {
		t.Fatalf("runCalibrateEmbeddingThresholds: %v", err)
	}

	var rowsIn, pairsOut float64
	found := false
	for _, line := range strings.Split(strings.TrimSpace(rep.buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec["msg"] != "calibrate-embedding-thresholds report" {
			continue
		}
		ri, riOK := rec["rows_in"].(float64)
		po, poOK := rec["pairs_out"].(float64)
		if !riOK || !poOK {
			t.Fatalf("report line missing rows_in/pairs_out: %s", line)
		}
		rowsIn, pairsOut, found = ri, po, true
	}
	if !found {
		t.Fatalf("no calibration report line found in output:\n%s", rep.buf.String())
	}
	if rowsIn < pairsOut {
		t.Fatalf("expected rows_in >= pairs_out, got rows_in=%v pairs_out=%v", rowsIn, pairsOut)
	}
	// The two same-pair rows must collapse: 3 rows in, 2 pairs out.
	if rowsIn != 3 || pairsOut != 2 {
		t.Fatalf("expected rows_in=3 pairs_out=2, got rows_in=%v pairs_out=%v", rowsIn, pairsOut)
	}
}

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

// TestSweepThreshold_BestPrecisionReportedOnMiss verifies that when no
// cut-point reaches the target, the recommendation still reports the best
// precision actually achieved at a cut-point meeting the minimum sample-size
// floor (minSampleSizeForBestPrecision), rather than just Met=false.
func TestSweepThreshold_BestPrecisionReportedOnMiss(t *testing.T) {
	// Cut-point 0.90: 3 true_dup + 2 not_dup at/above → precision 0.6, n=5
	// (meets the floor). Cut-point 0.95: 1 true_dup only → precision 1.0,
	// n=1 (below the floor of 5, must NOT be reported as "best").
	pairs := []calibrationPair{
		{label: "true_dup", cosine: 0.96}, // counted at both 0.90 and 0.95 cut-points
		{label: "true_dup", cosine: 0.92},
		{label: "true_dup", cosine: 0.91},
		{label: "not_dup", cosine: 0.93},
		{label: "not_dup", cosine: 0.90},
	}

	// Target > 1.0 is unreachable (precision is bounded by 1.0), guaranteeing
	// Met=false regardless of any single-pair 100%-precision cut-point — this
	// isolates the best-achieved diagnostic from the target-met path.
	rec := sweepThreshold(pairs, 1.01)
	if rec.Met {
		t.Fatalf("expected Met=false, got Met=true (threshold=%.4f)", rec.Threshold)
	}
	if rec.BestPrecisionSampleSize == 0 {
		t.Fatalf("expected a best-precision sample to be reported, got sample size 0")
	}
	// The lone cut-point with a sample >= 5 is 0.90: tp=3, fp=2 → precision 0.6.
	// Any cut-point above 0.90 has fewer than 5 pairs at/above it, so 0.6 must
	// be the reported best, not the spurious 1.0 from the single-pair cut.
	if rec.BestPrecisionAchieved != 0.6 {
		t.Fatalf("expected best precision 0.6 (n=5 floor), got %.4f (n=%d, thr=%.2f)",
			rec.BestPrecisionAchieved, rec.BestPrecisionSampleSize, rec.BestPrecisionThreshold)
	}
	if rec.BestPrecisionSampleSize != 5 {
		t.Fatalf("expected best-precision sample size 5, got %d", rec.BestPrecisionSampleSize)
	}
}

// TestSweepThreshold_BestPrecisionZeroWhenNoCutPointMeetsFloor verifies that
// when every cut-point has fewer samples than minSampleSizeForBestPrecision,
// BestPrecisionSampleSize stays 0 rather than reporting a spurious figure.
func TestSweepThreshold_BestPrecisionZeroWhenNoCutPointMeetsFloor(t *testing.T) {
	pairs := []calibrationPair{
		{label: "true_dup", cosine: 0.96},
		{label: "not_dup", cosine: 0.81},
	}

	rec := sweepThreshold(pairs, 1.01)
	if rec.Met {
		t.Fatalf("expected Met=false, got Met=true")
	}
	if rec.BestPrecisionSampleSize != 0 {
		t.Fatalf("expected BestPrecisionSampleSize=0 (no cut-point meets the floor of %d), got %d",
			minSampleSizeForBestPrecision, rec.BestPrecisionSampleSize)
	}
}

// TestNotDupHighCosineSample_SortedDescendingAndBounded verifies the
// diagnostic sample of highest-cosine not_dup pairs is sorted descending by
// cosine and bounded to the requested limit, ignoring true_dup pairs.
func TestNotDupHighCosineSample_SortedDescendingAndBounded(t *testing.T) {
	pairs := []calibrationPair{
		{label: "not_dup", cosine: 0.50, entityAID: "a1", entityBID: "b1"},
		{label: "true_dup", cosine: 0.99, entityAID: "a2", entityBID: "b2"}, // must be excluded
		{label: "not_dup", cosine: 0.92, entityAID: "a3", entityBID: "b3"},
		{label: "not_dup", cosine: 0.88, entityAID: "a4", entityBID: "b4"},
		{label: "not_dup", cosine: 0.95, entityAID: "a5", entityBID: "b5"},
	}

	sample := notDupHighCosineSample(pairs, 3)
	if len(sample) != 3 {
		t.Fatalf("expected sample bounded to 3, got %d", len(sample))
	}
	wantOrder := []float64{0.95, 0.92, 0.88}
	for i, want := range wantOrder {
		if sample[i].cosine != want {
			t.Fatalf("sample[%d].cosine = %.4f, want %.4f (descending order)", i, sample[i].cosine, want)
		}
		if sample[i].label != "not_dup" {
			t.Fatalf("sample[%d].label = %q, want not_dup", i, sample[i].label)
		}
	}

	// Requesting more than available returns all not_dup pairs, still sorted.
	full := notDupHighCosineSample(pairs, 100)
	if len(full) != 4 {
		t.Fatalf("expected all 4 not_dup pairs when limit exceeds count, got %d", len(full))
	}
	if full[0].cosine != 0.95 || full[3].cosine != 0.50 {
		t.Fatalf("expected full sample sorted descending 0.95..0.50, got %v", full)
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

	pairs, skippedMissing, skippedMismatch, _, _ := collectCalibrationPairs(examples, getter, model)

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
