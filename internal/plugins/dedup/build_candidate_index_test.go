// file: internal/plugins/dedup/build_candidate_index_test.go
// version: 1.0.0
// guid: 3d8f1c2a-6b4e-4a9d-9c7f-1e5a8b3d6f20
// last-edited: 2026-07-11

// Tests for the dedup.build-candidate-status-index op (INIT-2 T4).
//
// Wires a real EmbeddingStore backed by a temporary PebbleDB (no mock store
// needed — the op only touches p.embeddingStore) and exercises the op
// wrapper: it rebuilds dedup:s: rows for candidates whose index entries are
// missing (simulating pre-index legacy rows), sets the completion flag on a
// clean run, and is idempotent on re-run.

package dedup

import (
	"context"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildCandidateStatusIndexOp_Metadata asserts the OperationDef shape.
func TestBuildCandidateStatusIndexOp_Metadata(t *testing.T) {
	p := &Plugin{}
	def := p.buildCandidateStatusIndexDef()
	assert.Equal(t, "dedup.build-candidate-status-index", def.ID)
	assert.True(t, def.Cancellable)
}

// TestBuildCandidateStatusIndexOp_RebuildsMissingRowsAndSetsFlag simulates
// legacy pre-index candidates (dedup:s: rows deleted out from under otherwise
// normal dedup:r: records — the state a real pre-INIT-2-T4 store would be in)
// and asserts the op rebuilds every row and sets the completion flag on a
// clean, uncancelled run.
func TestBuildCandidateStatusIndexOp_RebuildsMissingRowsAndSetsFlag(t *testing.T) {
	es := newTestEmbeddingStorePurge(t)

	require.NoError(t, es.UpsertCandidate(database.DedupCandidate{
		EntityType: "book", EntityAID: "b1", EntityBID: "b2", Layer: "embedding", Status: "pending",
	}))
	require.NoError(t, es.UpsertCandidate(database.DedupCandidate{
		EntityType: "book", EntityAID: "b3", EntityBID: "b4", Layer: "embedding", Status: "merged",
	}))
	require.NoError(t, es.UpsertCandidate(database.DedupCandidate{
		EntityType: "author", EntityAID: "a1", EntityBID: "a2", Layer: "metadata", Status: "dismissed",
	}))

	results, _, err := es.ListCandidates(database.CandidateFilter{})
	require.NoError(t, err)
	require.Len(t, results, 3)

	// Simulate legacy rows: wipe every dedup:s: entry the inline write-path
	// maintenance already wrote, as if these candidates predate INIT-2 T4.
	wipeStatusIndex(t, es)
	assert.Empty(t, statusIndexRowsPlugin(t, es), "precondition: index wiped")
	assert.False(t, es.IsCandidateStatusIndexBuilt())

	p := &Plugin{embeddingStore: es}
	require.NoError(t, p.runBuildCandidateStatusIndex(context.Background(), nil, &mockReporter{}))

	assert.True(t, es.IsCandidateStatusIndexBuilt(), "flag must be set after a clean run")

	rows := statusIndexRowsPlugin(t, es)
	require.Len(t, rows, 3, "one status-index row rebuilt per candidate")

	for _, c := range results {
		got, _, err := es.ListCandidates(database.CandidateFilter{Status: c.Status})
		require.NoError(t, err)
		found := false
		for _, g := range got {
			if g.ID == c.ID {
				found = true
			}
		}
		assert.True(t, found, "candidate %d (status=%s) must be findable via the now-built index", c.ID, c.Status)
	}
}

// TestBuildCandidateStatusIndexOp_IdempotentReRun asserts a second run over
// an already-indexed store neither duplicates rows nor errors.
func TestBuildCandidateStatusIndexOp_IdempotentReRun(t *testing.T) {
	es := newTestEmbeddingStorePurge(t)
	require.NoError(t, es.UpsertCandidate(database.DedupCandidate{
		EntityType: "book", EntityAID: "b1", EntityBID: "b2", Layer: "embedding", Status: "pending",
	}))

	p := &Plugin{embeddingStore: es}
	require.NoError(t, p.runBuildCandidateStatusIndex(context.Background(), nil, &mockReporter{}))
	first := statusIndexRowsPlugin(t, es)
	require.Len(t, first, 1)

	require.NoError(t, p.runBuildCandidateStatusIndex(context.Background(), nil, &mockReporter{}))
	second := statusIndexRowsPlugin(t, es)
	assert.Equal(t, first, second, "re-running must not duplicate or otherwise change index rows")
	assert.True(t, es.IsCandidateStatusIndexBuilt())
}

// TestBuildCandidateStatusIndexOp_NoEmbeddingStore asserts the op fails fast
// with a clear error rather than a nil-pointer panic when wired without an
// embedding store.
func TestBuildCandidateStatusIndexOp_NoEmbeddingStore(t *testing.T) {
	p := &Plugin{}
	err := p.runBuildCandidateStatusIndex(context.Background(), nil, &mockReporter{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "embedding store not available")
}

// wipeStatusIndex deletes every "dedup:s:" row directly, bypassing the
// write-path maintenance, to simulate a pre-INIT-2-T4 store for the op test.
func wipeStatusIndex(t *testing.T, es *database.EmbeddingStore) {
	t.Helper()
	db := es.PebbleDB()
	require.NotNil(t, db)
	for _, key := range statusIndexRowsPlugin(t, es) {
		require.NoError(t, db.Delete([]byte(key), pebble.Sync))
	}
}

// statusIndexRowsPlugin returns every "dedup:s:" key currently stored, for
// direct assertion from the plugin package (which cannot see the database
// package's unexported dedupStatusIdxPfx constant, so it hardcodes the same
// literal the embedding_store.go key-space doc comment documents).
func statusIndexRowsPlugin(t *testing.T, es *database.EmbeddingStore) []string {
	t.Helper()
	db := es.PebbleDB()
	require.NotNil(t, db)
	prefix := []byte("dedup:s:")
	upper := append([]byte{}, prefix...)
	upper[len(upper)-1]++
	iter, err := db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	require.NoError(t, err)
	defer iter.Close()

	var rows []string
	for iter.First(); iter.Valid(); iter.Next() {
		rows = append(rows, string(iter.Key()))
	}
	require.NoError(t, iter.Error())
	return rows
}
