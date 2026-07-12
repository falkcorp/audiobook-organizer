// file: internal/plugins/dedup/calibrate_composite_test.go
// version: 1.0.0
// guid: 7e5a1c3b-9d2f-4a08-8b61-3c4d5e6f7a89
// last-edited: 2026-07-11

package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	"github.com/falkcorp/audiobook-organizer/internal/models"
)

// breakdownWith builds a ScoreBreakdown JSON snapshot carrying a single primary
// signal of the given kind + confidence, so a test can dial a pair's composite
// score deterministically (single-signal noisy-OR → score = 100*confidence,
// after cfg clamping).
func breakdownWith(kind unified.SignalKind, conf float64) json.RawMessage {
	uds := models.UnifiedDedupScore{
		Signals: []models.Signal{{Kind: kind, Confidence: conf, Raw: conf}},
	}
	b, _ := json.Marshal(uds)
	return b
}

// upsertPairs writes n labeled examples of the given label, each a DISTINCT
// canonical pair (so DedupeByPair never collapses them), each carrying the given
// single-signal breakdown. idBase offsets candidate/entity IDs across calls.
func upsertPairs(t *testing.T, es *database.EmbeddingStore, idBase, n int, label string, bd json.RawMessage) {
	t.Helper()
	for i := 0; i < n; i++ {
		id := idBase + i
		ex := database.LabeledExample{
			CandidateID:    int64(id),
			EntityAID:      fmt.Sprintf("a%06d", id),
			EntityBID:      fmt.Sprintf("b%06d", id),
			Label:          label,
			LabelSource:    "rule",
			DecidedAt:      "2026-07-01T00:00:00Z",
			ScoreBreakdown: bd,
		}
		if err := es.UpsertLabeledExample(ex); err != nil {
			t.Fatalf("upsert %d: %v", id, err)
		}
	}
}

// reportFields parses the single "calibrate-composite report" JSON log line the
// op emits and returns its fields for assertion.
func reportFields(t *testing.T, buf string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(buf), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec["msg"] == "calibrate-composite report" {
			return rec
		}
	}
	t.Fatalf("no 'calibrate-composite report' line found in:\n%s", buf)
	return nil
}

// TestCalibrateCompositeInsufficientCoverage: a below-floor input reports
// insufficient-coverage and recommends nothing; nil-breakdown rows are skipped
// and counted (never scored as zero).
func TestCalibrateCompositeInsufficientCoverage(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	es := database.NewEmbeddingStore(pebble.DB())

	upsertPairs(t, es, 1000, 3, "true_dup", breakdownWith(unified.SigEmbedHigh, 0.94))
	upsertPairs(t, es, 2000, 3, "not_dup", breakdownWith(unified.SigMetaFuzzy, 0.80))
	// Two nil-breakdown rows that must be skipped + counted, not scored as zero.
	upsertPairs(t, es, 3000, 2, "true_dup", nil)

	p := &Plugin{store: pebble, embeddingStore: es}
	rep := newCaptureReporter()
	if err := p.runCalibrateComposite(context.Background(), nil, rep); err != nil {
		t.Fatalf("run: %v", err)
	}
	f := reportFields(t, rep.buf.String())

	if f["status"] != "insufficient-coverage" {
		t.Fatalf("status = %v, want insufficient-coverage", f["status"])
	}
	if got := f["scored_true_dup"].(float64); got != 3 {
		t.Errorf("scored_true_dup = %v, want 3", got)
	}
	if got := f["skipped_no_breakdown"].(float64); got != 2 {
		t.Errorf("skipped_no_breakdown = %v, want 2 (nil breakdowns skipped, not scored)", got)
	}
	if _, ok := f["recommended_high_min"]; ok {
		t.Error("insufficient-coverage must recommend nothing, got recommended_high_min")
	}
}

// TestCalibrateCompositeDryRunWritesNothing: default params, a well-separated
// set → recommendations appear in the report, and the config store is untouched.
func TestCalibrateCompositeDryRunWritesNothing(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	es := database.NewEmbeddingStore(pebble.DB())

	// 600 per class clears the default 500 floor.
	upsertPairs(t, es, 100000, 600, "true_dup", breakdownWith(unified.SigEmbedHigh, 0.94))
	upsertPairs(t, es, 200000, 600, "not_dup", breakdownWith(unified.SigEmbedHigh, 0.915))

	before := config.Snapshot().Dedup.Signals

	p := &Plugin{store: pebble, embeddingStore: es}
	rep := newCaptureReporter()
	if err := p.runCalibrateComposite(context.Background(), nil, rep); err != nil {
		t.Fatalf("run: %v", err)
	}
	f := reportFields(t, rep.buf.String())

	if f["status"] != "ok" {
		t.Fatalf("status = %v, want ok", f["status"])
	}
	if _, ok := f["recommended_high_min"]; !ok {
		t.Error("expected a recommended_high_min in the dry-run report")
	}
	after := config.Snapshot().Dedup.Signals
	if before != after {
		t.Errorf("dry-run mutated config store: before=%+v after=%+v", before, after)
	}
}

// TestCalibrateCompositeFindsSeparation: a set where the HIGH band must shift
// from 90→92 to reach target precision → the recommendation contains 92.
func TestCalibrateCompositeFindsSeparation(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	es := database.NewEmbeddingStore(pebble.DB())

	// true_dup score 94, not_dup score 91.5 (both inside SigEmbedHigh's [0.88,0.95]
	// clamp so no clamping distorts). At min=90 precision is 0.5; at 92 the
	// not_dup pairs drop out → precision 1.0.
	upsertPairs(t, es, 100000, 30, "true_dup", breakdownWith(unified.SigEmbedHigh, 0.94))
	upsertPairs(t, es, 200000, 30, "not_dup", breakdownWith(unified.SigEmbedHigh, 0.915))

	p := &Plugin{store: pebble, embeddingStore: es}
	rep := newCaptureReporter()
	if err := p.runCalibrateComposite(context.Background(), json.RawMessage(`{"min_scored_pairs":10}`), rep); err != nil {
		t.Fatalf("run: %v", err)
	}
	f := reportFields(t, rep.buf.String())

	got, ok := f["recommended_high_min"].(float64)
	if !ok {
		t.Fatalf("expected recommended_high_min, report: %v", f)
	}
	if got != 92.0 {
		t.Errorf("recommended_high_min = %v, want 92.0", got)
	}
	if f["high_target_met"] != true {
		t.Errorf("high_target_met = %v, want true", f["high_target_met"])
	}
}

// TestCalibrateCompositeTargetNotMet: inseparable classes → target-not-met,
// nothing recommended (the recommender must never force a bad threshold).
func TestCalibrateCompositeTargetNotMet(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	es := database.NewEmbeddingStore(pebble.DB())

	// Both classes at the SAME score 92 — no cut-point can separate them.
	upsertPairs(t, es, 100000, 30, "true_dup", breakdownWith(unified.SigEmbedHigh, 0.92))
	upsertPairs(t, es, 200000, 30, "not_dup", breakdownWith(unified.SigEmbedHigh, 0.92))

	p := &Plugin{store: pebble, embeddingStore: es}
	rep := newCaptureReporter()
	if err := p.runCalibrateComposite(context.Background(), json.RawMessage(`{"min_scored_pairs":10}`), rep); err != nil {
		t.Fatalf("run: %v", err)
	}
	f := reportFields(t, rep.buf.String())

	if _, ok := f["recommended_high_min"]; ok {
		t.Error("inseparable classes must recommend nothing for HIGH")
	}
	if _, ok := f["recommended_certain_min"]; ok {
		t.Error("inseparable classes must recommend nothing for CERTAIN")
	}
	if f["high_target_met"] != false || f["certain_target_met"] != false {
		t.Errorf("expected both targets unmet, got high=%v certain=%v", f["high_target_met"], f["certain_target_met"])
	}
	if f["all_targets_met"] != false {
		t.Errorf("all_targets_met = %v, want false", f["all_targets_met"])
	}
}

// TestCalibrateCompositeTargetNotMetApplyWritesNothing: apply=true on an
// inseparable set is refused — the config store stays untouched.
func TestCalibrateCompositeTargetNotMetApplyWritesNothing(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	es := database.NewEmbeddingStore(pebble.DB())
	upsertPairs(t, es, 100000, 30, "true_dup", breakdownWith(unified.SigEmbedHigh, 0.92))
	upsertPairs(t, es, 200000, 30, "not_dup", breakdownWith(unified.SigEmbedHigh, 0.92))

	before := config.Snapshot().Dedup.Signals

	p := &Plugin{store: pebble, embeddingStore: es}
	rep := newCaptureReporter()
	if err := p.runCalibrateComposite(context.Background(), json.RawMessage(`{"min_scored_pairs":10,"apply":true}`), rep); err != nil {
		t.Fatalf("run: %v", err)
	}
	if after := config.Snapshot().Dedup.Signals; before != after {
		t.Errorf("apply on target-not-met mutated config: before=%+v after=%+v", before, after)
	}
}

// TestCalibrateCompositeSweepParallel exercises the errgroup band-sweep pool
// (run the package with -race). A moderate well-separated set is enough to fan
// out multiple candidate variants across workers.
func TestCalibrateCompositeSweepParallel(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	es := database.NewEmbeddingStore(pebble.DB())
	upsertPairs(t, es, 100000, 40, "true_dup", breakdownWith(unified.SigEmbedHigh, 0.94))
	upsertPairs(t, es, 200000, 40, "not_dup", breakdownWith(unified.SigEmbedHigh, 0.915))

	p := &Plugin{store: pebble, embeddingStore: es}
	rep := newCaptureReporter()
	if err := p.runCalibrateComposite(context.Background(), json.RawMessage(`{"min_scored_pairs":10}`), rep); err != nil {
		t.Fatalf("run: %v", err)
	}
	if f := reportFields(t, rep.buf.String()); f["status"] != "ok" {
		t.Fatalf("status = %v, want ok", f["status"])
	}
}

// TestCalibrateCompositeApplyPersistsBands: with apply=true and both tunable
// bands meeting target, the recommended BAND thresholds survive a
// SaveConfigToDatabase → LoadConfigFromDatabase round-trip. (Per-signal
// confidences are advisory-only and intentionally do NOT round-trip — no config
// blob surface exists for them.)
func TestCalibrateCompositeApplyPersistsBands(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	es := database.NewEmbeddingStore(pebble.DB())

	// true_dup score 99 (SigExactAcoustID, clamped to 0.99); not_dup score 80
	// (SigMetaFuzzy, clamped to [0.70,0.85]). Both CERTAIN(97) and HIGH(90) meet
	// target at baseline, so the sweep recommends and apply fires.
	upsertPairs(t, es, 100000, 30, "true_dup", breakdownWith(unified.SigExactAcoustID, 0.99))
	upsertPairs(t, es, 200000, 30, "not_dup", breakdownWith(unified.SigMetaFuzzy, 0.80))

	// Restore global config after the test (UpdateConfig/LoadConfigFromDatabase
	// mutate the process-wide AppConfig).
	orig := config.Snapshot()
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { *c = orig }) })

	p := &Plugin{store: pebble, embeddingStore: es}
	rep := newCaptureReporter()
	if err := p.runCalibrateComposite(context.Background(), json.RawMessage(`{"min_scored_pairs":10,"apply":true}`), rep); err != nil {
		t.Fatalf("run: %v", err)
	}
	f := reportFields(t, rep.buf.String())
	if f["all_targets_met"] != true {
		t.Fatalf("all_targets_met = %v, want true (report: %v)", f["all_targets_met"], f)
	}
	recCertain := f["recommended_certain_min"].(float64)
	recHigh := f["recommended_high_min"].(float64)

	// Reload from the persisted config blob and assert the bands round-tripped.
	if err := config.LoadConfigFromDatabase(pebble); err != nil {
		t.Fatalf("LoadConfigFromDatabase: %v", err)
	}
	sig := config.Snapshot().Dedup.Signals
	if sig.BandCertainMin != recCertain {
		t.Errorf("persisted band_certain_min = %v, want %v", sig.BandCertainMin, recCertain)
	}
	if sig.BandHighMin != recHigh {
		t.Errorf("persisted band_high_min = %v, want %v", sig.BandHighMin, recHigh)
	}
}
