// file: internal/plugins/dedup/cleanup_orphan_author_embeddings_test.go
// version: 1.0.0
// guid: 6f7a8b9c-0d1e-2f3a-4b5c-6d7e8f9a0b1c
// last-edited: 2026-07-08

// Tests for the dedup.cleanup-orphan-author-embeddings op (author-side
// counterpart to dedup.cleanup-orphan-embeddings).
//
// The key thing under test is the existence check: it must go through
// GetAllAuthors() (literal author:N key enumeration), NOT GetAuthorByID
// (which follows tombstone redirects for merged authors and would report a
// merged-away ID as "live"). cleanupOrphanAuthorMockStore deliberately wires
// GetAuthorByIDFunc to return a REDIRECTED canonical author for a merged ID,
// so a test that accidentally used GetAuthorByID for the existence check
// would wrongly classify it as live and fail to catch the regression.

package dedup

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cleanupOrphanAuthorMockStore returns a MockStore whose GetAllAuthors
// resolves only the given live author IDs. GetAuthorByID is wired to follow
// a tombstone-style redirect for mergedID -> canonicalID, mirroring
// PebbleStore.GetAuthorByID's real behavior: it returns the CANONICAL
// author's data for a merged-away ID rather than nil. Any op that used
// GetAuthorByID for the orphan check would therefore see mergedID as "live"
// and fail to flag it — exactly the bug this op exists to avoid.
func cleanupOrphanAuthorMockStore(liveIDs []int, mergedID, canonicalID int) *database.MockStore {
	authors := make([]database.Author, 0, len(liveIDs))
	byID := make(map[int]*database.Author, len(liveIDs))
	for _, id := range liveIDs {
		a := database.Author{ID: id, Name: "Live Author"}
		authors = append(authors, a)
		byID[id] = &a
	}
	return &database.MockStore{
		GetAllAuthorsFunc: func() ([]database.Author, error) {
			return authors, nil
		},
		GetAuthorByIDFunc: func(id int) (*database.Author, error) {
			if id == mergedID {
				// Tombstone redirect: returns the canonical author, non-nil.
				return byID[canonicalID], nil
			}
			return byID[id], nil
		},
	}
}

// seedAuthorEmbedding upserts an author embedding with the given entity ID and model.
func seedAuthorEmbedding(t *testing.T, es *database.EmbeddingStore, entityID, model string) {
	t.Helper()
	require.NoError(t, es.Upsert(database.Embedding{
		EntityType: "author",
		EntityID:   entityID,
		Model:      model,
		Vector:     []float32{0.1, 0.2, 0.3},
	}))
}

// TestCleanupOrphanAuthorEmbeddingsOp_Metadata asserts the OperationDef shape.
func TestCleanupOrphanAuthorEmbeddingsOp_Metadata(t *testing.T) {
	p := &Plugin{}
	def := p.cleanupOrphanAuthorEmbeddingsDef()
	assert.Equal(t, "dedup.cleanup-orphan-author-embeddings", def.ID)
	assert.Contains(t, def.Capabilities, sdk.CapLibraryRead)
	assert.Contains(t, def.Capabilities, sdk.CapLibraryWrite)

	var params cleanupOrphanAuthorEmbeddingsParams
	require.NoError(t, json.Unmarshal([]byte(`{}`), &params))
	assert.False(t, params.Apply, "apply must default to false")
}

// TestCleanupOrphanAuthorEmbeddingsOp_DryRunReportsCountsWithoutMutating
// asserts dry-run correctly classifies orphaned vs. live embeddings and
// writes nothing to the store.
func TestCleanupOrphanAuthorEmbeddingsOp_DryRunReportsCountsWithoutMutating(t *testing.T) {
	es := newTestEmbeddingStorePurge(t)
	ms := cleanupOrphanAuthorMockStore([]int{1, 2}, 39755, 1)

	seedAuthorEmbedding(t, es, "1", "bge-m3")
	seedAuthorEmbedding(t, es, "2", "text-embedding-3-large") // "wrong" model, but author is live
	seedAuthorEmbedding(t, es, "39755", "text-embedding-3-large")
	seedAuthorEmbedding(t, es, "40861", "text-embedding-3-large")

	p := buildPlugin(t, es, ms)
	params, err := json.Marshal(cleanupOrphanAuthorEmbeddingsParams{Apply: false})
	require.NoError(t, err)
	require.NoError(t, p.runCleanupOrphanAuthorEmbeddings(context.Background(), params, &mockReporter{}))

	all, err := es.ListByType("author")
	require.NoError(t, err)
	assert.Len(t, all, 4, "dry-run must not delete any embedding")
}

// TestCleanupOrphanAuthorEmbeddingsOp_ScanFlagsMergedAuthorAsOrphan is the
// regression test for the exact bug this op exists to avoid: a merged
// author's ID must be flagged orphaned even though GetAuthorByID(mergedID)
// would return the canonical author (non-nil) via tombstone redirect.
func TestCleanupOrphanAuthorEmbeddingsOp_ScanFlagsMergedAuthorAsOrphan(t *testing.T) {
	es := newTestEmbeddingStorePurge(t)
	ms := cleanupOrphanAuthorMockStore([]int{1}, 39755, 1)

	seedAuthorEmbedding(t, es, "1", "bge-m3")
	seedAuthorEmbedding(t, es, "39755", "text-embedding-3-large") // merged into author 1

	// Sanity-check the mock actually reproduces the redirect: GetAuthorByID
	// on the merged ID returns the canonical author, non-nil.
	canonical, err := ms.GetAuthorByID(39755)
	require.NoError(t, err)
	require.NotNil(t, canonical, "mock must reproduce GetAuthorByID's tombstone redirect")

	embeddings, err := es.ListByType("author")
	require.NoError(t, err)
	authors, err := ms.GetAllAuthors()
	require.NoError(t, err)
	liveIDs := map[string]struct{}{}
	for _, a := range authors {
		liveIDs[strconv.Itoa(a.ID)] = struct{}{}
	}

	report := scanOrphanAuthorEmbeddings(embeddings, liveIDs)
	assert.Equal(t, 2, report.Total)
	assert.Equal(t, 1, report.Orphaned)
	assert.Equal(t, 1, report.Live)
	assert.ElementsMatch(t, []string{"39755"}, report.OrphanIDs)
}

// TestCleanupOrphanAuthorEmbeddingsOp_ApplyDeletesOnlyOrphans asserts
// apply=true deletes only the rows whose author is confirmed gone, leaving
// live-author embeddings — including one with a "wrong" model — untouched.
func TestCleanupOrphanAuthorEmbeddingsOp_ApplyDeletesOnlyOrphans(t *testing.T) {
	es := newTestEmbeddingStorePurge(t)
	ms := cleanupOrphanAuthorMockStore([]int{1, 2}, 39755, 1)

	seedAuthorEmbedding(t, es, "1", "bge-m3")
	seedAuthorEmbedding(t, es, "2", "text-embedding-3-large") // stale model, but out of scope: author is live
	seedAuthorEmbedding(t, es, "39755", "text-embedding-3-large")
	seedAuthorEmbedding(t, es, "40861", "text-embedding-3-large")

	p := buildPlugin(t, es, ms)
	params, err := json.Marshal(cleanupOrphanAuthorEmbeddingsParams{Apply: true})
	require.NoError(t, err)
	require.NoError(t, p.runCleanupOrphanAuthorEmbeddings(context.Background(), params, &mockReporter{}))

	remaining, err := es.ListByType("author")
	require.NoError(t, err)
	require.Len(t, remaining, 2, "only the two orphaned rows should be deleted")

	ids := make([]string, 0, len(remaining))
	for _, e := range remaining {
		ids = append(ids, e.EntityID)
	}
	assert.ElementsMatch(t, []string{"1", "2"}, ids)

	live2, err := es.Get("author", "2")
	require.NoError(t, err)
	require.NotNil(t, live2)
	assert.Equal(t, "text-embedding-3-large", live2.Model)

	gone1, err := es.Get("author", "39755")
	require.NoError(t, err)
	assert.Nil(t, gone1)
	gone2, err := es.Get("author", "40861")
	require.NoError(t, err)
	assert.Nil(t, gone2)
}

// TestCleanupOrphanAuthorEmbeddingsOp_ApplyTwiceIsIdempotent asserts a second
// apply run after a clean pass finds nothing left to delete.
func TestCleanupOrphanAuthorEmbeddingsOp_ApplyTwiceIsIdempotent(t *testing.T) {
	es := newTestEmbeddingStorePurge(t)
	ms := cleanupOrphanAuthorMockStore([]int{1}, 39755, 1)

	seedAuthorEmbedding(t, es, "1", "bge-m3")
	seedAuthorEmbedding(t, es, "39755", "text-embedding-3-large")

	p := buildPlugin(t, es, ms)
	params, err := json.Marshal(cleanupOrphanAuthorEmbeddingsParams{Apply: true})
	require.NoError(t, err)

	require.NoError(t, p.runCleanupOrphanAuthorEmbeddings(context.Background(), params, &mockReporter{}))
	remaining, err := es.ListByType("author")
	require.NoError(t, err)
	require.Len(t, remaining, 1)
	assert.Equal(t, "1", remaining[0].EntityID)

	require.NoError(t, p.runCleanupOrphanAuthorEmbeddings(context.Background(), params, &mockReporter{}))
	remaining, err = es.ListByType("author")
	require.NoError(t, err)
	require.Len(t, remaining, 1)
	assert.Equal(t, "1", remaining[0].EntityID)
}
