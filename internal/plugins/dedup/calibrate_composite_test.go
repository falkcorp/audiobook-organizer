// file: internal/plugins/dedup/calibrate_composite_test.go
// version: 1.3.0
// guid: 7e5a1c3b-9d2f-4a08-8b61-3c4d5e6f7a89
// last-edited: 2026-07-18

package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
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
	if !reflect.DeepEqual(before, after) {
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
	if after := config.Snapshot().Dedup.Signals; !reflect.DeepEqual(before, after) {
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

// TestCalibrateCompositeParamsDefaultNoApply is the canary the scheduler's
// label_refinement chain (internal/scheduler/tasks.go, TestLabelRefinementChainPassesNoApply)
// relies on: empty params must default Apply=false so calibrate-composite reports
// without persisting band thresholds. If this default ever flips, the scheduled
// chain's empty-params call would silently become a timed prod mutation.
func TestCalibrateCompositeParamsDefaultNoApply(t *testing.T) {
	var p calibrateCompositeParams
	if err := json.Unmarshal([]byte(`{}`), &p); err != nil {
		t.Fatalf("unmarshal empty params: %v", err)
	}
	if p.Apply {
		t.Fatal("empty params must default Apply=false (report only); scheduled label_refinement chain depends on this")
	}
}

// upsertCandidateBreakdown writes a candidate record carrying a single-signal
// ScoreBreakdown and returns its assigned ID, so a test can point a
// stale-snapshot labeled example at it and exercise the CandidateID join.
func upsertCandidateBreakdown(t *testing.T, es *database.EmbeddingStore, tag string, kind unified.SignalKind, conf float64) int64 {
	t.Helper()
	id, _, err := es.UpsertCandidateNew(database.DedupCandidate{
		EntityType: "book",
		EntityAID:  "ca-" + tag,
		EntityBID:  "cb-" + tag,
		Status:     "pending",
		ScoreBreakdown: &models.UnifiedDedupScore{
			Signals: []models.Signal{{Kind: kind, Confidence: conf, Raw: conf}},
		},
	})
	if err != nil {
		t.Fatalf("upsert candidate %s: %v", tag, err)
	}
	return id
}

// upsertStaleLabel writes a labeled example with a NIL ScoreBreakdown (a stale
// snapshot) pointing at candidateID — the exact shape the CandidateID join must
// repair.
func upsertStaleLabel(t *testing.T, es *database.EmbeddingStore, candidateID int64, label string) {
	t.Helper()
	ex := database.LabeledExample{
		CandidateID: candidateID,
		EntityAID:   fmt.Sprintf("la%08d", candidateID),
		EntityBID:   fmt.Sprintf("lb%08d", candidateID),
		Label:       label,
		LabelSource: "rule",
		DecidedAt:   "2026-07-01T00:00:00Z",
		// ScoreBreakdown intentionally nil — the stale-snapshot condition.
	}
	if err := es.UpsertLabeledExample(ex); err != nil {
		t.Fatalf("upsert stale label %d: %v", candidateID, err)
	}
}

// TestCalibrateCompositeJoinsCandidateBreakdown proves the read-mismatch repair:
// a labeled example whose own ScoreBreakdown is stale (nil) but whose CANDIDATE
// record carries a fresh breakdown is recovered by the CandidateID join and
// scored — while a stale example whose candidate is absent stays skipped, and
// examples carrying their own breakdown never consult the join (pin preserved).
func TestCalibrateCompositeJoinsCandidateBreakdown(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	es := database.NewEmbeddingStore(pebble.DB())

	// A true_dup carrying its OWN breakdown — the join is never consulted for it.
	upsertPairs(t, es, 500000, 1, "true_dup", breakdownWith(unified.SigEmbedHigh, 0.94))

	// A not_dup with a stale (nil) snapshot whose candidate carries a fresh
	// breakdown — this is the pair the join must recover.
	notDupCandID := upsertCandidateBreakdown(t, es, "notdup", unified.SigMetaFuzzy, 0.80)
	upsertStaleLabel(t, es, notDupCandID, "not_dup")

	// A not_dup stale snapshot whose CandidateID references NO candidate record —
	// nothing to join to, so it must stay skipped (never scored as zero).
	upsertStaleLabel(t, es, 99999999, "not_dup")

	p := &Plugin{store: pebble, embeddingStore: es}
	rep := newCaptureReporter()
	if err := p.runCalibrateComposite(context.Background(), json.RawMessage(`{"min_scored_pairs":1}`), rep); err != nil {
		t.Fatalf("run: %v", err)
	}
	f := reportFields(t, rep.buf.String())

	if f["status"] != "ok" {
		t.Fatalf("status = %v, want ok (join lifts coverage over the floor)", f["status"])
	}
	if got := f["joined_from_candidate"].(float64); got != 1 {
		t.Errorf("joined_from_candidate = %v, want 1", got)
	}
	if got := f["scored_not_dup"].(float64); got != 1 {
		t.Errorf("scored_not_dup = %v, want 1 (recovered via join)", got)
	}
	if got := f["scored_true_dup"].(float64); got != 1 {
		t.Errorf("scored_true_dup = %v, want 1", got)
	}
	if got := f["skipped_no_breakdown"].(float64); got != 1 {
		t.Errorf("skipped_no_breakdown = %v, want 1 (orphan stale label, no candidate)", got)
	}
}
