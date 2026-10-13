// file: internal/database/embedding_store_delete_candidates_test.go
// version: 1.0.0
// guid: 3f81c7d2-9a4e-4b65-8c0d-e2a7b5f19d36
// last-edited: 2026-10-13

package database

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEmbeddingStore_DeleteCandidates: the batched delete removes every named
// ordinary row (and its index keys, so the pair can be re-upserted as new),
// skips a pinned manual candidate and an unknown ID without failing the batch,
// counts a repeated ID once, and treats an empty list as a no-op.
func TestEmbeddingStore_DeleteCandidates(t *testing.T) {
	s := newTestEmbeddingStore(t)

	var ids []int64
	for i := range 6 {
		id, _, err := s.UpsertCandidateNew(DedupCandidate{
			EntityType: "book",
			EntityAID:  fmt.Sprintf("a%d", i),
			EntityBID:  fmt.Sprintf("b%d", i),
			Layer:      "acoustid",
		})
		require.NoError(t, err)
		ids = append(ids, id)
	}
	pinned, err := s.EnqueueManualCandidate("book", "pin-a", "pin-b", "synthetic pin")
	require.NoError(t, err)

	n, err := s.DeleteCandidates(nil)
	require.NoError(t, err)
	require.Zero(t, n, "empty list")

	batch := append(append([]int64{}, ids[:4]...), ids[0], pinned.Candidate.ID, 999999)
	n, err = s.DeleteCandidates(batch)
	require.NoError(t, err)
	require.Equal(t, 4, n, "four ordinary rows; the repeat, the pin and the unknown ID are not counted")

	for _, id := range ids[:4] {
		got, err := s.GetCandidateByID(id)
		require.NoError(t, err)
		require.Nil(t, got, "candidate %d should be gone", id)
	}
	for _, id := range append(append([]int64{}, ids[4:]...), pinned.Candidate.ID) {
		got, err := s.GetCandidateByID(id)
		require.NoError(t, err)
		require.NotNil(t, got, "candidate %d was not named (or is pinned) and must survive", id)
	}

	// The pair's index keys went with the row: it upserts as a NEW candidate.
	_, isNew, err := s.UpsertCandidateNew(DedupCandidate{EntityType: "book", EntityAID: "a0", EntityBID: "b0", Layer: "acoustid"})
	require.NoError(t, err)
	require.True(t, isNew, "a deleted pair must leave no index key behind")
}
