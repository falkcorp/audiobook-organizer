// file: internal/database/activity_storer.go
// version: 1.3.0
// guid: a1b2c3d4-e5f6-0001-abcd-000000000001
// last-edited: 2026-08-11

package database

import (
	"context"
	"time"
)

// ActivityStorer is the minimal interface required by activity.Service and
// activity.Writer. PebbleActivityStore is the production implementation
// (NutsActivityStore retired as of TASK-22, retained unwired pending removal).
//
// Query and GetDistinctSources take a context and there is deliberately NO
// context-free variant of either. Both walk the activity log, and on production
// an abandoned request whose scan could not be cancelled kept decoding entries
// after the client had disconnected: 30 goroutines held 30.8 GB inside the scan
// with ZERO connected clients, and only a restart freed it. A parallel
// non-context method would let the next caller reintroduce that outage, so the
// cancellable path is the only path. The remaining methods are intentionally
// left context-free: they are maintenance/write operations that are not driven
// by an abandonable HTTP request.
type ActivityStorer interface {
	Record(ActivityEntry) (int64, error)
	Query(context.Context, ActivityFilter) ([]ActivityEntry, int, error)
	Summarize(ctx context.Context, olderThan time.Time, tier string) (int, error)
	Prune(olderThan time.Time, tier string) (int, error)
	GetDistinctSources(context.Context, ActivityFilter) ([]SourceCount, error)
	WipeAllActivity() (int64, error)
	CompactByDay(ctx context.Context, olderThan time.Time) (CompactResult, error)
	RecompactDigests(ctx context.Context) (RecompactResult, error)
	MigrateSystemActivityLogs() (int, error)
	Close() error
}

// MetricsStorer is the minimal interface required by server cache handlers.
// PebbleMetricsStore is the production implementation (NutsMetricsStore
// retired as of TASK-22, retained unwired pending removal).
type MetricsStorer interface {
	RecordCacheStatsSnapshots([]CacheStatsSnapshot) error
	GetCacheStatsHistory(cacheName string, since time.Time, limit int) ([]CacheStatsSnapshot, error)
	PruneCacheStatsHistory(olderThan time.Time) (int64, error)
	Close() error
}
