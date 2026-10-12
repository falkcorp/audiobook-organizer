// file: internal/database/metadata_fetch_cache_pcache_test.go
// version: 1.0.0
// guid: 2c8f6a17-9d43-4e0b-b71a-5e3d90c4a2f8
// last-edited: 2026-10-11

package database

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newPCacheTestStore(t *testing.T) *PebbleStore {
	t.Helper()
	s, err := NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestPCache_KeysAreProviderPrefixed(t *testing.T) {
	k := PCacheQuestionKey("Audible", "search_title", "v1\x1et=title 000123")
	assert.True(t, strings.HasPrefix(k, PCacheQuestionProviderPrefix("audible")), k)
	assert.NotEqual(t, k, PCacheQuestionKey("audible", "search_title", "v1\x1et=title 000124"))
	assert.Equal(t, "pcache:asin:audible:B0SYNTH001", PCacheASINKey("audible", " b0synth001 ", ""))
	assert.Equal(t, "pcache:asin:audnexus:B0SYNTH001", PCacheASINKey("audnexus", "B0SYNTH001", ""))
	assert.Equal(t, "pcache:asin:audible:B0SYNTH001@api.example.invalid", PCacheASINKey("audible", "B0SYNTH001", "api.example.invalid"))
	assert.Equal(t, "pcache:isbn:hardcover:978000000000X", PCacheISBNKey("hardcover", "978-0-00-000000-x", ""))
	assert.Equal(t, "pcache:q:a_b:c_d:", PCacheQuestionKey("a:b", "c d", "")[:len("pcache:q:a_b:c_d:")])
}

func TestPCache_PositiveAndNegativeTTL(t *testing.T) {
	s := newPCacheTestStore(t)
	t0 := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	ttl := PCacheTTL{Positive: 180 * 24 * time.Hour, Negative: 14 * 24 * time.Hour}

	ok, err := PutPCache(s, "pcache:q:audible:search_title:pos", PCacheEntry{Provider: "audible", Results: json.RawMessage(`[{"title":"Title 000123"}]`)}, ttl, t0)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = PutPCache(s, "pcache:q:audible:search_title:neg", PCacheEntry{Provider: "audible", Empty: true}, ttl, t0)
	require.NoError(t, err)
	require.True(t, ok)

	e, _, err := GetPCache(s, "pcache:q:audible:search_title:neg", ttl, t0.Add(13*24*time.Hour))
	require.NoError(t, err)
	require.NotNil(t, e)
	assert.True(t, e.Empty)
	assert.JSONEq(t, `[]`, string(e.Results))

	e, reason, err := GetPCache(s, "pcache:q:audible:search_title:neg", ttl, t0.Add(15*24*time.Hour))
	require.NoError(t, err)
	assert.Nil(t, e)
	assert.Equal(t, PCacheMissExpired, reason)
	raw, err := s.GetRaw("pcache:q:audible:search_title:neg")
	require.NoError(t, err)
	assert.NotNil(t, raw, "an expired entry is not deleted on read")

	e, _, err = GetPCache(s, "pcache:q:audible:search_title:pos", ttl, t0.Add(15*24*time.Hour))
	require.NoError(t, err)
	assert.NotNil(t, e, "a positive entry lives for the positive TTL")

	e, reason, err = GetPCache(s, "pcache:q:audible:search_title:neg", PCacheTTL{Positive: ttl.Positive}, t0)
	require.NoError(t, err)
	assert.Nil(t, e, "negative caching off: an empty entry is never served")
	assert.Equal(t, PCacheMissNegativeOff, reason)

	_, reason, err = GetPCache(s, "pcache:q:audible:search_title:none", ttl, t0)
	require.NoError(t, err)
	assert.Equal(t, PCacheMissNotFound, reason)
}

func TestPCache_EmptyNeverReplacesFreshAnswerAndOffWritesNothing(t *testing.T) {
	s := newPCacheTestStore(t)
	t0 := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	ttl := PCacheTTL{Positive: 30 * 24 * time.Hour, Negative: 14 * 24 * time.Hour}
	key := "pcache:asin:audible:B0SYNTH001"

	ok, err := PutPCache(s, key, PCacheEntry{Results: json.RawMessage(`[{"asin":"B0SYNTH001"}]`)}, ttl, t0)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = PutPCache(s, key, PCacheEntry{Empty: true}, ttl, t0.Add(time.Hour))
	require.NoError(t, err)
	assert.False(t, ok, "an empty answer does not replace a fresh found one")
	e, _, err := GetPCache(s, key, ttl, t0.Add(time.Hour))
	require.NoError(t, err)
	require.NotNil(t, e)
	assert.False(t, e.Empty)

	ok, err = PutPCache(s, key, PCacheEntry{Empty: true}, ttl, t0.Add(31*24*time.Hour))
	require.NoError(t, err)
	assert.True(t, ok, "once the found answer expired, the empty one is stored")

	ok, err = PutPCache(s, "pcache:q:hardcover:search_title:x", PCacheEntry{Empty: true}, PCacheTTL{Positive: ttl.Positive}, t0)
	require.NoError(t, err)
	assert.False(t, ok, "negative caching off: nothing is written")
}

func TestPCache_PointerUnionCapAndBust(t *testing.T) {
	s := newPCacheTestStore(t)
	t0 := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	ttl := PCacheTTL{Positive: time.Hour, Negative: time.Hour}
	for i := 0; i < 3; i++ {
		_, err := PutPCache(s, fmt.Sprintf("pcache:q:audible:search_title:%d", i), PCacheEntry{Results: json.RawMessage(`[1]`)}, ttl, t0)
		require.NoError(t, err)
	}
	_, err := PutPCache(s, "pcache:q:openlibrary:search_title:0", PCacheEntry{Results: json.RawMessage(`[1]`)}, ttl, t0)
	require.NoError(t, err)

	require.NoError(t, RecordPCachePointer(s, "book-a", map[string][]string{"audible": {"pcache:q:audible:search_title:0", "pcache:q:audible:search_title:1"}}, t0))
	require.NoError(t, RecordPCachePointer(s, "book-a", map[string][]string{
		"audible":     {"pcache:q:audible:search_title:2", "pcache:q:audible:search_title:0"},
		"openlibrary": {"pcache:q:openlibrary:search_title:0"},
	}, t0))
	keys, err := PCachePointedKeys(s, "book-a")
	require.NoError(t, err)
	assert.Equal(t, []string{"pcache:q:audible:search_title:1", "pcache:q:audible:search_title:2", "pcache:q:audible:search_title:0"},
		keys["audible"], "a union, newest last")

	// A bust limited to one provider leaves the other's rows and pointer.
	n, err := DeletePCacheForBook(s, "book-a", []string{"audible"})
	require.NoError(t, err)
	assert.Equal(t, 3, n)
	left, err := s.ScanPrefix(PCacheQuestionPrefix)
	require.NoError(t, err)
	require.Len(t, left, 1)
	assert.Equal(t, "pcache:q:openlibrary:search_title:0", left[0].Key)
	keys, err = PCachePointedKeys(s, "book-a")
	require.NoError(t, err)
	assert.NotContains(t, keys, "audible")

	// The reject-path invalidation busts the rest and removes the pointer.
	require.NoError(t, InvalidateAllCachedMetadataFetchesForBook(s, "book-a"))
	left, err = s.ScanPrefix(PCachePrefix)
	require.NoError(t, err)
	assert.Empty(t, left)
	raw, err := s.GetRaw(pcachePointerKey("book-a"))
	require.NoError(t, err)
	assert.Nil(t, raw)

	// The cap keeps the newest keys.
	many := make([]string, 0, pcachePointerMaxPerProvider+5)
	for i := 0; i < pcachePointerMaxPerProvider+5; i++ {
		many = append(many, fmt.Sprintf("pcache:q:audible:search_title:k%03d", i))
	}
	require.NoError(t, RecordPCachePointer(s, "book-b", map[string][]string{"audible": many}, t0))
	keys, err = PCachePointedKeys(s, "book-b")
	require.NoError(t, err)
	require.Len(t, keys["audible"], pcachePointerMaxPerProvider)
	assert.Equal(t, many[len(many)-1], keys["audible"][pcachePointerMaxPerProvider-1])
}

// A pointer row that names a key outside the pcache: family never deletes it.
func TestPCache_BustNeverLeavesTheFamily(t *testing.T) {
	s := newPCacheTestStore(t)
	require.NoError(t, s.SetRaw("setting:synthetic", []byte("keep")))
	require.NoError(t, RecordPCachePointer(s, "book-a", map[string][]string{"audible": {"setting:synthetic"}}, time.Now()))
	n, err := DeletePCacheForBook(s, "book-a", nil)
	require.NoError(t, err)
	assert.Zero(t, n)
	raw, err := s.GetRaw("setting:synthetic")
	require.NoError(t, err)
	assert.Equal(t, []byte("keep"), raw)
}
