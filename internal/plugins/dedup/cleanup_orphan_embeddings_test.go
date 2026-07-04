// file: internal/plugins/dedup/cleanup_orphan_embeddings_test.go
// version: 1.0.0
// guid: a53bc26a-3ecb-4aab-bef8-d6e76c174cd7
// last-edited: 2026-07-04

// Tests for the dedup.cleanup-orphan-embeddings op (retroactive counterpart to
// PR #1802's DeleteBook fix).
//
// These wire an in-memory EmbeddingStore + MockStore (no real Engine needed —
// the op only reads embeddings and looks up books), then exercise the op
// wrapper: dry-run reports correct orphan/live counts without mutating,
// apply deletes only orphaned rows and leaves live-book embeddings untouched
// (including ones with a "wrong" model — out of scope for this op), and a
// second apply run is idempotent (finds nothing left to delete).

package dedup

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cleanupOrphanMockStore returns a MockStore whose GetBookByID resolves only
// the given live book IDs; any other ID returns (nil, nil) — the "book gone"
// signal the op relies on.
func cleanupOrphanMockStore(liveIDs ...string) *database.MockStore {
	books := make(map[string]*database.Book, len(liveIDs))
	for _, id := range liveIDs {
		books[id] = &database.Book{ID: id, Title: "Live Book " + id}
	}
	return &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return books[id], nil
		},
	}
}

// seedEmbedding upserts a book embedding with the given entity ID and model.
func seedEmbedding(t *testing.T, es *database.EmbeddingStore, entityID, model string) {
	t.Helper()
	require.NoError(t, es.Upsert(database.Embedding{
		EntityType: "book",
		EntityID:   entityID,
		Model:      model,
		Vector:     []float32{0.1, 0.2, 0.3},
	}))
}

// TestCleanupOrphanEmbeddingsOp_Metadata asserts the OperationDef shape.
func TestCleanupOrphanEmbeddingsOp_Metadata(t *testing.T) {
	p := &Plugin{}
	def := p.cleanupOrphanEmbeddingsDef()
	assert.Equal(t, "dedup.cleanup-orphan-embeddings", def.ID)
	assert.Contains(t, def.Capabilities, sdk.CapLibraryRead)
	assert.Contains(t, def.Capabilities, sdk.CapLibraryWrite)

	var params cleanupOrphanEmbeddingsParams
	require.NoError(t, json.Unmarshal([]byte(`{}`), &params))
	assert.False(t, params.Apply, "apply must default to false")
}

// TestCleanupOrphanEmbeddingsOp_DryRunReportsCountsWithoutMutating asserts
// dry-run correctly classifies orphaned vs. live embeddings and writes
// nothing to the store.
func TestCleanupOrphanEmbeddingsOp_DryRunReportsCountsWithoutMutating(t *testing.T) {
	es := newTestEmbeddingStorePurge(t)
	ms := cleanupOrphanMockStore("book-live-1", "book-live-2")

	seedEmbedding(t, es, "book-live-1", "bge-m3")
	seedEmbedding(t, es, "book-live-2", "text-embedding-3-large") // "wrong" model, but book is live
	seedEmbedding(t, es, "book-gone-1", "text-embedding-3-large")
	seedEmbedding(t, es, "book-gone-2", "text-embedding-3-large")

	p := buildPlugin(t, es, ms)
	params, err := json.Marshal(cleanupOrphanEmbeddingsParams{Apply: false})
	require.NoError(t, err)
	require.NoError(t, p.runCleanupOrphanEmbeddings(context.Background(), params, &mockReporter{}))

	// Dry-run must not mutate: all 4 embeddings still present.
	all, err := es.ListByType("book")
	require.NoError(t, err)
	assert.Len(t, all, 4, "dry-run must not delete any embedding")
}

// TestCleanupOrphanEmbeddingsOp_ScanClassifiesCorrectly directly exercises the
// scan core to assert exact orphan/live counts and the sample contents.
func TestCleanupOrphanEmbeddingsOp_ScanClassifiesCorrectly(t *testing.T) {
	es := newTestEmbeddingStorePurge(t)
	ms := cleanupOrphanMockStore("book-live-1", "book-live-2")

	seedEmbedding(t, es, "book-live-1", "bge-m3")
	seedEmbedding(t, es, "book-live-2", "text-embedding-3-large")
	seedEmbedding(t, es, "book-gone-1", "text-embedding-3-large")
	seedEmbedding(t, es, "book-gone-2", "text-embedding-3-large")

	p := buildPlugin(t, es, ms)
	embeddings, err := es.ListByType("book")
	require.NoError(t, err)

	report, err := p.scanOrphanEmbeddings(context.Background(), &mockReporter{}, embeddings)
	require.NoError(t, err)

	assert.Equal(t, 4, report.Total)
	assert.Equal(t, 2, report.Orphaned)
	assert.Equal(t, 2, report.Live)
	assert.Equal(t, 0, report.LookupErr)
	assert.ElementsMatch(t, []string{"book-gone-1", "book-gone-2"}, report.OrphanIDs)
}

// TestCleanupOrphanEmbeddingsOp_ApplyDeletesOnlyOrphans asserts apply=true
// deletes only the rows whose book is confirmed gone, leaving live-book
// embeddings — including one with a "wrong" model — untouched.
func TestCleanupOrphanEmbeddingsOp_ApplyDeletesOnlyOrphans(t *testing.T) {
	es := newTestEmbeddingStorePurge(t)
	ms := cleanupOrphanMockStore("book-live-1", "book-live-2")

	seedEmbedding(t, es, "book-live-1", "bge-m3")
	seedEmbedding(t, es, "book-live-2", "text-embedding-3-large") // stale model, but out of scope: book is live
	seedEmbedding(t, es, "book-gone-1", "text-embedding-3-large")
	seedEmbedding(t, es, "book-gone-2", "text-embedding-3-large")

	p := buildPlugin(t, es, ms)
	params, err := json.Marshal(cleanupOrphanEmbeddingsParams{Apply: true})
	require.NoError(t, err)
	require.NoError(t, p.runCleanupOrphanEmbeddings(context.Background(), params, &mockReporter{}))

	remaining, err := es.ListByType("book")
	require.NoError(t, err)
	require.Len(t, remaining, 2, "only the two orphaned rows should be deleted")

	ids := make([]string, 0, len(remaining))
	for _, e := range remaining {
		ids = append(ids, e.EntityID)
	}
	assert.ElementsMatch(t, []string{"book-live-1", "book-live-2"}, ids)

	// The live book with a "wrong" model must be untouched — model-aware
	// re-embed is out of scope for this op.
	live2, err := es.Get("book", "book-live-2")
	require.NoError(t, err)
	require.NotNil(t, live2)
	assert.Equal(t, "text-embedding-3-large", live2.Model)

	// Orphaned rows are gone.
	gone1, err := es.Get("book", "book-gone-1")
	require.NoError(t, err)
	assert.Nil(t, gone1)
	gone2, err := es.Get("book", "book-gone-2")
	require.NoError(t, err)
	assert.Nil(t, gone2)
}

// TestCleanupOrphanEmbeddingsOp_ApplyTwiceIsIdempotent asserts a second apply
// run after a clean pass finds nothing left to delete.
func TestCleanupOrphanEmbeddingsOp_ApplyTwiceIsIdempotent(t *testing.T) {
	es := newTestEmbeddingStorePurge(t)
	ms := cleanupOrphanMockStore("book-live-1")

	seedEmbedding(t, es, "book-live-1", "bge-m3")
	seedEmbedding(t, es, "book-gone-1", "text-embedding-3-large")

	p := buildPlugin(t, es, ms)
	params, err := json.Marshal(cleanupOrphanEmbeddingsParams{Apply: true})
	require.NoError(t, err)

	// First apply deletes the one orphan.
	require.NoError(t, p.runCleanupOrphanEmbeddings(context.Background(), params, &mockReporter{}))
	remaining, err := es.ListByType("book")
	require.NoError(t, err)
	require.Len(t, remaining, 1)
	assert.Equal(t, "book-live-1", remaining[0].EntityID)

	// Second apply against the now-clean store must be a no-op — nothing left
	// to delete, and the live embedding is still present.
	require.NoError(t, p.runCleanupOrphanEmbeddings(context.Background(), params, &mockReporter{}))
	remaining, err = es.ListByType("book")
	require.NoError(t, err)
	require.Len(t, remaining, 1)
	assert.Equal(t, "book-live-1", remaining[0].EntityID)
}
