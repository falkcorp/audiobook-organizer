// file: internal/database/pebble_metrics_store.go
// version: 1.0.0
// guid: e5f6a7b8-c9d0-0005-ef01-000000000005

// Package database — PebbleDB-backed cache-stats metrics store.
//
// WHY a Pebble backend:
//   - NutsMetricsStore (nuts_metrics_store.go) uses NutsDB's per-entry TTL
//     (30-day expiry). Pebble has no built-in per-key TTL, so we emulate it:
//     (a) writes record a 30-day expiry timestamp in each value's JSON, and
//     (b) a TTL sweep function (called from maintenance) prunes expired entries.
//     This is equivalent to the NutsDB behaviour and preserves the 30-day window
//     without requiring a background goroutine.
//
// Key layout:
//
//	met:<cacheName>:<20d-unix-nano>  = JSON(CacheStatsSnapshot)   primary
//	met:_idx:<cacheName>             = []byte("1")                cache-name index
//
// The "met:" prefix avoids collision with all other PebbleDB key families.
// Cache names are stored in met:_idx: so GetCacheStatsHistory can enumerate
// all known names without scanning every met: key.
package database

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

const (
	// pmetTTLSeconds is the time-to-live for metrics snapshots: 30 days.
	// Mirrors NutsMetricsStore's metricsTTL constant.
	pmetTTLSeconds = 30 * 24 * 3600

	pmetKeyMaxNano = "99999999999999999999"
	pmetIdxPrefix  = "met:_idx:"
)

// PebbleMetricsStore persists cache-stats snapshots in a shared PebbleDB database.
// It satisfies the MetricsStorer interface and is a drop-in replacement for
// NutsMetricsStore.
//
// The caller retains ownership of the *pebble.DB — Close() on this store is a no-op.
type PebbleMetricsStore struct {
	db *pebble.DB
}

// NewPebbleMetricsStore creates a PebbleMetricsStore backed by the provided DB.
// The caller retains ownership of db; Close() on this store does NOT close db.
func NewPebbleMetricsStore(db *pebble.DB) *PebbleMetricsStore {
	return &PebbleMetricsStore{db: db}
}

// Close is a no-op: the caller owns the PebbleDB instance.
func (s *PebbleMetricsStore) Close() error { return nil }

// ── Key construction ──────────────────────────────────────────────────────────

// pmetKey builds the primary key for a snapshot:
//
//	met:<cacheName>:<20d-unix-nano>
func pmetKey(cacheName string, t time.Time) []byte {
	return fmt.Appendf(nil, "met:%s:%020d", cacheName, t.UnixNano())
}

// pmetPrefix returns the inclusive lower-bound prefix for a cache-name range scan.
func pmetPrefix(cacheName string) []byte {
	return []byte("met:" + cacheName + ":")
}

// pmetUpperBound returns the exclusive upper-bound for a cache-name range scan.
// ';' (0x3B) is one above ':' (0x3A).
func pmetUpperBound(cacheName string) []byte {
	return []byte("met:" + cacheName + ";")
}

// pmetIdxKey builds the cache-name index key.
func pmetIdxKey(cacheName string) []byte {
	return []byte(pmetIdxPrefix + cacheName)
}

// ── envelope to track TTL ─────────────────────────────────────────────────────

// pmetValue wraps a CacheStatsSnapshot with an expiry wall-clock timestamp.
// WHY: Pebble has no built-in per-key TTL; we embed expiry in the value so
// SweepExpiredMetrics can skip (or delete) entries without re-parsing timestamps.
type pmetValue struct {
	ExpiresAt int64              `json:"expires_at"` // Unix seconds
	Snapshot  CacheStatsSnapshot `json:"snapshot"`
}

// ── MetricsStorer implementation ──────────────────────────────────────────────

// RecordCacheStatsSnapshots writes all snapshots in a single batch.
// Buckets / index entries are created on demand.
func (s *PebbleMetricsStore) RecordCacheStatsSnapshots(snapshots []CacheStatsSnapshot) error {
	if len(snapshots) == 0 {
		return nil
	}
	batch := s.db.NewBatch()
	defer batch.Close()

	for _, snap := range snapshots {
		v := pmetValue{
			ExpiresAt: snap.Timestamp.Unix() + pmetTTLSeconds,
			Snapshot:  snap,
		}
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("pebble_metrics_store: marshal %s: %w", snap.CacheName, err)
		}
		if err := batch.Set(pmetKey(snap.CacheName, snap.Timestamp), b, nil); err != nil {
			return fmt.Errorf("pebble_metrics_store: set snapshot %s: %w", snap.CacheName, err)
		}
		// Track cache name in the index (idempotent — Set is safe to repeat).
		if err := batch.Set(pmetIdxKey(snap.CacheName), []byte("1"), nil); err != nil {
			return fmt.Errorf("pebble_metrics_store: set idx %s: %w", snap.CacheName, err)
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return fmt.Errorf("pebble_metrics_store: commit: %w", err)
	}
	return nil
}

// GetCacheStatsHistory returns snapshots since the given time, oldest-first.
// If cacheName is empty, all known caches are returned merged and sorted.
func (s *PebbleMetricsStore) GetCacheStatsHistory(cacheName string, since time.Time, limit int) ([]CacheStatsSnapshot, error) {
	names, err := s.cacheNames(cacheName)
	if err != nil {
		return nil, err
	}

	now := time.Now().Unix()
	var out []CacheStatsSnapshot

	for _, name := range names {
		lower := pmetKey(name, since)
		upper := fmt.Appendf(nil, "met:%s:%s", name, pmetKeyMaxNano)

		iter, err := s.db.NewIter(&pebble.IterOptions{
			LowerBound: lower,
			UpperBound: upper,
		})
		if err != nil {
			return nil, fmt.Errorf("pebble_metrics_store: GetCacheStatsHistory new iter: %w", err)
		}
		for iter.First(); iter.Valid(); iter.Next() {
			var v pmetValue
			if err := json.Unmarshal(iter.Value(), &v); err != nil {
				continue
			}
			// Skip expired entries (TTL enforcement on read).
			if v.ExpiresAt <= now {
				continue
			}
			out = append(out, v.Snapshot)
		}
		_ = iter.Close()
	}

	// Sort oldest-first; multi-cache entries from different prefixes need sorting.
	if len(names) > 1 {
		sort.Slice(out, func(i, j int) bool {
			return out[i].Timestamp.Before(out[j].Timestamp)
		})
	}

	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// PruneCacheStatsHistory deletes snapshots older than olderThan.
// Unlike NutsMetricsStore (which relies solely on TTL), this method provides
// an explicit prune so maintenance can reclaim space on demand.
func (s *PebbleMetricsStore) PruneCacheStatsHistory(olderThan time.Time) (int64, error) {
	names, err := s.cacheNames("")
	if err != nil {
		return 0, err
	}

	var deleted int64
	for _, name := range names {
		lower := pmetPrefix(name)
		upper := pmetKey(name, olderThan)

		iter, err := s.db.NewIter(&pebble.IterOptions{
			LowerBound: lower,
			UpperBound: upper,
		})
		if err != nil {
			return deleted, fmt.Errorf("pebble_metrics_store: PruneCacheStatsHistory iter: %w", err)
		}

		// Collect keys to delete.
		var keys [][]byte
		for iter.First(); iter.Valid(); iter.Next() {
			keyCopy := make([]byte, len(iter.Key()))
			copy(keyCopy, iter.Key())
			keys = append(keys, keyCopy)
		}
		_ = iter.Close()

		for i := 0; i < len(keys); i += 500 {
			end := min(i+500, len(keys))
			batch := s.db.NewBatch()
			for _, k := range keys[i:end] {
				if err := batch.Delete(k, nil); err != nil {
					batch.Close()
					return deleted, err
				}
			}
			if err := batch.Commit(pebble.Sync); err != nil {
				batch.Close()
				return deleted, fmt.Errorf("pebble_metrics_store: prune commit: %w", err)
			}
			batch.Close()
			deleted += int64(end - i)
		}
	}
	return deleted, nil
}

// SweepExpiredMetrics deletes snapshots whose embedded ExpiresAt has passed.
// This is the TTL sweep that replaces NutsDB's per-entry TTL. It is called
// from the maintenance job registered in jobs/sweep_pebble_metrics_ttl.go.
//
// Returns the number of entries deleted.
func (s *PebbleMetricsStore) SweepExpiredMetrics() (int64, error) {
	now := time.Now().Unix()
	names, err := s.cacheNames("")
	if err != nil {
		return 0, err
	}

	var deleted int64
	for _, name := range names {
		iter, err := s.db.NewIter(&pebble.IterOptions{
			LowerBound: pmetPrefix(name),
			UpperBound: pmetUpperBound(name),
		})
		if err != nil {
			return deleted, fmt.Errorf("pebble_metrics_store: SweepExpiredMetrics iter: %w", err)
		}

		var keys [][]byte
		for iter.First(); iter.Valid(); iter.Next() {
			var v pmetValue
			if err := json.Unmarshal(iter.Value(), &v); err != nil {
				continue
			}
			if v.ExpiresAt <= now {
				keyCopy := make([]byte, len(iter.Key()))
				copy(keyCopy, iter.Key())
				keys = append(keys, keyCopy)
			}
		}
		_ = iter.Close()

		for i := 0; i < len(keys); i += 500 {
			end := min(i+500, len(keys))
			batch := s.db.NewBatch()
			for _, k := range keys[i:end] {
				if err := batch.Delete(k, nil); err != nil {
					batch.Close()
					return deleted, err
				}
			}
			if err := batch.Commit(pebble.Sync); err != nil {
				batch.Close()
				return deleted, fmt.Errorf("pebble_metrics_store: sweep commit: %w", err)
			}
			batch.Close()
			deleted += int64(end - i)
		}
	}
	return deleted, nil
}

// ── internal helpers ──────────────────────────────────────────────────────────

// cacheNames returns [cacheName] when non-empty, or all known names from the index.
func (s *PebbleMetricsStore) cacheNames(cacheName string) ([]string, error) {
	if cacheName != "" {
		return []string{cacheName}, nil
	}
	var names []string
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(pmetIdxPrefix),
		UpperBound: []byte("met:_idx;"),
	})
	if err != nil {
		return nil, fmt.Errorf("pebble_metrics_store: cacheNames iter: %w", err)
	}
	defer iter.Close()
	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		name := key[len(pmetIdxPrefix):]
		if name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}
