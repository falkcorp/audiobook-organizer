// file: internal/plugins/dedup/breakdown_backfill_test.go
// version: 1.0.0
// guid: db53792e-6046-4acd-ba6c-1857084924cc
// last-edited: 2026-07-17

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
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
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
