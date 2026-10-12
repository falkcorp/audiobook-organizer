// file: internal/metafetch/pcache_followup_test.go
// version: 1.1.0
// guid: e971d44a-1aa0-4ff3-8223-e9a5a43c932b
// last-edited: 2026-10-11

package metafetch

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// Review follow-ups of the shared provider-answer cache. Synthetic fixtures
// only ("Title 000123", "Author 07").

// A book's own per-provider row (written by FetchMetadataForBook's context
// search or transcribed-title fallback, or by the interactive search's pooled
// write, from a question outside the walk ladder) still answers after a
// duplicate copy's walk stored every rung of the ladder empty.
func TestWalkSourceChain_AllEmptyLadderDoesNotShadowPerBookRow(t *testing.T) {
	withSharedCache(t, true)
	svc, _ := newPCacheService(t)
	none := &scriptedProvider{id: metadata.SourceIDOpenLibrary}
	sem := NewProviderSemaphore([]metadata.MetadataSource{none}, 2)
	ctx := context.Background()

	_, err := WalkSourceChain(ctx, svc.db, []metadata.MetadataSource{none}, sem, "book-a", "Title 000777", "Author 07", "v1:book-a", 0)
	require.NoError(t, err)
	ladder := none.calls.Load()
	require.Positive(t, ladder, "book-a walks live and stores every rung empty")

	blob, err := json.Marshal([]metadata.BookMetadata{{Title: "Title 000777", Author: "Author 07", ASIN: "B0OWNROW01"}})
	require.NoError(t, err)
	require.NoError(t, database.PutCachedMetadataFetch(svc.db, "book-b", metadata.SourceIDOpenLibrary, "v1:book-b", blob, 0))

	out, err := WalkSourceChain(ctx, svc.db, []metadata.MetadataSource{none}, sem, "book-b", "Title 000777", "Author 07", "v1:book-b", 0)
	require.NoError(t, err)
	require.Len(t, out.Results, 1, "book-b's own per-provider row still answers")
	assert.Equal(t, "B0OWNROW01", out.Results[0].ASIN)
	assert.True(t, out.CacheHit)
	assert.Equal(t, ladder, none.calls.Load(), "and no call is made")

	// A third copy with no row of its own still short-circuits on the empty ladder.
	out, err = WalkSourceChain(ctx, svc.db, []metadata.MetadataSource{none}, sem, "book-c", "Title 000777", "Author 07", "v1:book-c", 0)
	require.NoError(t, err)
	assert.Equal(t, FetchStatusNotFound, out.Status())
	assert.Equal(t, ladder, none.calls.Load())
}

// A shared-cache hit written back to the book's own row keeps the shared
// answer's age: stamping the copy "now" would let the answer outlive its TTL
// by up to the TTL again. A walk answered either way is "from cache".
func TestWriteBackOutcome_SharedHitKeepsAskedAt(t *testing.T) {
	withSharedCache(t, true)
	asked := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	now := asked
	withClock(t, &now)
	svc, _ := newPCacheService(t)
	src := &scriptedProvider{id: metadata.SourceIDAudible, answer: echoAnswer}
	sem := NewProviderSemaphore([]metadata.MetadataSource{src}, 2)
	ctx := context.Background()

	live, err := WalkSourceChain(ctx, svc.db, []metadata.MetadataSource{src}, sem, "book-a", "Title 000123", "Author 07", "v1:book-a", 0)
	require.NoError(t, err)
	assert.False(t, live.FromCache())
	require.NoError(t, WriteBackOutcome(svc.db, "book-a", live))
	rowA, err := database.GetCachedMetadataFetch(svc.db, "book-a", metadata.SourceIDAudible, "v1:book-a")
	require.NoError(t, err)
	require.NotNil(t, rowA, "a live answer is written to the book's own row")

	now = asked.Add(20 * 24 * time.Hour)
	out, err := WalkSourceChain(ctx, svc.db, []metadata.MetadataSource{src}, sem, "book-b", "Title 000123", "Author 07", "v1:book-b", 0)
	require.NoError(t, err)
	require.True(t, out.SharedCacheHit)
	assert.True(t, out.FromCache(), "a shared hit made no call")
	assert.True(t, out.SharedAskedAt.Equal(asked), "the outcome carries the shared answer's age, got %s", out.SharedAskedAt)

	require.NoError(t, WriteBackOutcome(svc.db, "book-b", out))
	row, err := database.GetCachedMetadataFetch(svc.db, "book-b", metadata.SourceIDAudible, "v1:book-b")
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.True(t, row.CachedAt.Equal(asked), "the per-book copy is stamped with the provider's answer time, got %s", row.CachedAt)

	// The book's own row answered: nothing to write back.
	own, err := WalkSourceChain(ctx, svc.db, []metadata.MetadataSource{src}, sem, "book-b", "Title 000123", "Author 07", "v1:book-b", 0)
	require.NoError(t, err)
	require.True(t, own.CacheHit || own.SharedCacheHit)
	assert.True(t, own.FromCache())
}

func TestWriteBackOutcome_PerBookHitWritesNothing(t *testing.T) {
	svc, kv := newPCacheService(t)
	out := ChainOutcome{Results: []metadata.BookMetadata{{Title: "Title 000123"}}, SourceName: "Scripted audible",
		ProviderKey: metadata.SourceIDAudible, SearchIdentity: "v1:book-a", CacheHit: true}
	require.NoError(t, WriteBackOutcome(svc.db, "book-a", out))
	assert.Empty(t, kv.keys("metadata_fetch_cache:"), "the book's own row answered: no write")
	require.NoError(t, WriteBackOutcome(svc.db, "book-a", ChainOutcome{ProviderKey: metadata.SourceIDAudible, SearchIdentity: "v1:book-a"}))
	assert.Empty(t, kv.keys("metadata_fetch_cache:"), "no results: no write")
}
