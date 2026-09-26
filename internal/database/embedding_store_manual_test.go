// file: internal/database/embedding_store_manual_test.go
// version: 1.2.0
// guid: 4966ec42-ef03-41a0-8800-98ea0ff24c76
// last-edited: 2026-09-26

package database

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/models"
	"github.com/stretchr/testify/require"
)

func pendingByPair(t *testing.T, s *EmbeddingStore, a, b string) []DedupCandidate {
	t.Helper()
	all, _, err := s.ListCandidates(CandidateFilter{EntityType: "book", Limit: 1000})
	require.NoError(t, err)
	var out []DedupCandidate
	for _, c := range all {
		if (c.EntityAID == a && c.EntityBID == b) || (c.EntityAID == b && c.EntityBID == a) {
			out = append(out, c)
		}
	}
	return out
}

func TestManualCandidate_PlanWritesNothing(t *testing.T) {
	s := newTestEmbeddingStore(t)

	res, err := s.PlanManualCandidate("book", "b2", "b1", "shell of b2")
	require.NoError(t, err)
	require.Equal(t, ManualCandidateCreated, res.Outcome)
	require.Equal(t, int64(0), res.Candidate.ID)
	require.Equal(t, "b1", res.Candidate.EntityAID, "pair is canonicalised")
	require.Equal(t, CandidateLayerManual, res.Candidate.Layer)
	require.Empty(t, pendingByPair(t, s, "b1", "b2"), "a plan must not write")
}

func TestManualCandidate_CreateThenIdempotent(t *testing.T) {
	s := newTestEmbeddingStore(t)

	first, err := s.EnqueueManualCandidate("book", "b2", "b1", "shell of b2")
	require.NoError(t, err)
	require.Equal(t, ManualCandidateCreated, first.Outcome)
	require.NotZero(t, first.Candidate.ID)

	got, err := s.GetCandidateByID(first.Candidate.ID)
	require.NoError(t, err)
	require.Equal(t, "pending", got.Status)
	require.Equal(t, CandidateLayerManual, got.Layer)
	require.Equal(t, CandidateSourceManual, got.Source)
	require.Equal(t, "shell of b2", got.SourceNote)
	require.True(t, IsManualCandidate(*got))

	// Every index class resolves the row: status index, entity index, source filter.
	byStatus, _, err := s.ListCandidates(CandidateFilter{Status: "pending", Source: CandidateSourceManual, Limit: 10})
	require.NoError(t, err)
	require.Len(t, byStatus, 1)
	forEntity, err := s.ListCandidatesForEntity("book", "b2", "pending")
	require.NoError(t, err)
	require.Len(t, forEntity, 1)

	second, err := s.EnqueueManualCandidate("book", "b1", "b2", "again")
	require.NoError(t, err)
	require.Equal(t, ManualCandidateAlreadyOpen, second.Outcome)
	require.False(t, second.Pinned)
	require.Equal(t, first.Candidate.ID, second.Candidate.ID)
	require.Len(t, pendingByPair(t, s, "b1", "b2"), 1)
}

func TestManualCandidate_PinsOpenScannerRow(t *testing.T) {
	s := newTestEmbeddingStore(t)
	sim := 0.91
	id, _, err := s.UpsertCandidateNew(DedupCandidate{EntityType: "book", EntityAID: "b1", EntityBID: "b2", Layer: "embedding", Similarity: &sim})
	require.NoError(t, err)

	plan, err := s.PlanManualCandidate("book", "b1", "b2", "look")
	require.NoError(t, err)
	require.Equal(t, ManualCandidateAlreadyOpen, plan.Outcome)
	require.True(t, plan.Pinned)
	unchanged, err := s.GetCandidateByID(id)
	require.NoError(t, err)
	require.Empty(t, unchanged.Source, "plan must not pin")

	res, err := s.EnqueueManualCandidate("book", "b1", "b2", "look")
	require.NoError(t, err)
	require.Equal(t, ManualCandidateAlreadyOpen, res.Outcome)
	require.True(t, res.Pinned)
	got, err := s.GetCandidateByID(id)
	require.NoError(t, err)
	require.Equal(t, "embedding", got.Layer, "pinning keeps the scanner layer")
	require.Equal(t, CandidateSourceManual, got.Source)
}

func TestManualCandidate_ReopensMachineReclassified(t *testing.T) {
	s := newTestEmbeddingStore(t)
	id, _, err := s.UpsertCandidateNew(DedupCandidate{EntityType: "book", EntityAID: "b1", EntityBID: "b2", Layer: "exact"})
	require.NoError(t, err)
	require.NoError(t, s.UpdateCandidateStatus(id, "stale-drain"))

	res, err := s.EnqueueManualCandidate("book", "b1", "b2", "")
	require.NoError(t, err)
	require.Equal(t, ManualCandidateReopened, res.Outcome)
	got, err := s.GetCandidateByID(id)
	require.NoError(t, err)
	require.Equal(t, "pending", got.Status)

	// The status index moved with it.
	stale, _, err := s.ListCandidates(CandidateFilter{Status: "stale-drain", Limit: 10})
	require.NoError(t, err)
	require.Empty(t, stale)
}

func TestManualCandidate_LeavesDecidedRowAlone(t *testing.T) {
	s := newTestEmbeddingStore(t)
	id, _, err := s.UpsertCandidateNew(DedupCandidate{EntityType: "book", EntityAID: "b1", EntityBID: "b2", Layer: "exact"})
	require.NoError(t, err)
	require.NoError(t, s.UpdateCandidateStatus(id, "dismissed"))

	res, err := s.EnqueueManualCandidate("book", "b1", "b2", "")
	require.NoError(t, err)
	require.Equal(t, ManualCandidateDecided, res.Outcome)
	got, err := s.GetCandidateByID(id)
	require.NoError(t, err)
	require.Equal(t, "dismissed", got.Status)
	require.Empty(t, got.Source)
}

// A scanner re-upsert of a manual pair must keep both the manual layer and the
// Source mark: losing either would drop the row back into the purges.
func TestManualCandidate_SurvivesScannerUpsert(t *testing.T) {
	s := newTestEmbeddingStore(t)
	res, err := s.EnqueueManualCandidate("book", "b1", "b2", "note")
	require.NoError(t, err)

	sim := 0.5
	_, isNew, err := s.UpsertCandidateNew(DedupCandidate{EntityType: "book", EntityAID: "b1", EntityBID: "b2", Layer: "embedding", Similarity: &sim, Status: "pending"})
	require.NoError(t, err)
	require.False(t, isNew)

	got, err := s.GetCandidateByID(res.Candidate.ID)
	require.NoError(t, err)
	require.Equal(t, CandidateLayerManual, got.Layer)
	require.Equal(t, CandidateSourceManual, got.Source)
	require.Equal(t, "note", got.SourceNote)
}

func TestManualCandidate_DeleteCandidateRefuses(t *testing.T) {
	s := newTestEmbeddingStore(t)
	res, err := s.EnqueueManualCandidate("book", "b1", "b2", "")
	require.NoError(t, err)

	err = s.DeleteCandidate(res.Candidate.ID)
	require.True(t, errors.Is(err, ErrManualCandidateProtected), "got %v", err)
	got, err := s.GetCandidateByID(res.Candidate.ID)
	require.NoError(t, err)
	require.NotNil(t, got)

	// Control: an ordinary scanner row still deletes.
	id, _, err := s.UpsertCandidateNew(DedupCandidate{EntityType: "book", EntityAID: "c1", EntityBID: "c2", Layer: "exact"})
	require.NoError(t, err)
	require.NoError(t, s.DeleteCandidate(id))
	gone, err := s.GetCandidateByID(id)
	require.NoError(t, err)
	require.Nil(t, gone)
}

func TestManualCandidate_RejectsSelfPair(t *testing.T) {
	s := newTestEmbeddingStore(t)
	_, err := s.EnqueueManualCandidate("book", "b1", "b1", "")
	require.Error(t, err)
}

// ReclassifyCandidate is the automated status write: it refuses a manual row
// and a row whose status moved since the pass read it, and moves an ordinary
// pending row (the positive control).
func TestReclassifyCandidate_GuardsManualAndChangedStatus(t *testing.T) {
	s := newTestEmbeddingStore(t)

	pinned, _, err := s.UpsertCandidateNew(DedupCandidate{EntityType: "book", EntityAID: "b1", EntityBID: "b2", Layer: "exact"})
	require.NoError(t, err)
	_, err = s.EnqueueManualCandidate("book", "b1", "b2", "")
	require.NoError(t, err)
	err = s.ReclassifyCandidate(pinned, "pending", "stale-drain")
	require.True(t, errors.Is(err, ErrManualCandidateProtected), "got %v", err)
	got, err := s.GetCandidateByID(pinned)
	require.NoError(t, err)
	require.Equal(t, "pending", got.Status)

	decided, _, err := s.UpsertCandidateNew(DedupCandidate{EntityType: "book", EntityAID: "c1", EntityBID: "c2", Layer: "exact"})
	require.NoError(t, err)
	require.NoError(t, s.UpdateCandidateStatus(decided, "dismissed"))
	err = s.ReclassifyCandidate(decided, "pending", "stale-fp")
	require.True(t, errors.Is(err, ErrCandidateStatusChanged), "got %v", err)
	got, err = s.GetCandidateByID(decided)
	require.NoError(t, err)
	require.Equal(t, "dismissed", got.Status)

	err = s.ReclassifyCandidate(99999, "pending", "stale-fp")
	require.True(t, errors.Is(err, ErrCandidateStatusChanged), "missing row: got %v", err)

	plain, _, err := s.UpsertCandidateNew(DedupCandidate{EntityType: "book", EntityAID: "d1", EntityBID: "d2", Layer: "exact"})
	require.NoError(t, err)
	require.NoError(t, s.ReclassifyCandidate(plain, "pending", "stale-drain"))
	got, err = s.GetCandidateByID(plain)
	require.NoError(t, err)
	require.Equal(t, "stale-drain", got.Status)
	moved, _, err := s.ListCandidates(CandidateFilter{Status: "stale-drain", Limit: 10})
	require.NoError(t, err)
	require.Len(t, moved, 1, "status index moved with the row")
}

// An LLM verdict must not rewrite a pinned row (Layer would become "llm" and
// the reviewer loses the scanner layer) nor a row a human already decided.
func TestUpdateCandidateLLM_RefusesManualAndDecidedRows(t *testing.T) {
	s := newTestEmbeddingStore(t)

	pinned, _, err := s.UpsertCandidateNew(DedupCandidate{EntityType: "book", EntityAID: "b1", EntityBID: "b2", Layer: "embedding"})
	require.NoError(t, err)
	_, err = s.EnqueueManualCandidate("book", "b1", "b2", "")
	require.NoError(t, err)
	err = s.UpdateCandidateLLM(pinned, "duplicate", "[high] same")
	require.True(t, errors.Is(err, ErrManualCandidateProtected), "got %v", err)
	got, err := s.GetCandidateByID(pinned)
	require.NoError(t, err)
	require.Equal(t, "embedding", got.Layer)
	require.Empty(t, got.LLMVerdict)

	decided, _, err := s.UpsertCandidateNew(DedupCandidate{EntityType: "book", EntityAID: "c1", EntityBID: "c2", Layer: "embedding"})
	require.NoError(t, err)
	require.NoError(t, s.UpdateCandidateStatus(decided, "dismissed"))
	err = s.UpdateCandidateLLM(decided, "duplicate", "x")
	require.True(t, errors.Is(err, ErrCandidateStatusChanged), "got %v", err)

	plain, _, err := s.UpsertCandidateNew(DedupCandidate{EntityType: "book", EntityAID: "d1", EntityBID: "d2", Layer: "embedding"})
	require.NoError(t, err)
	require.NoError(t, s.UpdateCandidateLLM(plain, "duplicate", "x"))
	got, err = s.GetCandidateByID(plain)
	require.NoError(t, err)
	require.Equal(t, "llm", got.Layer)
	require.Equal(t, "duplicate", got.LLMVerdict)
}

// A pin landing after Rescore listed the backlog must still stop the re-band.
func TestUpdateCandidateScores_SkipsManualRow(t *testing.T) {
	s := newTestEmbeddingStore(t)
	pinned, _, err := s.UpsertCandidateNew(DedupCandidate{EntityType: "book", EntityAID: "b1", EntityBID: "b2", Layer: "embedding", Band: "REVIEW"})
	require.NoError(t, err)
	_, err = s.EnqueueManualCandidate("book", "b1", "b2", "")
	require.NoError(t, err)
	plain, _, err := s.UpsertCandidateNew(DedupCandidate{EntityType: "book", EntityAID: "c1", EntityBID: "c2", Layer: "embedding", Band: "REVIEW"})
	require.NoError(t, err)

	applied, failed, err := s.UpdateCandidateScores([]CandidateScoreUpdate{
		{ID: pinned, Band: "CERTAIN"},
		{ID: plain, Band: "CERTAIN"},
	})
	require.NoError(t, err)
	require.Empty(t, failed)
	require.Equal(t, 1, applied)
	got, err := s.GetCandidateByID(pinned)
	require.NoError(t, err)
	require.Equal(t, "REVIEW", got.Band)
	got, err = s.GetCandidateByID(plain)
	require.NoError(t, err)
	require.Equal(t, "CERTAIN", got.Band)
}

// A pin freezes the score: a scanner re-upsert of a pinned scanner row (Source
// manual, scanner layer) must not rewrite its band, score or formula, but may
// still refresh the evidence fields (Similarity).
func TestUpsertCandidateNew_PinnedScannerRowKeepsScore(t *testing.T) {
	s := newTestEmbeddingStore(t)
	sim1, sim2 := 0.81, 0.93
	id, _, err := s.UpsertCandidateNew(DedupCandidate{
		EntityType: "book", EntityAID: "b1", EntityBID: "b2", Layer: "embedding", Similarity: &sim1,
		ScoreBreakdown: &models.UnifiedDedupScore{Score: 70, Band: "REVIEW", Formula: "v1"},
		Band:           "REVIEW", FormulaVersion: "v1",
	})
	require.NoError(t, err)
	_, err = s.EnqueueManualCandidate("book", "b1", "b2", "look at this")
	require.NoError(t, err)

	_, isNew, err := s.UpsertCandidateNew(DedupCandidate{
		EntityType: "book", EntityAID: "b2", EntityBID: "b1", Layer: "embedding", Similarity: &sim2,
		ScoreBreakdown: &models.UnifiedDedupScore{Score: 99, Band: "CERTAIN", Formula: "v2"},
		Band:           "CERTAIN", FormulaVersion: "v2",
		LLMVerdict: "duplicate", LLMReason: "same book",
	})
	require.NoError(t, err)
	require.False(t, isNew)

	got, err := s.GetCandidateByID(id)
	require.NoError(t, err)
	require.Equal(t, "REVIEW", got.Band)
	require.Equal(t, "v1", got.FormulaVersion)
	require.NotNil(t, got.ScoreBreakdown)
	require.Equal(t, 70.0, got.ScoreBreakdown.Score)
	require.Equal(t, CandidateSourceManual, got.Source)
	require.Equal(t, "pending", got.Status)
	require.NotNil(t, got.Similarity)
	require.Equal(t, sim2, *got.Similarity, "evidence fields still refresh")
	require.Empty(t, got.LLMVerdict, "an LLM verdict on a pinned row is advice only")
	require.Equal(t, "duplicate", got.AIAdviceVerdict)
	require.Equal(t, "same book", got.AIAdviceReason)
}

// A pinned row with no band must not gain one from a re-upsert.
func TestUpsertCandidateNew_PinnedRowWithoutBandStaysUnbanded(t *testing.T) {
	s := newTestEmbeddingStore(t)
	id, _, err := s.UpsertCandidateNew(DedupCandidate{EntityType: "book", EntityAID: "b1", EntityBID: "b2", Layer: "embedding"})
	require.NoError(t, err)
	_, err = s.EnqueueManualCandidate("book", "b1", "b2", "")
	require.NoError(t, err)
	_, _, err = s.UpsertCandidateNew(DedupCandidate{
		EntityType: "book", EntityAID: "b1", EntityBID: "b2", Layer: "embedding",
		ScoreBreakdown: &models.UnifiedDedupScore{Score: 99, Band: "CERTAIN"}, Band: "CERTAIN", FormulaVersion: "v2",
	})
	require.NoError(t, err)
	got, err := s.GetCandidateByID(id)
	require.NoError(t, err)
	require.Empty(t, got.Band)
	require.Nil(t, got.ScoreBreakdown)
	require.Empty(t, got.FormulaVersion)
}

// An unpinned row is still re-scored by a re-upsert.
func TestUpsertCandidateNew_UnpinnedRowIsRescored(t *testing.T) {
	s := newTestEmbeddingStore(t)
	id, _, err := s.UpsertCandidateNew(DedupCandidate{EntityType: "book", EntityAID: "b1", EntityBID: "b2", Layer: "embedding", Band: "REVIEW", FormulaVersion: "v1"})
	require.NoError(t, err)
	_, _, err = s.UpsertCandidateNew(DedupCandidate{EntityType: "book", EntityAID: "b1", EntityBID: "b2", Layer: "embedding", Band: "HIGH", FormulaVersion: "v2", LLMVerdict: "duplicate"})
	require.NoError(t, err)
	got, err := s.GetCandidateByID(id)
	require.NoError(t, err)
	require.Equal(t, "HIGH", got.Band)
	require.Equal(t, "duplicate", got.LLMVerdict)
	require.Empty(t, got.AIAdviceVerdict)
}

// The single-row rescore write refuses a pinned row, and a pin landing after
// the caller listed the row is honoured at write time.
func TestUpdateCandidateScore_RefusesManualRow(t *testing.T) {
	s := newTestEmbeddingStore(t)
	pinned, _, err := s.UpsertCandidateNew(DedupCandidate{EntityType: "book", EntityAID: "b1", EntityBID: "b2", Layer: "embedding", Band: "REVIEW"})
	require.NoError(t, err)
	_, err = s.EnqueueManualCandidate("book", "b1", "b2", "")
	require.NoError(t, err)
	plain, _, err := s.UpsertCandidateNew(DedupCandidate{EntityType: "book", EntityAID: "c1", EntityBID: "c2", Layer: "embedding", Band: "REVIEW"})
	require.NoError(t, err)

	err = s.UpdateCandidateScore(pinned, &models.UnifiedDedupScore{Score: 99, Band: "CERTAIN"}, "CERTAIN", "v2")
	require.ErrorIs(t, err, ErrManualCandidateProtected)
	got, err := s.GetCandidateByID(pinned)
	require.NoError(t, err)
	require.Equal(t, "REVIEW", got.Band)
	require.Nil(t, got.ScoreBreakdown)

	require.NoError(t, s.UpdateCandidateScore(plain, &models.UnifiedDedupScore{Score: 99, Band: "CERTAIN"}, "CERTAIN", "v2"))
	got, err = s.GetCandidateByID(plain)
	require.NoError(t, err)
	require.Equal(t, "CERTAIN", got.Band)
}

// LLM advice lands only on a pending manual row and touches nothing else.
func TestRecordCandidateLLMAdvice(t *testing.T) {
	s := newTestEmbeddingStore(t)
	pinned, _, err := s.UpsertCandidateNew(DedupCandidate{EntityType: "book", EntityAID: "b1", EntityBID: "b2", Layer: "embedding", Band: "REVIEW", FormulaVersion: "v1"})
	require.NoError(t, err)
	_, err = s.EnqueueManualCandidate("book", "b1", "b2", "")
	require.NoError(t, err)

	require.NoError(t, s.RecordCandidateLLMAdvice(pinned, "duplicate", "[high] same narrator"))
	got, err := s.GetCandidateByID(pinned)
	require.NoError(t, err)
	require.Equal(t, "duplicate", got.AIAdviceVerdict)
	require.Equal(t, "[high] same narrator", got.AIAdviceReason)
	require.NotNil(t, got.AIAdviceAt)
	require.Equal(t, "pending", got.Status)
	require.Equal(t, "REVIEW", got.Band)
	require.Equal(t, "embedding", got.Layer)
	require.Empty(t, got.LLMVerdict)
	require.Equal(t, CandidateSourceManual, got.Source)

	plain, _, err := s.UpsertCandidateNew(DedupCandidate{EntityType: "book", EntityAID: "c1", EntityBID: "c2", Layer: "embedding"})
	require.NoError(t, err)
	require.ErrorIs(t, s.RecordCandidateLLMAdvice(plain, "duplicate", "x"), ErrCandidateNotManual)

	require.NoError(t, s.UpdateCandidateStatus(pinned, "dismissed"))
	require.ErrorIs(t, s.RecordCandidateLLMAdvice(pinned, "not_duplicate", "y"), ErrCandidateStatusChanged)
	require.ErrorIs(t, s.RecordCandidateLLMAdvice(999999, "duplicate", "z"), ErrCandidateStatusChanged)
}
