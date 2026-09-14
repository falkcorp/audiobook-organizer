// file: internal/database/metadata_fetch_cache_identity_test.go
// version: 1.0.0
// guid: 3c7f2a91-5e4b-4d08-9a6c-e1b8d2f40a57
// last-edited: 2026-09-14

package database

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// testFetchIdentity is the identity the shared cache tests stamp and read
// with. Any non-empty value works; the identity recipe is tested below.
const testFetchIdentity = "v1:test-identity"

// TestMetadataSearchIdentity_Recipe pins what the stamp depends on: title,
// author, ASIN and both ISBNs change it; case and surrounding whitespace do not.
func TestMetadataSearchIdentity_Recipe(t *testing.T) {
	base := MetadataSearchIdentity("The Way of Kings", "Brandon Sanderson", strp("B003P2WO5E"), strp("9780765326355"), nil)
	require.NotEmpty(t, base)
	require.Equal(t, base, MetadataSearchIdentity("  the way of KINGS ", "brandon sanderson", strp("b003p2wo5e"), strp("9780765326355"), nil),
		"normalization must not change the stamp")

	for name, other := range map[string]string{
		"title":  MetadataSearchIdentity("Words of Radiance", "Brandon Sanderson", strp("B003P2WO5E"), strp("9780765326355"), nil),
		"author": MetadataSearchIdentity("The Way of Kings", "Somebody Else", strp("B003P2WO5E"), strp("9780765326355"), nil),
		"asin":   MetadataSearchIdentity("The Way of Kings", "Brandon Sanderson", strp("B00OTHER00"), strp("9780765326355"), nil),
		"isbn13": MetadataSearchIdentity("The Way of Kings", "Brandon Sanderson", strp("B003P2WO5E"), nil, nil),
		"isbn10": MetadataSearchIdentity("The Way of Kings", "Brandon Sanderson", strp("B003P2WO5E"), strp("9780765326355"), strp("0765326353")),
	} {
		require.NotEqual(t, base, other, "changing %s must change the stamp", name)
	}
}

// TestMetadataFetchCache_IdentityMismatchIsMiss: a row fetched for one
// identity is not returned for another, and IS returned for its own.
func TestMetadataFetchCache_IdentityMismatchIsMiss(t *testing.T) {
	store := newCacheTestStore(t)
	garbage := MetadataSearchIdentity("01 - track", "", nil, nil, nil)
	fixed := MetadataSearchIdentity("The Way of Kings", "Brandon Sanderson", nil, nil, nil)
	require.NoError(t, PutCachedMetadataFetch(store, "b1", "audible", garbage, json.RawMessage(`[{"title":"Wrong"}]`), 0))

	got, hit, err := CachedMetadataForProvider(store, "b1", "audible", "Audible", fixed, time.Hour)
	require.NoError(t, err)
	require.False(t, hit, "a row fetched for another identity must be a miss")
	require.Nil(t, got)

	got, hit, err = CachedMetadataForProvider(store, "b1", "audible", "Audible", garbage, time.Hour)
	require.NoError(t, err)
	require.True(t, hit, "a row read with its own identity must still hit")
	require.NotNil(t, got)

	got, hit, err = CachedMetadataForProvider(store, "b1", "audible", "Audible", "", time.Hour)
	require.NoError(t, err)
	require.False(t, hit, "an empty caller identity must match nothing")
	require.Nil(t, got)
}

// TestMetadataFetchCache_LegacyRowWithoutIdentityIsMiss: rows written before
// the stamp existed stay readable (no error, no corruption cleanup) but are
// never hits, under both the id key and the legacy display-name key.
func TestMetadataFetchCache_LegacyRowWithoutIdentityIsMiss(t *testing.T) {
	store := newCacheTestStore(t)
	legacy, err := json.Marshal(map[string]any{
		"book_id": "b1", "source": "audible", "results": json.RawMessage(`[{"title":"Wrong"}]`),
		"best_score": 1.0, "cached_at": time.Now().UTC(),
	})
	require.NoError(t, err)
	require.NoError(t, store.SetRaw(metadataFetchCacheKey("b1", "audible"), legacy))
	require.NoError(t, store.SetRaw(metadataFetchCacheKey("b1", "Audible Legacy"), legacy))

	ident := MetadataSearchIdentity("The Way of Kings", "Brandon Sanderson", nil, nil, nil)
	got, hit, err := CachedMetadataForProvider(store, "b1", "audible", "Audible Legacy", ident, 0)
	require.NoError(t, err)
	require.False(t, hit)
	require.Nil(t, got)

	// The legacy row is left in place (it is not corrupt) for the write-back
	// to overwrite.
	raw, err := store.GetRaw(metadataFetchCacheKey("b1", "audible"))
	require.NoError(t, err)
	require.NotNil(t, raw)
}

// writeLegacyFetchRow writes a pre-stamp row (no search_identity) cached `age` ago.
func writeLegacyFetchRow(t *testing.T, store RawKVStore, bookID, source string, age time.Duration) {
	t.Helper()
	legacy, err := json.Marshal(map[string]any{
		"book_id": bookID, "source": source, "results": json.RawMessage(`[{"title":"Wrong"}]`),
		"best_score": 1.0, "cached_at": time.Now().UTC().Add(-age),
	})
	require.NoError(t, err)
	require.NoError(t, store.SetRaw(metadataFetchCacheKey(bookID, source), legacy))
}

// TestCachedMetadataCoversSkip_LegacyRowCountsAsCached: the A3#14 rollout rule.
// The bulk skip probe treats an unstamped row as cached (no mass refetch on
// deploy), under both the id key and the legacy display-name key, while the
// TTL still ages it out.
func TestCachedMetadataCoversSkip_LegacyRowCountsAsCached(t *testing.T) {
	store := newCacheTestStore(t)
	ident := MetadataSearchIdentity("The Way of Kings", "Brandon Sanderson", nil, nil, nil)

	writeLegacyFetchRow(t, store, "b1", "audible", time.Hour)
	require.True(t, CachedMetadataCoversSkip(store, "b1", "audible", "Audible", ident, 7*24*time.Hour),
		"a fresh unstamped row must count as cached for the bulk skip")

	writeLegacyFetchRow(t, store, "b2", "Audible Legacy", time.Hour)
	require.True(t, CachedMetadataCoversSkip(store, "b2", "audible", "Audible Legacy", ident, 7*24*time.Hour),
		"an unstamped row under the legacy display-name key must count too")

	writeLegacyFetchRow(t, store, "b3", "audible", 8*24*time.Hour)
	require.False(t, CachedMetadataCoversSkip(store, "b3", "audible", "Audible", ident, 7*24*time.Hour),
		"the TTL still applies to unstamped rows")

	require.False(t, CachedMetadataCoversSkip(store, "b4", "audible", "Audible", ident, 7*24*time.Hour),
		"no row at all is not cached")
}

// TestCachedMetadataCoversSkip_MismatchedStampIsNotCached: a row stamped for a
// different identity is a real identity change, so the bulk skip must NOT skip
// the book; a matching stamp is skipped.
func TestCachedMetadataCoversSkip_MismatchedStampIsNotCached(t *testing.T) {
	store := newCacheTestStore(t)
	garbage := MetadataSearchIdentity("01 - track", "", nil, nil, nil)
	fixed := MetadataSearchIdentity("The Way of Kings", "Brandon Sanderson", nil, nil, nil)
	require.NoError(t, PutCachedMetadataFetch(store, "b1", "audible", garbage, json.RawMessage(`[{"title":"Wrong"}]`), 0))

	require.False(t, CachedMetadataCoversSkip(store, "b1", "audible", "Audible", fixed, 7*24*time.Hour),
		"a row stamped for another identity must not let the bulk run skip the book")
	require.True(t, CachedMetadataCoversSkip(store, "b1", "audible", "Audible", garbage, 7*24*time.Hour),
		"a row stamped for the current identity is cached")
}

// TestCachedMetadataForProvider_LegacyRowStillNotReplayed: the skip allowance
// must not leak into the replay read. The same unstamped row the bulk skip
// accepts is still a miss for CachedMetadataForProvider, so a fetch re-queries.
func TestCachedMetadataForProvider_LegacyRowStillNotReplayed(t *testing.T) {
	store := newCacheTestStore(t)
	ident := MetadataSearchIdentity("The Way of Kings", "Brandon Sanderson", nil, nil, nil)
	writeLegacyFetchRow(t, store, "b1", "audible", time.Hour)

	require.True(t, CachedMetadataCoversSkip(store, "b1", "audible", "Audible", ident, 7*24*time.Hour))
	got, hit, err := CachedMetadataForProvider(store, "b1", "audible", "Audible", ident, 7*24*time.Hour)
	require.NoError(t, err)
	require.False(t, hit, "an unstamped row must never be replayed")
	require.Nil(t, got)
}

// TestPutCachedMetadataFetch_RequiresIdentity: an unstampable row is refused
// rather than written as a row nothing can ever read back.
func TestPutCachedMetadataFetch_RequiresIdentity(t *testing.T) {
	store := newCacheTestStore(t)
	err := PutCachedMetadataFetch(store, "b1", "audible", "", json.RawMessage(`[]`), 0)
	require.True(t, errors.Is(err, ErrNoSearchIdentity), "got %v", err)
	raw, gerr := store.GetRaw(metadataFetchCacheKey("b1", "audible"))
	require.NoError(t, gerr)
	require.Nil(t, raw)
}
