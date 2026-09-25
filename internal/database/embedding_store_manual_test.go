// file: internal/database/embedding_store_manual_test.go
// version: 1.0.0
// guid: 4966ec42-ef03-41a0-8800-98ea0ff24c76
// last-edited: 2026-09-25

package database

import (
	"errors"
	"testing"

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
