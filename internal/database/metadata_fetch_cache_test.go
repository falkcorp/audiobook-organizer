// file: internal/database/metadata_fetch_cache_test.go
// version: 1.4.0
// guid: 6f5e4d3c-2b1a-0f9e-8d7c-6b5a4f3e2d1c
// last-edited: 2026-09-14

package database

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newCacheTestStore returns a :memory: SQLiteStore for the
// metadata fetch cache tests. The cache only needs the raw
// kv_store table (created lazily by SetRaw/GetRaw), so we
// don't need to run migrations.
func newCacheTestStore(t *testing.T) *PebbleStore {
	t.Helper()
	dbPath := t.TempDir()
	store, err := NewPebbleStore(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// TestMetadataFetchCache_RoundTrip verifies basic put/get
// semantics. Put a payload, read it back, compare.
func TestMetadataFetchCache_RoundTrip(t *testing.T) {
	store := newCacheTestStore(t)

	payload := json.RawMessage(`[{"title":"Dune","author":"Frank Herbert"}]`)
	if err := PutCachedMetadataFetch(store, "book-1", "Hardcover", testFetchIdentity, payload, 0.95); err != nil {
		t.Fatalf("put: %v", err)
	}
	entry, err := GetCachedMetadataFetch(store, "book-1", "Hardcover", testFetchIdentity)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if entry == nil {
		t.Fatal("expected cache hit, got nil")
	}
	if string(entry.Results) != string(payload) {
		t.Errorf("results = %s, want %s", entry.Results, payload)
	}
	if entry.BestScore != 0.95 {
		t.Errorf("best_score = %v, want 0.95", entry.BestScore)
	}
	// Source is stored lowercased.
	if entry.Source != "hardcover" {
		t.Errorf("source = %q, want 'hardcover'", entry.Source)
	}
}

// TestMetadataFetchCache_Miss confirms nil+nil on miss so
// callers can branch without a sentinel error.
func TestMetadataFetchCache_Miss(t *testing.T) {
	store := newCacheTestStore(t)

	entry, err := GetCachedMetadataFetch(store, "book-unknown", "Hardcover", testFetchIdentity)
	if err != nil {
		t.Fatalf("unexpected error on miss: %v", err)
	}
	if entry != nil {
		t.Errorf("expected nil entry on miss, got %+v", entry)
	}
}

// TestMetadataFetchCache_CaseInsensitiveSource locks in that
// "Hardcover" and "hardcover" resolve to the same cache row.
// Drift here would silently duplicate cache entries.
func TestMetadataFetchCache_CaseInsensitiveSource(t *testing.T) {
	store := newCacheTestStore(t)

	payload := json.RawMessage(`[]`)
	if err := PutCachedMetadataFetch(store, "book-1", "Hardcover", testFetchIdentity, payload, 0); err != nil {
		t.Fatal(err)
	}
	entry, err := GetCachedMetadataFetch(store, "book-1", "hardcover", testFetchIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if entry == nil {
		t.Fatal("expected hit when source case differs")
	}
}

// TestMetadataFetchCache_Invalidate confirms per-(book, source)
// invalidation works.
func TestMetadataFetchCache_Invalidate(t *testing.T) {
	store := newCacheTestStore(t)

	_ = PutCachedMetadataFetch(store, "book-1", "Hardcover", testFetchIdentity, json.RawMessage(`[]`), 0)
	if err := InvalidateCachedMetadataFetch(store, "book-1", "Hardcover"); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	entry, _ := GetCachedMetadataFetch(store, "book-1", "Hardcover", testFetchIdentity)
	if entry != nil {
		t.Errorf("expected miss after invalidate, got hit")
	}
}

// TestMetadataFetchCache_InvalidateAllForBook wipes every
// source entry for a book without touching other books.
func TestMetadataFetchCache_InvalidateAllForBook(t *testing.T) {
	store := newCacheTestStore(t)

	_ = PutCachedMetadataFetch(store, "book-1", "Hardcover", testFetchIdentity, json.RawMessage(`[]`), 0)
	_ = PutCachedMetadataFetch(store, "book-1", "Audible", testFetchIdentity, json.RawMessage(`[]`), 0)
	_ = PutCachedMetadataFetch(store, "book-2", "Hardcover", testFetchIdentity, json.RawMessage(`[]`), 0)

	if err := InvalidateAllCachedMetadataFetchesForBook(store, "book-1"); err != nil {
		t.Fatalf("invalidate all: %v", err)
	}

	// book-1 entries gone.
	if e, _ := GetCachedMetadataFetch(store, "book-1", "Hardcover", testFetchIdentity); e != nil {
		t.Error("book-1 Hardcover should be gone")
	}
	if e, _ := GetCachedMetadataFetch(store, "book-1", "Audible", testFetchIdentity); e != nil {
		t.Error("book-1 Audible should be gone")
	}
	// book-2 entry still there.
	if e, _ := GetCachedMetadataFetch(store, "book-2", "Hardcover", testFetchIdentity); e == nil {
		t.Error("book-2 Hardcover should have survived")
	}
}

// TestMetadataFetchCache_CorruptEntry_TreatedAsMiss verifies
// that a garbage cache value is transparently treated as a
// miss instead of a hard error, and the corrupt row is
// cleaned up so the next write lands in a clean slot.
func TestMetadataFetchCache_CorruptEntry_TreatedAsMiss(t *testing.T) {
	store := newCacheTestStore(t)

	// Hand-poison a cache key with non-JSON bytes.
	_ = store.SetRaw(metadataFetchCacheKey("book-1", "Hardcover"), []byte("not json"))

	entry, err := GetCachedMetadataFetch(store, "book-1", "Hardcover", testFetchIdentity)
	if err != nil {
		t.Fatalf("unexpected error on corrupt entry: %v", err)
	}
	if entry != nil {
		t.Errorf("expected nil entry on corrupt row, got %+v", entry)
	}
	// The corrupt row should have been cleaned up during the read.
	raw, _ := store.GetRaw(metadataFetchCacheKey("book-1", "Hardcover"))
	if raw != nil {
		t.Errorf("expected corrupt entry to be cleaned up, still got %d bytes", len(raw))
	}
}

// TestMetadataFetchCache_TTL_ZeroMeansInfinite verifies that maxAge=0 disables
// the TTL check so even a very old entry is returned as a hit.
func TestMetadataFetchCache_TTL_ZeroMeansInfinite(t *testing.T) {
	store := newCacheTestStore(t)

	payload := json.RawMessage(`[{"title":"Old Book"}]`)
	require.NoError(t, PutCachedMetadataFetch(store, "book-1", "Audible", testFetchIdentity, payload, 0.80))

	// Backdate the entry by 1 year by overwriting with a manipulated timestamp.
	key := metadataFetchCacheKey("book-1", "audible")
	blob, err := store.GetRaw(key)
	require.NoError(t, err)
	require.NotNil(t, blob)
	var entry CachedMetadataEntry
	require.NoError(t, json.Unmarshal(blob, &entry))
	entry.CachedAt = time.Now().UTC().Add(-365 * 24 * time.Hour)
	updated, err := json.Marshal(entry)
	require.NoError(t, err)
	require.NoError(t, store.SetRaw(key, updated))

	// maxAge=0 → infinite TTL, must return hit.
	got, hit, err := GetCachedMetadataFetchWithMaxAge(store, "book-1", "Audible", testFetchIdentity, 0)
	require.NoError(t, err)
	require.True(t, hit, "expected hit when maxAge=0")
	require.NotNil(t, got)
}

// TestMetadataFetchCache_TTL_ExpiredReturnsMiss verifies that an entry older
// than maxAge is returned as a miss.
func TestMetadataFetchCache_TTL_ExpiredReturnsMiss(t *testing.T) {
	store := newCacheTestStore(t)

	payload := json.RawMessage(`[{"title":"Old Book"}]`)
	require.NoError(t, PutCachedMetadataFetch(store, "book-2", "Audnexus", testFetchIdentity, payload, 0.70))

	// Backdate to 8 days ago.
	key := metadataFetchCacheKey("book-2", "audnexus")
	blob, err := store.GetRaw(key)
	require.NoError(t, err)
	require.NotNil(t, blob)
	var entry CachedMetadataEntry
	require.NoError(t, json.Unmarshal(blob, &entry))
	entry.CachedAt = time.Now().UTC().Add(-8 * 24 * time.Hour)
	updated, err := json.Marshal(entry)
	require.NoError(t, err)
	require.NoError(t, store.SetRaw(key, updated))

	// maxAge=7d → entry is expired, must return miss.
	got, hit, err := GetCachedMetadataFetchWithMaxAge(store, "book-2", "Audnexus", testFetchIdentity, 7*24*time.Hour)
	require.NoError(t, err)
	require.False(t, hit, "expected miss for expired entry")
	require.Nil(t, got)
}

// TestMetadataFetchCache_TTL_FreshReturnsHit verifies that an entry within
// maxAge is returned as a hit.
func TestMetadataFetchCache_TTL_FreshReturnsHit(t *testing.T) {
	store := newCacheTestStore(t)

	payload := json.RawMessage(`[{"title":"Fresh Book"}]`)
	require.NoError(t, PutCachedMetadataFetch(store, "book-3", "OpenLibrary", testFetchIdentity, payload, 0.90))

	// Backdate to 1 day ago (within a 7d maxAge).
	key := metadataFetchCacheKey("book-3", "openlibrary")
	blob, err := store.GetRaw(key)
	require.NoError(t, err)
	require.NotNil(t, blob)
	var entry CachedMetadataEntry
	require.NoError(t, json.Unmarshal(blob, &entry))
	entry.CachedAt = time.Now().UTC().Add(-1 * 24 * time.Hour)
	updated, err := json.Marshal(entry)
	require.NoError(t, err)
	require.NoError(t, store.SetRaw(key, updated))

	// maxAge=7d → entry is fresh, must return hit.
	got, hit, err := GetCachedMetadataFetchWithMaxAge(store, "book-3", "OpenLibrary", testFetchIdentity, 7*24*time.Hour)
	require.NoError(t, err)
	require.True(t, hit, "expected hit for fresh entry")
	require.NotNil(t, got)
	if string(got.Results) != string(payload) {
		t.Errorf("results = %s, want %s", got.Results, payload)
	}
}
