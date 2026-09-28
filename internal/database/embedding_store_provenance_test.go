// file: internal/database/embedding_store_provenance_test.go
// version: 1.0.0
// guid: 2ce8b1be-8006-4e15-a2eb-6bead0a27d18
// last-edited: 2026-09-27

package database

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/models"
)

func provenanceScore(rule, evidence string) *models.UnifiedDedupScore {
	return &models.UnifiedDedupScore{
		Pair: [2]string{"a", "b"},
		Signals: []models.Signal{{
			Kind: models.SignalKindExactRule, Rule: rule, Evidence: evidence,
		}},
	}
}

func certainScore() *models.UnifiedDedupScore {
	return &models.UnifiedDedupScore{
		Pair:  [2]string{"a", "b"},
		Score: 99.5,
		Band:  "CERTAIN",
		Signals: []models.Signal{{
			Kind: "isbn_asin", Raw: 1, Confidence: 0.98, Evidence: "isbn match",
		}},
		Formula: "unified-v1",
	}
}

func candidateFor(t *testing.T, s *EmbeddingStore) DedupCandidate {
	t.Helper()
	cands, _, err := s.ListCandidates(CandidateFilter{Limit: 10})
	if err != nil || len(cands) != 1 {
		t.Fatalf("list: %v (%d rows)", err, len(cands))
	}
	return cands[0]
}

func rules(sb *models.UnifiedDedupScore) []string {
	var out []string
	if sb == nil {
		return out
	}
	for _, s := range sb.Signals {
		if s.Kind == models.SignalKindExactRule {
			out = append(out, s.Rule)
		}
	}
	return out
}

// An existing exact row is "protected" against every later write, which is
// why no exact row in production ever gained a breakdown. Provenance is merged
// into it anyway, and a second rule firing is appended, not dropped.
func TestUpsert_ProvenanceMergesIntoProtectedExactRow(t *testing.T) {
	s := newTestEmbeddingStore(t)
	sim := 1.0
	// A legacy exact row: no breakdown at all.
	if err := s.UpsertCandidate(DedupCandidate{EntityType: "book", EntityAID: "a", EntityBID: "b", Layer: "exact", Similarity: &sim}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertCandidate(DedupCandidate{EntityType: "book", EntityAID: "a", EntityBID: "b", Layer: "exact", Similarity: &sim,
		ScoreBreakdown: provenanceScore("isbn_asin", "shared ASIN X")}); err != nil {
		t.Fatal(err)
	}
	if got := rules(candidateFor(t, s).ScoreBreakdown); len(got) != 1 || got[0] != "isbn_asin" {
		t.Fatalf("rules after first merge = %v", got)
	}
	// Second rule: appended. Same rule again: replaced, not duplicated.
	for _, sb := range []*models.UnifiedDedupScore{
		provenanceScore("title_author", "same title"),
		provenanceScore("isbn_asin", "shared ASIN X (refreshed)"),
	} {
		if err := s.UpsertCandidate(DedupCandidate{EntityType: "book", EntityAID: "a", EntityBID: "b", Layer: "exact", Similarity: &sim, ScoreBreakdown: sb}); err != nil {
			t.Fatal(err)
		}
	}
	c := candidateFor(t, s)
	got := rules(c.ScoreBreakdown)
	if len(got) != 2 || got[0] != "isbn_asin" || got[1] != "title_author" {
		t.Fatalf("rules = %v, want [isbn_asin title_author]", got)
	}
	if c.ScoreBreakdown.Signals[0].Evidence != "shared ASIN X (refreshed)" {
		t.Fatalf("same-rule re-emit did not refresh evidence: %q", c.ScoreBreakdown.Signals[0].Evidence)
	}
	if c.Band != "" || c.ScoreBreakdown.Score != 0 {
		t.Fatalf("provenance must not create a score/band: band=%q score=%v", c.Band, c.ScoreBreakdown.Score)
	}
}

// A scored row gains the rule; its score, band and scoring signals stay.
func TestUpsert_ProvenanceLeavesScoreUntouched(t *testing.T) {
	s := newTestEmbeddingStore(t)
	sim := 0.99
	if err := s.UpsertCandidate(DedupCandidate{EntityType: "book", EntityAID: "a", EntityBID: "b", Layer: "exact", Similarity: &sim,
		ScoreBreakdown: certainScore(), Band: "CERTAIN", FormulaVersion: "unified-v1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertCandidate(DedupCandidate{EntityType: "book", EntityAID: "a", EntityBID: "b", Layer: "exact", Similarity: &sim,
		ScoreBreakdown: provenanceScore("isbn_asin", "shared ASIN")}); err != nil {
		t.Fatal(err)
	}
	c := candidateFor(t, s)
	if c.Band != "CERTAIN" || c.ScoreBreakdown.Score != 99.5 || c.ScoreBreakdown.Band != "CERTAIN" {
		t.Fatalf("score/band changed: band=%q sb=%+v", c.Band, c.ScoreBreakdown)
	}
	if len(c.ScoreBreakdown.Signals) != 2 || c.ScoreBreakdown.Signals[0].Kind != "isbn_asin" {
		t.Fatalf("signals = %+v", c.ScoreBreakdown.Signals)
	}
}

// A pinned (manual) row's breakdown is frozen against provenance too.
func TestUpsert_ProvenanceDoesNotTouchPinnedRow(t *testing.T) {
	s := newTestEmbeddingStore(t)
	if _, err := s.EnqueueManualCandidate("book", "a", "b", "look at this"); err != nil {
		t.Fatal(err)
	}
	sim := 1.0
	if err := s.UpsertCandidate(DedupCandidate{EntityType: "book", EntityAID: "a", EntityBID: "b", Layer: "exact", Similarity: &sim,
		ScoreBreakdown: provenanceScore("title_author", "x")}); err != nil {
		t.Fatal(err)
	}
	if sb := candidateFor(t, s).ScoreBreakdown; sb != nil {
		t.Fatalf("pinned row gained a breakdown: %+v", sb)
	}
}

// A rescore / breakdown backfill replaces the score but keeps the rule.
func TestUpdateCandidateScores_CarriesProvenance(t *testing.T) {
	s := newTestEmbeddingStore(t)
	sim := 1.0
	if err := s.UpsertCandidate(DedupCandidate{EntityType: "book", EntityAID: "a", EntityBID: "b", Layer: "exact", Similarity: &sim,
		ScoreBreakdown: provenanceScore("file_hash", "identical bytes")}); err != nil {
		t.Fatal(err)
	}
	id := candidateFor(t, s).ID
	if _, _, err := s.UpdateCandidateScores([]CandidateScoreUpdate{{ID: id, Score: certainScore(), Band: "CERTAIN", FormulaVersion: "unified-v1"}}); err != nil {
		t.Fatal(err)
	}
	c := candidateFor(t, s)
	if c.Band != "CERTAIN" || c.ScoreBreakdown.Score != 99.5 {
		t.Fatalf("score not applied: %+v", c.ScoreBreakdown)
	}
	if got := rules(c.ScoreBreakdown); len(got) != 1 || got[0] != "file_hash" {
		t.Fatalf("provenance lost on rescore: rules=%v", got)
	}

	if err := s.UpdateCandidateScore(id, certainScore(), "CERTAIN", "unified-v1"); err != nil {
		t.Fatal(err)
	}
	if got := rules(candidateFor(t, s).ScoreBreakdown); len(got) != 1 {
		t.Fatalf("provenance lost on single-row score update: rules=%v", got)
	}
}

func TestIsProvenanceOnly(t *testing.T) {
	if !provenanceScore("x", "y").IsProvenanceOnly() {
		t.Fatal("rule-only breakdown should be provenance-only")
	}
	if certainScore().IsProvenanceOnly() {
		t.Fatal("scored breakdown is not provenance-only")
	}
	var nilScore *models.UnifiedDedupScore
	if nilScore.IsProvenanceOnly() {
		t.Fatal("nil is not provenance-only")
	}
}
