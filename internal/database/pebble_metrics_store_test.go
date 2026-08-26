// file: internal/database/pebble_metrics_store_test.go
// version: 1.0.0
// guid: d0e1f2a3-b4c5-0011-4567-000000000011

package database

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestPebbleMetricsStore creates a temp PebbleDB and a PebbleMetricsStore.
func newTestPebbleMetricsStore(t *testing.T) *PebbleMetricsStore {
	t.Helper()
	dir := t.TempDir()
	db, err := pebble.Open(filepath.Join(dir, "metrics.pebble"), &pebble.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return NewPebbleMetricsStore(db)
}

// TestPebbleMetricsStore_RecordAndRetrieve verifies basic round-trip.
func TestPebbleMetricsStore_RecordAndRetrieve(t *testing.T) {
	s := newTestPebbleMetricsStore(t)

	now := time.Now().UTC().Truncate(time.Second)
	snaps := []CacheStatsSnapshot{
		{CacheName: "authors", Timestamp: now.Add(-2 * time.Minute), Hits: 10, Misses: 2},
		{CacheName: "authors", Timestamp: now.Add(-1 * time.Minute), Hits: 20, Misses: 3},
		{CacheName: "series", Timestamp: now.Add(-30 * time.Second), Hits: 5, Misses: 1},
	}

	err := s.RecordCacheStatsSnapshots(snaps)
	require.NoError(t, err)

	t.Run("retrieve_named_cache", func(t *testing.T) {
		got, err := s.GetCacheStatsHistory("authors", time.Time{}, 100)
		require.NoError(t, err)
		assert.Len(t, got, 2, "expected 2 authors snapshots")
		// oldest-first
		assert.Equal(t, int64(10), got[0].Hits)
		assert.Equal(t, int64(20), got[1].Hits)
	})

	t.Run("retrieve_all_caches", func(t *testing.T) {
		got, err := s.GetCacheStatsHistory("", time.Time{}, 100)
		require.NoError(t, err)
		assert.Len(t, got, 3)
	})

	t.Run("since_filter", func(t *testing.T) {
		cutoff := now.Add(-90 * time.Second)
		got, err := s.GetCacheStatsHistory("authors", cutoff, 100)
		require.NoError(t, err)
		assert.Len(t, got, 1, "only the latest authors snapshot should be after cutoff")
		assert.Equal(t, int64(20), got[0].Hits)
	})

	t.Run("limit", func(t *testing.T) {
		got, err := s.GetCacheStatsHistory("", time.Time{}, 2)
		require.NoError(t, err)
		assert.Len(t, got, 2, "limit should be applied")
	})
}

// TestPebbleMetricsStore_Close_Noop verifies Close is a no-op.
func TestPebbleMetricsStore_Close_Noop(t *testing.T) {
	s := newTestPebbleMetricsStore(t)
	require.NoError(t, s.Close())
}

// TestPebbleMetricsStore_PruneCacheStatsHistory prunes old snapshots.
func TestPebbleMetricsStore_PruneCacheStatsHistory(t *testing.T) {
	s := newTestPebbleMetricsStore(t)

	cutoff := time.Now().UTC().Add(-1 * time.Hour)

	// 3 old snapshots.
	for i := range 3 {
		err := s.RecordCacheStatsSnapshots([]CacheStatsSnapshot{
			{CacheName: "authors", Timestamp: cutoff.Add(-time.Duration(i+1) * time.Minute), Hits: int64(i)},
		})
		require.NoError(t, err)
	}

	// 1 recent snapshot.
	err := s.RecordCacheStatsSnapshots([]CacheStatsSnapshot{
		{CacheName: "authors", Timestamp: time.Now().UTC(), Hits: 99},
	})
	require.NoError(t, err)

	pruned, err := s.PruneCacheStatsHistory(cutoff)
	require.NoError(t, err)
	assert.Equal(t, int64(3), pruned, "3 old snapshots should be pruned")

	remaining, err := s.GetCacheStatsHistory("authors", time.Time{}, 100)
	require.NoError(t, err)
	assert.Len(t, remaining, 1, "only the recent snapshot should remain")
	assert.Equal(t, int64(99), remaining[0].Hits)
}

// TestPebbleMetricsStore_SweepExpiredMetrics tests TTL sweep.
func TestPebbleMetricsStore_SweepExpiredMetrics(t *testing.T) {
	dir := t.TempDir()
	db, err := pebble.Open(filepath.Join(dir, "metrics.pebble"), &pebble.Options{})
	require.NoError(t, err)
	defer db.Close()
	s := NewPebbleMetricsStore(db)

	// Write one snapshot with a past ExpiresAt to simulate an expired entry.
	pastTime := time.Now().UTC().Add(-40 * 24 * time.Hour) // 40 days ago (> 30d TTL)
	err = s.RecordCacheStatsSnapshots([]CacheStatsSnapshot{
		{CacheName: "expired-cache", Timestamp: pastTime, Hits: 1},
	})
	require.NoError(t, err)

	// Write a fresh snapshot that should survive.
	err = s.RecordCacheStatsSnapshots([]CacheStatsSnapshot{
		{CacheName: "fresh-cache", Timestamp: time.Now().UTC(), Hits: 42},
	})
	require.NoError(t, err)

	// Sweep: should delete the expired entry.
	deleted, err := s.SweepExpiredMetrics()
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted, "one expired snapshot should be deleted")

	// Fresh entry should still be readable.
	remaining, err := s.GetCacheStatsHistory("fresh-cache", time.Time{}, 100)
	require.NoError(t, err)
	assert.Len(t, remaining, 1)
	assert.Equal(t, int64(42), remaining[0].Hits)

	// Expired entry should be gone.
	expired, err := s.GetCacheStatsHistory("expired-cache", time.Time{}, 100)
	require.NoError(t, err)
	assert.Len(t, expired, 0)
}

// TestPebbleMetricsStore_EmptyStore verifies graceful handling of an empty store.
func TestPebbleMetricsStore_EmptyStore(t *testing.T) {
	s := newTestPebbleMetricsStore(t)

	// Get from empty store should return empty slice, no error.
	snaps, err := s.GetCacheStatsHistory("nonexistent", time.Time{}, 100)
	require.NoError(t, err)
	assert.Empty(t, snaps)

	// Prune from empty store.
	n, err := s.PruneCacheStatsHistory(time.Now())
	require.NoError(t, err)
	assert.Equal(t, int64(0), n)

	// Sweep from empty store.
	n2, err := s.SweepExpiredMetrics()
	require.NoError(t, err)
	assert.Equal(t, int64(0), n2)
}

// TestPebbleMetricsStore_Implements verifies that PebbleMetricsStore satisfies MetricsStorer.
func TestPebbleMetricsStore_Implements(t *testing.T) {
	var _ MetricsStorer = (*PebbleMetricsStore)(nil)
}
