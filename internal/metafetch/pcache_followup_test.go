// file: internal/metafetch/pcache_followup_test.go
// version: 1.3.0
// guid: e971d44a-1aa0-4ff3-8223-e9a5a43c932b
// last-edited: 2026-10-11

package metafetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
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

// A legacy per-variant row copied into the shared row keeps the legacy row's
// age: the copy must expire when the original answer would have.
func TestPCache_LegacyCopyKeepsOriginalAskedAt(t *testing.T) {
	withSharedCache(t, true)
	now := time.Now().UTC().Truncate(time.Second)
	withClock(t, &now)
	svc, kv := newPCacheService(t)
	src := &scriptedProvider{id: metadata.SourceIDAudible, answer: echoAnswer}
	v := titleAuthor("Title 000601", "Author 07")
	legacyAt := now.Add(-10 * 24 * time.Hour)
	blob, err := json.Marshal([]metadata.BookMetadata{{Title: "Title 000601", Author: "Author 07", ASIN: "B0LEGACY02"}})
	require.NoError(t, err)
	require.NoError(t, database.PutCachedMetadataFetchAt(svc.db, "book-a", variantCacheSource(src, v), "v1:book-a", blob, 0, legacyAt))

	st := askFor(svc, src, "book-a", v, SearchOptions{})
	require.Zero(t, src.calls.Load(), "the legacy row answers")
	require.Len(t, st.results, 1)
	keys := kv.keys(database.PCacheQuestionPrefix)
	require.Len(t, keys, 1, "the legacy answer is copied to the shared row")
	var e database.PCacheEntry
	require.NoError(t, json.Unmarshal(kv.m[keys[0]], &e))
	assert.True(t, e.AskedAt.Equal(legacyAt), "the copy keeps the legacy row's age: asked_at %s, want %s", e.AskedAt, legacyAt)
}

// A non-empty answer is replayed for metadata_fetch_cache_ttl_days and asked
// again after it.
func TestPCache_PositiveAnswerAgesOutAfterFetchCacheTTL(t *testing.T) {
	withSharedCache(t, true) // 180 days
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	withClock(t, &now)
	svc, _ := newPCacheService(t)
	src := &scriptedProvider{id: metadata.SourceIDAudible, answer: echoAnswer}
	v := titleAuthor("Title 000602", "Author 07")

	askFor(svc, src, "book-a", v, SearchOptions{})
	now = now.Add(179 * 24 * time.Hour)
	askFor(svc, src, "book-b", v, SearchOptions{})
	require.EqualValues(t, 1, src.calls.Load(), "inside 180 days the answer is replayed")
	now = now.Add(2 * 24 * time.Hour) // 181 days after the ask
	askFor(svc, src, "book-c", v, SearchOptions{})
	assert.EqualValues(t, 2, src.calls.Load(), "past metadata_fetch_cache_ttl_days the question is asked again")
}

// Negative TTLs are per provider: on day 15 an empty Audible answer (14 days)
// is asked again while an empty Open Library answer (30 days) is replayed.
func TestPCache_NegativeTTLIsPerProvider(t *testing.T) {
	withSharedCache(t, true)
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	withClock(t, &now)
	svc, _ := newPCacheService(t)
	aud := &scriptedProvider{id: metadata.SourceIDAudible}
	ol := &scriptedProvider{id: metadata.SourceIDOpenLibrary}
	v := titleAuthor("Title 000603", "Author 07")

	askFor(svc, aud, "book-a", v, SearchOptions{})
	askFor(svc, ol, "book-a", v, SearchOptions{})
	now = now.Add(15 * 24 * time.Hour)
	askFor(svc, aud, "book-b", v, SearchOptions{})
	askFor(svc, ol, "book-b", v, SearchOptions{})
	assert.EqualValues(t, 2, aud.calls.Load(), "Audible's 14-day negative TTL has run out on day 15")
	assert.EqualValues(t, 1, ol.calls.Load(), "Open Library's 30-day negative TTL has not")
	now = now.Add(16 * 24 * time.Hour) // day 31
	askFor(svc, ol, "book-c", v, SearchOptions{})
	assert.EqualValues(t, 2, ol.calls.Load(), "and runs out after 30 days")
}

// One fan-out asks several sources concurrently in the same round, and every
// one of them records its key on the search's shared pointer collector.
// Meaningful under -race.
func TestPCache_ConcurrentFanoutRecordsEveryKey(t *testing.T) {
	withSharedCache(t, true)
	svc, _ := newPCacheService(t)
	srcs := []metadata.MetadataSource{
		&scriptedProvider{id: metadata.SourceIDAudible},
		&scriptedProvider{id: metadata.SourceIDGoogleBooks},
		&scriptedProvider{id: metadata.SourceIDOpenLibrary},
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			book := fmt.Sprintf("book-%d", i)
			vs := []queryVariant{titleAuthor(fmt.Sprintf("Title %06d", 700+i%3), "Author 07")}
			svc.runSearchFanout(fanoutParams{ctx: context.Background(), bookID: book, identity: "v1:" + book}, srcs, vs)
		}(i)
	}
	wg.Wait()
	for i := 0; i < 8; i++ {
		pointed, err := database.PCachePointedKeys(svc.db, fmt.Sprintf("book-%d", i))
		require.NoError(t, err)
		assert.Len(t, pointed, len(srcs), "book-%d: every source's key is on the pointer row", i)
	}
}

// identitySearcher is a counting fake of the Audible identity search.
type identitySearcher struct {
	calls atomic.Int64
	err   error
}

func (f *identitySearcher) SearchIdentities(_ context.Context, q metadata.AudibleIdentityQuery) ([]metadata.AudibleIdentity, error) {
	f.calls.Add(1)
	if f.err != nil {
		return nil, f.err
	}
	return []metadata.AudibleIdentity{{ASIN: "B0SYNTH010", Title: q.Title}}, nil
}

// The identity-search key hashes everything the call sends: two ISBN-only
// searches differ only in their keywords, and the result count is sent too.
func TestPCache_CachedIdentitySearchKeyIncludesKeywordsAndCount(t *testing.T) {
	withSharedCache(t, true)
	svc, _ := newPCacheService(t)
	fa := &identitySearcher{}
	ctx := context.Background()
	ask := func(q metadata.AudibleIdentityQuery) {
		_, err := CachedIdentitySearch(ctx, svc.db, "book-a", fa, q, IdentitySearchCache{})
		require.NoError(t, err)
	}
	ask(metadata.AudibleIdentityQuery{Keywords: "9780000000001", NumResults: 10})
	ask(metadata.AudibleIdentityQuery{Keywords: "9780000000002", NumResults: 10})
	assert.EqualValues(t, 2, fa.calls.Load(), "different keywords are different questions")
	ask(metadata.AudibleIdentityQuery{Keywords: "9780000000001", NumResults: 25})
	assert.EqualValues(t, 3, fa.calls.Load(), "a different result count is a different question")
	ask(metadata.AudibleIdentityQuery{Keywords: "9780000000001", NumResults: 10})
	assert.EqualValues(t, 3, fa.calls.Load(), "the same question is replayed")
}

// CachedIdentitySearch records its key on the book's pointer row, so the
// reject path's bust reaches it and the next search asks live.
func TestPCache_CachedIdentitySearchPointerAndRejectBust(t *testing.T) {
	withSharedCache(t, true)
	svc, _ := newPCacheService(t)
	fa := &identitySearcher{}
	ctx := context.Background()
	q := metadata.AudibleIdentityQuery{Title: "Title 000604", Author: "Author 07", NumResults: 25}

	_, err := CachedIdentitySearch(ctx, svc.db, "book-a", fa, q, IdentitySearchCache{})
	require.NoError(t, err)
	pointed, err := database.PCachePointedKeys(svc.db, "book-a")
	require.NoError(t, err)
	require.Len(t, pointed[metadata.SourceIDAudible], 1, "the identity key is on the book's pointer row")

	require.NoError(t, database.InvalidateAllCachedMetadataFetchesForBook(svc.db, "book-a"))
	_, err = CachedIdentitySearch(ctx, svc.db, "book-b", fa, q, IdentitySearchCache{})
	require.NoError(t, err)
	assert.EqualValues(t, 2, fa.calls.Load(), "the reject bust deleted the shared answer: the next asker asks live")

	// ReadOnly writes neither the answer nor the pointer row.
	_, err = CachedIdentitySearch(ctx, svc.db, "book-c", fa, metadata.AudibleIdentityQuery{Title: "Title 000605", Author: "Author 07"}, IdentitySearchCache{ReadOnly: true})
	require.NoError(t, err)
	pointed, err = database.PCachePointedKeys(svc.db, "book-c")
	require.NoError(t, err)
	assert.Empty(t, pointed)
}

// "Search again" (BypassFetchCache) deletes the shared answers the book's
// pointer row lists BEFORE it asks: otherwise an empty fresh answer could not
// replace the stale found one (an empty never replaces a fresh positive), and
// every other book asking the question would keep the stale answer.
func TestPCache_SearchAgainBustsBeforeTheLiveCall(t *testing.T) {
	withSharedCache(t, true)
	svc, _ := newPCacheService(t)
	var dropped atomic.Bool
	src := &scriptedProvider{id: metadata.SourceIDAudible, answer: func(title, author string) []metadata.BookMetadata {
		if dropped.Load() {
			return nil
		}
		return echoAnswer(title, author)
	}}
	v := titleAuthor("Title 000606", "Author 07")
	search := func(bookID string, opts SearchOptions) {
		svc.runSearchFanout(fanoutParams{ctx: context.Background(), bookID: bookID, identity: "v1:" + bookID, opts: opts},
			[]metadata.MetadataSource{src}, []queryVariant{v})
	}

	search("book-a", SearchOptions{})
	dropped.Store(true) // the provider no longer carries the title
	search("book-a", SearchOptions{BypassFetchCache: true})
	calls := src.calls.Load()

	b := askFor(svc, src, "book-b", v, SearchOptions{})
	assert.Equal(t, calls, src.calls.Load(), "book-b is answered from the shared row")
	assert.Empty(t, b.results, "the forced search replaced the stale found answer with the provider's empty one")
}

// A failed product lookup (an error, not the provider's "no such ASIN") is
// never stored, not even as an empty negative entry: the next search asks
// again.
func TestCachedLookupASIN_ErrorNeverCached(t *testing.T) {
	withSharedCache(t, true)
	svc, kv := newPCacheService(t)
	var calls atomic.Int64
	svc.asinLookupOverride = func(context.Context, string, string) (*metadata.BookMetadata, error) {
		calls.Add(1)
		return nil, errors.New("503 from the provider")
	}
	p := fanoutParams{ctx: context.Background(), bookID: "book-a", identity: "v1:book-a"}
	_, err := svc.cachedLookupASIN(p, metadata.SourceIDAudible, "B0SYNTH005")
	require.Error(t, err)
	assert.Empty(t, kv.keys(database.PCachePrefix), "a failed lookup writes 0 pcache: rows")
	_, err = svc.cachedLookupASIN(p, metadata.SourceIDAudible, "B0SYNTH005")
	require.Error(t, err)
	assert.EqualValues(t, 2, calls.Load(), "the next search asks again")
}
