// file: internal/database/candidate_score_batch_test.go
// version: 1.0.0
// guid: 0a5e83c1-64bf-4d72-b9e3-8c17f0a2d946
// last-edited: 2026-09-02

package database

import (
	"strconv"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	"github.com/falkcorp/audiobook-organizer/internal/models"
)

func seedCandidate(t *testing.T, s *EmbeddingStore, i int) int64 {
	t.Helper()
	sim := 0.9
	id, _, err := s.UpsertCandidateNew(DedupCandidate{
		EntityType:     "book",
		EntityAID:      "a" + strconv.Itoa(i),
		EntityBID:      "b" + strconv.Itoa(i),
		Layer:          "embedding",
		Similarity:     &sim,
		Status:         "pending",
		Band:           unified.BandHigh,
		FormulaVersion: "unified_v1",
	})
	if err != nil {
		t.Fatalf("UpsertCandidateNew: %v", err)
	}
	return id
}

// TestUpdateCandidateScores_WritesBatchAndReportsMissing is the store half of
// D4: the whole-backlog re-band writes through ONE batch instead of one
// fsync-ed Set per row, and a row that vanished between listing and writing is
// reported back rather than failing the batch or being silently counted as
// applied.
func TestUpdateCandidateScores_WritesBatchAndReportsMissing(t *testing.T) {
	s := newTestEmbeddingStore(t)
	id1 := seedCandidate(t, s, 1)
	id2 := seedCandidate(t, s, 2)

	score := &models.UnifiedDedupScore{Score: 98, Band: unified.BandCertain, Formula: "unified_v1"}
	applied, failed, err := s.UpdateCandidateScores([]CandidateScoreUpdate{
		{ID: id1, Score: score, Band: unified.BandCertain, FormulaVersion: "unified_v1"},
		{ID: id2, Score: score, Band: unified.BandCertain, FormulaVersion: "unified_v1"},
		{ID: 999999, Score: score, Band: unified.BandCertain, FormulaVersion: "unified_v1"},
	})
	if err != nil {
		t.Fatalf("UpdateCandidateScores: %v", err)
	}
	if applied != 2 {
		t.Errorf("applied = %d, want 2", applied)
	}
	if len(failed) != 1 || failed[0] != 999999 {
		t.Errorf("failed = %v, want [999999] (the row that no longer exists)", failed)
	}
	if err := s.SyncCandidateWrites(); err != nil {
		t.Fatalf("SyncCandidateWrites: %v", err)
	}

	got, _, err := s.ListCandidates(CandidateFilter{Status: "pending", Limit: 10})
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(got))
	}
	for _, c := range got {
		if c.Band != unified.BandCertain {
			t.Errorf("candidate %d band = %q, want %q — the batch did not commit", c.ID, c.Band, unified.BandCertain)
		}
		if c.ScoreBreakdown == nil || c.ScoreBreakdown.Score != 98 {
			t.Errorf("candidate %d breakdown not written: %+v", c.ID, c.ScoreBreakdown)
		}
	}
}

// TestUpdateCandidateScores_SerializesWithUpsert: the re-band and a concurrent
// dedup.full-scan upsert both read-modify-write dedupRecKey(id). The per-row
// path used to skip s.mu entirely, so an interleaving lost one side's fields.
// Under -race this also proves the two paths share a lock.
func TestUpdateCandidateScores_SerializesWithUpsert(t *testing.T) {
	s := newTestEmbeddingStore(t)
	ids := make([]int64, 0, 20)
	for i := range 20 {
		ids = append(ids, seedCandidate(t, s, i))
	}
	score := &models.UnifiedDedupScore{Score: 98, Band: unified.BandCertain, Formula: "unified_v1"}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 10 {
			ups := make([]CandidateScoreUpdate, 0, len(ids))
			for _, id := range ids {
				ups = append(ups, CandidateScoreUpdate{ID: id, Score: score, Band: unified.BandCertain, FormulaVersion: "unified_v1"})
			}
			if _, _, err := s.UpdateCandidateScores(ups); err != nil {
				t.Errorf("UpdateCandidateScores: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for range 10 {
			for i := range 20 {
				seedCandidate(t, s, i)
			}
		}
	}()
	wg.Wait()

	if err := s.SyncCandidateWrites(); err != nil {
		t.Fatalf("SyncCandidateWrites: %v", err)
	}
	got, _, err := s.ListCandidates(CandidateFilter{Status: "pending", Limit: 100})
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	if len(got) != len(ids) {
		t.Fatalf("candidate count = %d, want %d — a concurrent upsert duplicated or dropped rows", len(got), len(ids))
	}
}
