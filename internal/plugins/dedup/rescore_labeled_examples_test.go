// file: internal/plugins/dedup/rescore_labeled_examples_test.go
// version: 1.0.0
// guid: 6d2b8f14-3a97-4e05-9c81-7f0a5d3e2c68
// last-edited: 2026-07-12

// Tests for dedup.rescore-labeled-examples. A fake pairScorer stands in for the
// real Engine so the persistence contract (below-band write, narrow write that
// preserves human labels, dry-run no-op, parallel safety) is exercised
// deterministically against a real EmbeddingStore.
package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	"github.com/falkcorp/audiobook-organizer/internal/models"
)

// fakePairScorer returns a canned UnifiedDedupScore for every injected pair. When
// signals is empty the pair is reported unscorable (nil Score, NumSignals 0),
// matching the real engine's zero-signal contract.
type fakePairScorer struct {
	score   float64
	band    string
	signals []models.Signal
}

func (f *fakePairScorer) ScorePairsForBook(_ context.Context, aID string, inputs []dedupengine.RescorePairInput) ([]dedupengine.RescorePairResult, error) {
	out := make([]dedupengine.RescorePairResult, 0, len(inputs))
	for _, in := range inputs {
		res := dedupengine.RescorePairResult{OtherID: in.OtherID, NumSignals: len(f.signals)}
		if len(f.signals) > 0 {
			res.Score = &unified.UnifiedDedupScore{
				Pair:    [2]string{aID, in.OtherID},
				Score:   f.score,
				Band:    f.band,
				Signals: f.signals,
				Formula: "test",
			}
		}
		out = append(out, res)
	}
	return out, nil
}

// belowBandScorer emits a single below-band (band=="") duration signal — exactly
// the negative the operational scan discards and this op must persist.
func belowBandScorer() *fakePairScorer {
	return &fakePairScorer{
		score: 4.0,
		band:  "", // below the review floor — the scan would drop this pair
		signals: []models.Signal{
			{Kind: unified.SigDuration, Raw: 1.0, Confidence: 0, Evidence: "duration match"},
		},
	}
}

func seedLabeled(t *testing.T, es *database.EmbeddingStore, ex database.LabeledExample) {
	t.Helper()
	if err := es.UpsertLabeledExample(ex); err != nil {
		t.Fatalf("seed labeled example %d: %v", ex.CandidateID, err)
	}
}

// TestRescoreLabeledExamples_BelowBandPersisted proves the band-skip bypass: a
// below-band (band=="") labeled pair, which the operational scan never persists,
// gets a parseable ScoreBreakdown written onto its LabeledExample.
func TestRescoreLabeledExamples_BelowBandPersisted(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	es := database.NewEmbeddingStore(pebble.DB())

	seedLabeled(t, es, database.LabeledExample{
		CandidateID: 101, EntityAID: "bookA", EntityBID: "bookB",
		Label: "not_dup", LabelSource: "rule", // dismissed-style negative, no breakdown
	})

	if err := runRescoreLabeledExamplesWith(context.Background(), belowBandScorer(), es,
		json.RawMessage(`{"apply":true}`), &fakeReporter{}); err != nil {
		t.Fatalf("run: %v", err)
	}

	got, err := es.GetLabeledExample(101)
	if err != nil {
		t.Fatalf("GetLabeledExample: %v", err)
	}
	if got == nil {
		t.Fatal("labeled example vanished")
	}
	if len(got.ScoreBreakdown) == 0 {
		t.Fatal("below-band pair: ScoreBreakdown was not persisted (band-skip bypass failed)")
	}
	// It must be parseable with >=1 signal — the property calibrate-composite needs.
	var uds models.UnifiedDedupScore
	if err := json.Unmarshal(got.ScoreBreakdown, &uds); err != nil {
		t.Fatalf("persisted ScoreBreakdown does not parse: %v", err)
	}
	if len(uds.Signals) < 1 {
		t.Fatalf("persisted ScoreBreakdown has no signals (unscorable): %+v", uds)
	}
	if got.Score != 4.0 {
		t.Fatalf("Score not persisted: want 4.0, got %.4f", got.Score)
	}
	if got.Band != "" {
		t.Fatalf("Band should mirror the composed (empty) band, got %q", got.Band)
	}
}

// TestRescoreLabeledExamples_HumanLabelPreserved proves the narrow write: a
// human-sourced labeled example keeps LabelSource=="human", its Label, and its
// DecidedAt after rescore — only Score/ScoreBreakdown/Band change.
func TestRescoreLabeledExamples_HumanLabelPreserved(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	es := database.NewEmbeddingStore(pebble.DB())

	seedLabeled(t, es, database.LabeledExample{
		CandidateID: 202, EntityAID: "humA", EntityBID: "humB",
		Label: "not_dup", LabelSource: "human",
		LabelReason: "reviewer said different book", DecidedAt: "2026-07-01T00:00:00Z",
	})

	if err := runRescoreLabeledExamplesWith(context.Background(), belowBandScorer(), es,
		json.RawMessage(`{"apply":true}`), &fakeReporter{}); err != nil {
		t.Fatalf("run: %v", err)
	}

	got, err := es.GetLabeledExample(202)
	if err != nil {
		t.Fatalf("GetLabeledExample: %v", err)
	}
	if got == nil {
		t.Fatal("labeled example vanished")
	}
	// Label provenance MUST survive untouched.
	if got.LabelSource != "human" {
		t.Fatalf("human LabelSource clobbered: got %q", got.LabelSource)
	}
	if got.Label != "not_dup" {
		t.Fatalf("human Label clobbered: got %q", got.Label)
	}
	if got.LabelReason != "reviewer said different book" {
		t.Fatalf("LabelReason clobbered: got %q", got.LabelReason)
	}
	if got.DecidedAt != "2026-07-01T00:00:00Z" {
		t.Fatalf("DecidedAt clobbered: got %q", got.DecidedAt)
	}
	// But the ScoreBreakdown must now be populated.
	if len(got.ScoreBreakdown) == 0 {
		t.Fatal("ScoreBreakdown not written onto human-labeled example")
	}
}

// TestRescoreLabeledExamples_DryRunWritesNothing proves apply=false persists nothing.
func TestRescoreLabeledExamples_DryRunWritesNothing(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	es := database.NewEmbeddingStore(pebble.DB())

	seedLabeled(t, es, database.LabeledExample{
		CandidateID: 303, EntityAID: "dryA", EntityBID: "dryB",
		Label: "true_dup", LabelSource: "rule",
	})

	if err := runRescoreLabeledExamplesWith(context.Background(), belowBandScorer(), es,
		json.RawMessage(`{}`), &fakeReporter{}); err != nil {
		t.Fatalf("run: %v", err)
	}

	got, err := es.GetLabeledExample(303)
	if err != nil {
		t.Fatalf("GetLabeledExample: %v", err)
	}
	if got == nil {
		t.Fatal("dry-run must not delete the example")
	}
	if len(got.ScoreBreakdown) != 0 || got.Score != 0 {
		t.Fatalf("dry-run wrote something: breakdown_len=%d score=%.4f", len(got.ScoreBreakdown), got.Score)
	}
}

// TestRescoreLabeledExamples_DuplicatePairRowsBothRescored proves that two labeled
// examples for the SAME canonical (A,B) pair but with DIFFERENT candidateIDs — the
// candidate-recreated-after-dismiss case — BOTH get their breakdown persisted.
// Regression guard for keying results by OtherID (which would collapse them,
// dropping one and double-writing the other).
func TestRescoreLabeledExamples_DuplicatePairRowsBothRescored(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	es := database.NewEmbeddingStore(pebble.DB())

	// Same (A,B), two candidateIDs (old dismissed row + new recreated row).
	seedLabeled(t, es, database.LabeledExample{
		CandidateID: 401, EntityAID: "dupA", EntityBID: "dupB",
		Label: "not_dup", LabelSource: "rule",
	})
	seedLabeled(t, es, database.LabeledExample{
		CandidateID: 402, EntityAID: "dupA", EntityBID: "dupB",
		Label: "not_dup", LabelSource: "human",
	})

	if err := runRescoreLabeledExamplesWith(context.Background(), belowBandScorer(), es,
		json.RawMessage(`{"apply":true}`), &fakeReporter{}); err != nil {
		t.Fatalf("run: %v", err)
	}

	for _, id := range []int64{401, 402} {
		got, err := es.GetLabeledExample(id)
		if err != nil {
			t.Fatalf("GetLabeledExample(%d): %v", id, err)
		}
		if got == nil || len(got.ScoreBreakdown) == 0 {
			t.Fatalf("duplicate-pair row %d: breakdown not persisted (results keyed by OtherID?)", id)
		}
	}
	// The human-sourced duplicate row must still have its label intact.
	human, _ := es.GetLabeledExample(402)
	if human.LabelSource != "human" || human.Label != "not_dup" {
		t.Fatalf("row 402: label provenance clobbered: source=%q label=%q", human.LabelSource, human.Label)
	}
}

// TestRescoreLabeledExamples_ParallelManyGroups spreads many labeled pairs across
// many A-groups so the registry.RunItems pool is genuinely contended; run under
// -race to catch unguarded shared state. Every example must end up with a
// persisted breakdown.
func TestRescoreLabeledExamples_ParallelManyGroups(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	es := database.NewEmbeddingStore(pebble.DB())

	const n = 200
	for i := 0; i < n; i++ {
		class := "true_dup"
		if i%2 == 0 {
			class = "not_dup"
		}
		seedLabeled(t, es, database.LabeledExample{
			CandidateID: int64(1000 + i),
			EntityAID:   fmt.Sprintf("A%03d", i), // distinct A per pair → many groups
			EntityBID:   fmt.Sprintf("B%03d", i),
			Label:       class, LabelSource: "rule",
		})
	}

	if err := runRescoreLabeledExamplesWith(context.Background(), belowBandScorer(), es,
		json.RawMessage(`{"apply":true}`), &fakeReporter{}); err != nil {
		t.Fatalf("run: %v", err)
	}

	for i := 0; i < n; i++ {
		got, err := es.GetLabeledExample(int64(1000 + i))
		if err != nil {
			t.Fatalf("GetLabeledExample(%d): %v", 1000+i, err)
		}
		if got == nil || len(got.ScoreBreakdown) == 0 {
			t.Fatalf("example %d: breakdown not persisted", 1000+i)
		}
	}
}
