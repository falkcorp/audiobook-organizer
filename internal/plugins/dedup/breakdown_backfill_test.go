// file: internal/plugins/dedup/breakdown_backfill_test.go
// version: 1.1.0
// guid: db53792e-6046-4acd-ba6c-1857084924cc
// last-edited: 2026-08-20

// Tests for dedup.breakdown-backfill. A fake pairScorer (shared with the
// rescore-labeled-examples tests) stands in for the real Engine so the op's
// decision/skip logic — has-breakdown skip, dry-run no-write, apply persist,
// zero-signal skip, update-error counting, missing-embedding-cosine
// accounting — is exercised deterministically against a real EmbeddingStore.
package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	"github.com/falkcorp/audiobook-organizer/internal/models"
)

// seedCandidate upserts a pending dedup candidate and returns its store ID.
func seedCandidate(t *testing.T, es *database.EmbeddingStore, c database.DedupCandidate) int64 {
	t.Helper()
	if c.EntityType == "" {
		c.EntityType = "book"
	}
	if c.Status == "" {
		c.Status = "pending"
	}
	id, _, err := es.UpsertCandidateNew(c)
	if err != nil {
		t.Fatalf("seed candidate %s/%s: %v", c.EntityAID, c.EntityBID, err)
	}
	return id
}

// failingUpdateScoreStore wraps a real *database.EmbeddingStore but forces
// UpdateCandidateScore to fail for a chosen set of candidate IDs, so the test
// can assert the op counts (rather than swallows) persist failures.
type failingUpdateScoreStore struct {
	*database.EmbeddingStore
	failIDs map[int64]bool
}

func (f *failingUpdateScoreStore) UpdateCandidateScore(id int64, score *models.UnifiedDedupScore, band, formulaVersion string) error {
	if f.failIDs[id] {
		return fmt.Errorf("simulated update failure")
	}
	return f.EmbeddingStore.UpdateCandidateScore(id, score, band, formulaVersion)
}

// TestBreakdownBackfill_SkipsHasBreakdown proves a candidate that already has a
// non-empty ScoreBreakdown is never rescored or rewritten — it is counted as
// skipped_has_breakdown and its stored breakdown is byte-identical afterwards.
func TestBreakdownBackfill_SkipsHasBreakdown(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	es := database.NewEmbeddingStore(pebble.DB())

	existing := &unified.UnifiedDedupScore{
		Pair:    [2]string{"hasA", "hasB"},
		Score:   88.0,
		Band:    "HIGH",
		Formula: "noisy_or_v1",
		Signals: []models.Signal{{Kind: unified.SigExactFile, Confidence: 0.99}},
	}
	id := seedCandidate(t, es, database.DedupCandidate{
		EntityAID: "hasA", EntityBID: "hasB", Layer: "exact",
		ScoreBreakdown: existing, Band: "HIGH", FormulaVersion: "noisy_or_v1",
	})

	// The fake scorer would produce a DIFFERENT score — if the op rescored the
	// row, the stored breakdown would change.
	rep := &capturingReporter{}
	if err := runBreakdownBackfillWith(context.Background(), belowBandScorer(), es,
		json.RawMessage(`{"apply":true}`), rep); err != nil {
		t.Fatalf("run: %v", err)
	}

	got, err := es.GetCandidateByID(id)
	if err != nil {
		t.Fatalf("GetCandidateByID: %v", err)
	}
	if got == nil || got.ScoreBreakdown == nil {
		t.Fatal("candidate with existing breakdown vanished or lost its breakdown")
	}
	if got.ScoreBreakdown.Score != 88.0 || got.Band != "HIGH" {
		t.Fatalf("existing breakdown was rewritten: score=%.1f band=%q", got.ScoreBreakdown.Score, got.Band)
	}
	if !strings.Contains(rep.lastMsg, "skipped_has_breakdown=1") {
		t.Fatalf("summary should report skipped_has_breakdown=1, got %q", rep.lastMsg)
	}
}

// TestBreakdownBackfill_DryRunWritesNothing proves the default (apply=false)
// computes and counts but persists nothing.
func TestBreakdownBackfill_DryRunWritesNothing(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	es := database.NewEmbeddingStore(pebble.DB())

	id := seedCandidate(t, es, database.DedupCandidate{
		EntityAID: "dryA", EntityBID: "dryB", Layer: "exact",
	})

	rep := &capturingReporter{}
	if err := runBreakdownBackfillWith(context.Background(), belowBandScorer(), es,
		json.RawMessage(`{}`), rep); err != nil {
		t.Fatalf("run: %v", err)
	}

	got, err := es.GetCandidateByID(id)
	if err != nil {
		t.Fatalf("GetCandidateByID: %v", err)
	}
	if got == nil {
		t.Fatal("dry-run must not delete the candidate")
	}
	if got.ScoreBreakdown != nil {
		t.Fatalf("dry-run wrote a ScoreBreakdown: %+v", got.ScoreBreakdown)
	}
	if !strings.Contains(rep.lastMsg, "would_backfill=1") {
		t.Fatalf("summary should report would_backfill=1, got %q", rep.lastMsg)
	}
}

// TestBreakdownBackfill_ApplyWritesBreakdown proves apply=true persists the
// recomputed breakdown (even a below-band one — ANY breakdown beats none for
// triage/calibration) while leaving Status/Layer/Similarity untouched.
func TestBreakdownBackfill_ApplyWritesBreakdown(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	es := database.NewEmbeddingStore(pebble.DB())

	sim := 0.42
	id := seedCandidate(t, es, database.DedupCandidate{
		EntityAID: "appA", EntityBID: "appB", Layer: "exact", Similarity: &sim,
	})

	if err := runBreakdownBackfillWith(context.Background(), belowBandScorer(), es,
		json.RawMessage(`{"apply":true}`), &fakeReporter{}); err != nil {
		t.Fatalf("run: %v", err)
	}

	got, err := es.GetCandidateByID(id)
	if err != nil {
		t.Fatalf("GetCandidateByID: %v", err)
	}
	if got == nil {
		t.Fatal("candidate vanished")
	}
	if got.ScoreBreakdown == nil || len(got.ScoreBreakdown.Signals) < 1 {
		t.Fatalf("apply=true did not persist a breakdown with >=1 signal: %+v", got.ScoreBreakdown)
	}
	if got.ScoreBreakdown.Score != 4.0 {
		t.Fatalf("persisted score: want 4.0, got %.4f", got.ScoreBreakdown.Score)
	}
	if got.FormulaVersion != "test" {
		t.Fatalf("FormulaVersion not persisted: got %q", got.FormulaVersion)
	}
	// Below-band composed score → empty band mirrored onto the candidate.
	if got.Band != "" {
		t.Fatalf("Band should mirror the composed (empty) band, got %q", got.Band)
	}
	// Lifecycle fields must be untouched.
	if got.Status != "pending" {
		t.Fatalf("Status changed: got %q", got.Status)
	}
	if got.Layer != "exact" {
		t.Fatalf("Layer changed: got %q", got.Layer)
	}
	if got.Similarity == nil || *got.Similarity != 0.42 {
		t.Fatalf("Similarity changed: got %v", got.Similarity)
	}
}

// TestBreakdownBackfill_ZeroSignalNotPersisted proves a pair the scorer cannot
// produce any signal for is counted (zero_signal) and never written — an empty
// breakdown would be indistinguishable from "scored, no evidence".
func TestBreakdownBackfill_ZeroSignalNotPersisted(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	es := database.NewEmbeddingStore(pebble.DB())

	id := seedCandidate(t, es, database.DedupCandidate{
		EntityAID: "zeroA", EntityBID: "zeroB", Layer: "exact",
	})

	rep := &capturingReporter{}
	zeroScorer := &fakePairScorer{} // no signals → unscorable
	if err := runBreakdownBackfillWith(context.Background(), zeroScorer, es,
		json.RawMessage(`{"apply":true}`), rep); err != nil {
		t.Fatalf("run: %v", err)
	}

	got, err := es.GetCandidateByID(id)
	if err != nil {
		t.Fatalf("GetCandidateByID: %v", err)
	}
	if got == nil || got.ScoreBreakdown != nil {
		t.Fatalf("zero-signal pair must not get a breakdown, got %+v", got)
	}
	if !strings.Contains(rep.lastMsg, "zero_signal=1") {
		t.Fatalf("summary should report zero_signal=1, got %q", rep.lastMsg)
	}
}

// TestBreakdownBackfill_UpdateFailure_CountsError proves a persist failure is
// counted in the summary (update_errs) instead of being silently swallowed.
func TestBreakdownBackfill_UpdateFailure_CountsError(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	es := database.NewEmbeddingStore(pebble.DB())

	id := seedCandidate(t, es, database.DedupCandidate{
		EntityAID: "failA", EntityBID: "failB", Layer: "exact",
	})

	wrapped := &failingUpdateScoreStore{EmbeddingStore: es, failIDs: map[int64]bool{id: true}}
	rep := &capturingReporter{}
	if err := runBreakdownBackfillWith(context.Background(), belowBandScorer(), wrapped,
		json.RawMessage(`{"apply":true}`), rep); err != nil {
		t.Fatalf("run: %v", err)
	}

	got, err := es.GetCandidateByID(id)
	if err != nil {
		t.Fatalf("GetCandidateByID: %v", err)
	}
	if got == nil || got.ScoreBreakdown != nil {
		t.Fatalf("failed update must leave the candidate breakdown-less, got %+v", got)
	}
	if !strings.Contains(rep.lastMsg, "update_errs=1") {
		t.Fatalf("summary should report update_errs=1, got %q", rep.lastMsg)
	}
}

// TestBreakdownBackfill_MissingEmbeddingCosineCounted proves an embedding-layer
// candidate with no stored cosine is still scored with the remaining signals and
// the unavailable embedding signal is recorded in missing_signal_counts.
func TestBreakdownBackfill_MissingEmbeddingCosineCounted(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	es := database.NewEmbeddingStore(pebble.DB())

	id := seedCandidate(t, es, database.DedupCandidate{
		EntityAID: "embA", EntityBID: "embB", Layer: "embedding", // Similarity nil → cosine unrecoverable
	})

	rep := &capturingReporter{}
	if err := runBreakdownBackfillWith(context.Background(), belowBandScorer(), es,
		json.RawMessage(`{"apply":true}`), rep); err != nil {
		t.Fatalf("run: %v", err)
	}

	// Still backfilled from the remaining (duration) signal.
	got, err := es.GetCandidateByID(id)
	if err != nil {
		t.Fatalf("GetCandidateByID: %v", err)
	}
	if got == nil || got.ScoreBreakdown == nil {
		t.Fatal("pair with missing cosine should still be backfilled from remaining signals")
	}
	if !strings.Contains(rep.lastMsg, "missing[embedding_cosine]=1") {
		t.Fatalf("summary should report missing[embedding_cosine]=1, got %q", rep.lastMsg)
	}
}

// TestBreakdownBackfill_ParallelManyGroups spreads many nil-breakdown candidates
// across many A-groups so the registry.RunItems pool is genuinely contended; run
// under -race to catch unguarded shared state. Every candidate must end up with
// a persisted breakdown.
func TestBreakdownBackfill_ParallelManyGroups(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	es := database.NewEmbeddingStore(pebble.DB())

	const n = 200
	ids := make([]int64, n)
	for i := 0; i < n; i++ {
		ids[i] = seedCandidate(t, es, database.DedupCandidate{
			EntityAID: fmt.Sprintf("pA%03d", i), // distinct A per pair → many groups
			EntityBID: fmt.Sprintf("pB%03d", i),
			Layer:     "exact",
		})
	}

	if err := runBreakdownBackfillWith(context.Background(), belowBandScorer(), es,
		json.RawMessage(`{"apply":true}`), &fakeReporter{}); err != nil {
		t.Fatalf("run: %v", err)
	}

	for i, id := range ids {
		got, err := es.GetCandidateByID(id)
		if err != nil {
			t.Fatalf("GetCandidateByID(%d): %v", id, err)
		}
		if got == nil || got.ScoreBreakdown == nil {
			t.Fatalf("candidate %d (pair %d): breakdown not persisted", id, i)
		}
	}
}

// --- Band histogram -------------------------------------------------------

// perPairScorer returns a different canned result per B-side ID, so one run can
// produce several bands and several signal-set shapes. fakePairScorer returns
// ONE canned score for every pair, which cannot distinguish a working histogram
// from one that files every row into a single bucket.
type perPairScorer struct {
	byOther map[string]*unified.UnifiedDedupScore // nil value ⇒ zero-signal pair
}

func (p *perPairScorer) ScorePairsForBook(_ context.Context, aID string, inputs []dedupengine.RescorePairInput) ([]dedupengine.RescorePairResult, error) {
	out := make([]dedupengine.RescorePairResult, 0, len(inputs))
	for _, in := range inputs {
		sc, ok := p.byOther[in.OtherID]
		if !ok || sc == nil {
			out = append(out, dedupengine.RescorePairResult{OtherID: in.OtherID})
			continue
		}
		cp := *sc
		cp.Pair = [2]string{aID, in.OtherID}
		out = append(out, dedupengine.RescorePairResult{
			OtherID: in.OtherID, Score: &cp, NumSignals: len(cp.Signals),
		})
	}
	return out, nil
}

// reportCapturingReporter records the JSON report payload the op logs, so a
// test can assert on the whole struct instead of on the summary substring.
type reportCapturingReporter struct {
	capturingReporter
	report string
}

func (r *reportCapturingReporter) Log(_ slog.Level, msg string, attrs ...slog.Attr) error {
	if msg != "Breakdown-backfill report (JSON)" {
		return nil
	}
	for _, a := range attrs {
		if a.Key == "report" {
			r.report = a.Value.String()
		}
	}
	return nil
}

// TestBreakdownBackfill_BandHistogram proves the dry-run report carries a band
// histogram and the CERTAIN-band risk split, and that the split is keyed on the
// AUTO-RESOLVE primary-kind allow-list rather than on "any non-supporting
// signal": the certain3 pair carries two primary-by-noisy-OR kinds
// (metadata_fuzzy + embedding_high) that dedup.auto-resolve does NOT accept as
// corroboration, so it must land in the "0" bucket.
func TestBreakdownBackfill_BandHistogram(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	es := database.NewEmbeddingStore(pebble.DB())

	mk := func(score float64, band string, sigs ...models.Signal) *unified.UnifiedDedupScore {
		return &unified.UnifiedDedupScore{Score: score, Band: band, Signals: sigs, Formula: "test"}
	}
	scorer := &perPairScorer{byOther: map[string]*unified.UnifiedDedupScore{
		// CERTAIN, one signal, one primary kind → "1"
		"hB1": mk(100, unified.BandCertain, models.Signal{Kind: unified.SigExactFile, Confidence: 1.0}),
		// CERTAIN, two primary kinds → "2+" (the only auto-mergeable shape)
		"hB2": mk(99, unified.BandCertain,
			models.Signal{Kind: unified.SigExactFile, Confidence: 1.0},
			models.Signal{Kind: unified.SigISBNASIN, Confidence: 0.98}),
		// CERTAIN with NO auto-resolve primary kind → "0"
		"hB3": mk(98, unified.BandCertain,
			models.Signal{Kind: unified.SigMetaFuzzy, Confidence: 0.85},
			models.Signal{Kind: unified.SigEmbedHigh, Confidence: 0.90},
			models.Signal{Kind: unified.SigDuration, Confidence: 0}),
		"hB4": mk(92, unified.BandHigh, models.Signal{Kind: unified.SigMetaFuzzy, Confidence: 0.85}),
		"hB5": mk(80, unified.BandMedium, models.Signal{Kind: unified.SigMetaFuzzy, Confidence: 0.80}),
		"hB6": mk(65, unified.BandReview, models.Signal{Kind: unified.SigEmbedMedium, Confidence: 0.65}),
		"hB7": mk(4, "", models.Signal{Kind: unified.SigDuration, Confidence: 0}), // below the REVIEW floor
		"hB8": nil,                                                                // zero-signal: scored nothing, so it has no band
	}}

	for i := 1; i <= 8; i++ {
		seedCandidate(t, es, database.DedupCandidate{
			EntityAID: fmt.Sprintf("hA%d", i), EntityBID: fmt.Sprintf("hB%d", i), Layer: "exact",
		})
	}

	rep := &reportCapturingReporter{}
	if err := runBreakdownBackfillWith(context.Background(), scorer, es, json.RawMessage(`{}`), rep); err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.report == "" {
		t.Fatal("op did not log the JSON report")
	}

	var got breakdownBackfillReport
	if err := json.Unmarshal([]byte(rep.report), &got); err != nil {
		t.Fatalf("unmarshal report %q: %v", rep.report, err)
	}

	wantBands := map[string]int{
		unified.BandCertain: 3, unified.BandHigh: 1, unified.BandMedium: 1,
		unified.BandReview: 1, breakdownBackfillBelowBandKey: 1,
	}
	for band, want := range wantBands {
		if got.BandCounts[band] != want {
			t.Errorf("band_counts[%s] = %d, want %d (full: %v)", band, got.BandCounts[band], want, got.BandCounts)
		}
	}
	if len(got.BandCounts) != len(wantBands) {
		t.Errorf("band_counts has extra buckets: %v", got.BandCounts)
	}

	// Reconciliation: every scored pair is banded exactly once, and the
	// zero-signal pair is counted separately and banded nowhere.
	total := 0
	for _, v := range got.BandCounts {
		total += v
	}
	if total != got.WouldBackfill {
		t.Errorf("band_counts sum = %d, want would_backfill = %d", total, got.WouldBackfill)
	}
	if got.ZeroSignal != 1 {
		t.Errorf("zero_signal = %d, want 1", got.ZeroSignal)
	}

	if got.CertainSignalsEq1 != 1 {
		t.Errorf("certain_signals_eq_1 = %d, want 1", got.CertainSignalsEq1)
	}
	wantPrimary := map[string]int{"0": 1, "1": 1, "2+": 1}
	for bucket, want := range wantPrimary {
		if got.CertainPrimaryKindCounts[bucket] != want {
			t.Errorf("certain_primary_kind_counts[%s] = %d, want %d (full: %v)",
				bucket, got.CertainPrimaryKindCounts[bucket], want, got.CertainPrimaryKindCounts)
		}
	}

	wantSets := map[string]int{
		"exact_file":                             1,
		"exact_file+isbn_asin":                   1,
		"duration+embedding_high+metadata_fuzzy": 1,
	}
	for set, want := range wantSets {
		if got.CertainSignalSets[set] != want {
			t.Errorf("certain_signal_sets[%s] = %d, want %d (full: %v)",
				set, got.CertainSignalSets[set], want, got.CertainSignalSets)
		}
	}
	if len(got.CertainSignalSets) != len(wantSets) {
		t.Errorf("certain_signal_sets has extra keys: %v", got.CertainSignalSets)
	}

	// The human-readable summary must carry the same numbers, including the
	// zero buckets, so an operator reading the progress line is not left
	// guessing whether a missing band was measured or dropped.
	for _, want := range []string{"CERTAIN=3", "HIGH=1", "MEDIUM=1", "REVIEW=1", "BELOW=1",
		"certain_1sig=1", "certain_primary[0]=1", "certain_primary[2+]=1"} {
		if !strings.Contains(rep.lastMsg, want) {
			t.Errorf("summary missing %q; got %q", want, rep.lastMsg)
		}
	}
}

// TestBreakdownBackfill_HistogramCountedOnApply proves the histogram is filled
// on an apply=true run too — counting it inside the dry-run arm would make a
// real apply report an all-zero histogram that reads as a regression.
func TestBreakdownBackfill_HistogramCountedOnApply(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	es := database.NewEmbeddingStore(pebble.DB())

	scorer := &perPairScorer{byOther: map[string]*unified.UnifiedDedupScore{
		"apB1": {Score: 100, Band: unified.BandCertain, Formula: "test",
			Signals: []models.Signal{{Kind: unified.SigExactFile, Confidence: 1.0}}},
	}}
	seedCandidate(t, es, database.DedupCandidate{EntityAID: "apA1", EntityBID: "apB1", Layer: "exact"})

	rep := &reportCapturingReporter{}
	if err := runBreakdownBackfillWith(context.Background(), scorer, es, json.RawMessage(`{"apply":true}`), rep); err != nil {
		t.Fatalf("run: %v", err)
	}
	var got breakdownBackfillReport
	if err := json.Unmarshal([]byte(rep.report), &got); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	if !got.Apply || got.Backfilled != 1 {
		t.Fatalf("expected an applied run with backfilled=1, got apply=%v backfilled=%d", got.Apply, got.Backfilled)
	}
	if got.BandCounts[unified.BandCertain] != 1 {
		t.Errorf("apply run band_counts[CERTAIN] = %d, want 1 (full: %v)", got.BandCounts[unified.BandCertain], got.BandCounts)
	}
	if got.CertainSignalSets["exact_file"] != 1 {
		t.Errorf("apply run certain_signal_sets = %v, want exact_file:1", got.CertainSignalSets)
	}
}
