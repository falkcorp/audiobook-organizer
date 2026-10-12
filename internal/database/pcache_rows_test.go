// file: internal/database/pcache_rows_test.go
// version: 1.0.0
// guid: fa5307ff-b133-422e-9954-34ea42a9e692
// last-edited: 2026-10-11

package database

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCountPCache_KindsAndExpired(t *testing.T) {
	s := newPCacheTestStore(t)
	t0 := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	rule := func(kind, provider string) PCacheTTL {
		if kind == PCacheKindProduct {
			return PCacheTTL{Positive: 30 * 24 * time.Hour, Negative: 14 * 24 * time.Hour}
		}
		if provider == "hardcover" {
			return PCacheTTL{Positive: 180 * 24 * time.Hour}
		}
		return PCacheTTL{Positive: 180 * 24 * time.Hour, Negative: 14 * 24 * time.Hour}
	}
	put := func(key, provider string, empty bool, askedAt time.Time) {
		blob, err := json.Marshal(PCacheEntry{Provider: provider, AskedAt: askedAt, Empty: empty, Results: json.RawMessage(`[]`)})
		require.NoError(t, err)
		require.NoError(t, s.SetRaw(key, blob))
	}
	put("pcache:q:audible:search_title:a", "audible", false, t0.Add(-10*24*time.Hour))
	put("pcache:q:audible:search_title:b", "audible", true, t0.Add(-20*24*time.Hour)) // expired
	put("pcache:q:hardcover:search_title:c", "", true, t0)                            // negative off; provider from the key
	put("pcache:asin:audible:B0SYNTH001", "audible", false, t0.Add(-31*24*time.Hour)) // expired product
	require.NoError(t, s.SetRaw("pcache:q:audible:search_title:d", []byte("{")))      // corrupt
	require.NoError(t, RecordPCachePointer(s, "book-a", map[string][]string{"audible": {"pcache:q:audible:search_title:a"}}, t0))
	require.NoError(t, s.SetRaw("metadata_fetch_cache:book-a:audible", []byte("{}")))

	c, pages, err := CountPCache(context.Background(), s, rule, t0, 2)
	require.NoError(t, err)
	assert.Equal(t, PCacheCounts{Total: 6, Questions: 4, Products: 1, Pointers: 1, Expired: 4}, c)
	assert.Equal(t, 3, pages, "six rows in pages of two")

	dead, reason := PCacheRowDead("pcache:q:hardcover:search_title:c", []byte(`{"empty":true}`), rule, t0)
	assert.True(t, dead)
	assert.Equal(t, PCacheMissNegativeOff, reason)
	dead, _ = PCacheRowDead(PCacheBookPrefix+"book-a", []byte("{"), rule, t0)
	assert.False(t, dead, "a pointer row is never dead by TTL")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = CountPCache(ctx, s, rule, t0, 2)
	assert.ErrorIs(t, err, context.Canceled)
}
