// file: internal/database/hnsw_embedding_store_persist_test.go
// version: 1.0.0
// guid: c3d4e5f6-a7b8-9012-cdef-012345678902
// last-edited: 2026-06-15

package database_test

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHNSWEmbeddingStore_ExportImportRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := database.NewHNSWEmbeddingStore(4)
	require.NoError(t, store.Upsert(ctx, "book", "b1", []float32{1, 0, 0, 0}, map[string]string{"title": "Dune"}))
	require.NoError(t, store.Upsert(ctx, "book", "b2", []float32{0, 1, 0, 0}, map[string]string{"title": "Foundation"}))

	dir := t.TempDir()
	require.NoError(t, store.Export(dir))

	store2 := database.NewHNSWEmbeddingStore(4)
	require.NoError(t, store2.Import(dir))

	meta, err := store2.Get(ctx, "book", "b1")
	require.NoError(t, err)
	assert.Equal(t, "Dune", meta["title"])

	count, err := store2.CountByType(ctx, "book")
	require.NoError(t, err)
	assert.Equal(t, 2, count)
}

func TestHNSWEmbeddingStore_Import_ErrNoSnapshot(t *testing.T) {
	store := database.NewHNSWEmbeddingStore(4)
	err := store.Import(t.TempDir())
	assert.ErrorIs(t, err, database.ErrNoHNSWSnapshot)
}
