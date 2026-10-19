// file: internal/database/activity_storer.go
// version: 1.14.0
// guid: a1b2c3d4-e5f6-0001-abcd-000000000001
// last-edited: 2026-10-19

package database

import (
	"context"
	"time"
)

// ActivityWriter appends activity records.
type ActivityWriter interface {
	Record(ActivityEntry) (int64, error)
}

// ActivityReader queries and summarizes activity.
type ActivityReader interface {
	Query(context.Context, ActivityFilter) ([]ActivityEntry, int, error)
	Summarize(ctx context.Context, olderThan time.Time, tier string) (int, error)
	GetDistinctSources(context.Context, ActivityFilter) ([]SourceCount, error)
}

// ActivityCounter reports an EXACT row count for a tier, optionally bounded to
// rows strictly older than a cutoff.
//
// It is separate from ActivityReader.Query on purpose. Query's total is a
// pagination probe — the Pebble store stops at Offset+Limit+1 matches (and
// again at activityQueryScanBudget) — so it cannot be used to size a keyspace.
// Anything reporting "how much is there" must ask for it explicitly, and a
// backend that cannot answer exactly should be refused rather than guessed at.
//
// It is an optional interface rather than part of ActivityStorer so a backend
// that cannot count cheaply is not forced to fake it.
type ActivityCounter interface {
	CountActivity(ctx context.Context, tier string, olderThan *time.Time) (int, error)
}

// ActivityRetention covers pruning, compaction and migration.
type ActivityRetention interface {
	// Prune hard-deletes every entry of tier older than olderThan and returns
	// the count actually deleted. It takes a context because a cancelled
	// nightly cleanup (or one past its op budget) must stop at its next batch
	// rather than prune to completion. On cancellation it returns the
	// rows deleted so far — a lower bound, never a projection — alongside
	// ctx.Err(); rows not yet reached are left untouched and a plain retry
	// finishes them.
	// Implementations report each committed batch through
	// ReportMaintenanceProgress (MaintenancePhasePrune) when a
	// WithMaintenanceProgress hook rides ctx.
	Prune(ctx context.Context, olderThan time.Time, tier string) (int, error)
	CompactByDay(ctx context.Context, olderThan time.Time) (CompactResult, error)
	RecompactDigests(ctx context.Context) (RecompactResult, error)
	// RepairActivityIndexes deletes secondary index entries (act:op:, act:bk:)
	// whose primary row no longer exists. Until 2026-08-29 no deletion path
	// removed index entries at all, so every pruned, summarized, compacted or
	// wiped row left its indexes behind permanently — on production act:op:
	// alone held ~0.783 GiB of a ~1.342 GiB activity keyspace. The leak is
	// closed in the delete paths; this is the repair for what they already
	// left, and it is idempotent, so the maintenance job can run it nightly.
	//
	// It sits here rather than behind an optional interface + type assertion
	// (the backup.Checkpointable shape) for two measured reasons, both checked
	// rather than assumed. First, precedent: RecompactDigests directly above is
	// equally backend-specific and already lives here, and
	// MigrateSystemActivityLogs is documented as a no-op on the Pebble backend
	// — the interface already carries exactly this category, so an optional
	// interface would be a second pattern for one kind of thing. Second, cost:
	// the compiler said widening broke ONE test fake
	// (metafetch.capturingActivityStore) beyond the real implementations.
	// Checkpointable earns its type assertion by hanging off database.Store,
	// which has ~398 methods and many mocks; ActivityStorer has a handful of
	// implementations. The assertion would also add a fallback branch that
	// does nothing and says nothing when it misses — the failure shape this
	// repo keeps getting burned by — to buy back one three-line fake.
	RepairActivityIndexes(ctx context.Context) (ActivityIndexRepairResult, error)
	MigrateSystemActivityLogs() (int, error)
}

// ActivityLifecycle covers store teardown.
type ActivityLifecycle interface {
	Close() error
}

// ActivityStorer is the minimal interface required by activity.Service and
// activity.Writer. PebbleActivityStore is the production implementation.
//
// Query and GetDistinctSources take a context and there is deliberately NO
// context-free variant of either. Both walk the activity log, and on
// production an abandoned request whose scan could not be cancelled kept
// decoding entries after the client had disconnected: 30 goroutines held
// 30.8 GB inside the scan with ZERO connected clients, and only a restart
// freed it. A parallel non-context method would let the next caller
// reintroduce that outage, so the cancellable path is the only path.
// MigrateSystemActivityLogs remains intentionally context-free: it shares the
// same defect shape but is not reachable from a live request path today, so
// widening it is left as a follow-up. (Summarize, Prune and CompactByDay
// already take a context.)
//
// Split into the 4 interfaces above on 2026-08-18. This name is retained as
// their composition so the method set is byte-identical and no consumer moves;
// the type checker proves it, because every implementation -- PebbleStore and
// database.MockStore among them -- fails to compile on a dropped or
// re-signatured method.
type ActivityStorer interface {
	ActivityWriter
	ActivityReader
	ActivityRetention
	ActivityLifecycle
}

// MetricsStorer is the minimal interface required by server cache handlers.
// PebbleMetricsStore is the production implementation.
type MetricsStorer interface {
	RecordCacheStatsSnapshots([]CacheStatsSnapshot) error
	GetCacheStatsHistory(cacheName string, since time.Time, limit int) ([]CacheStatsSnapshot, error)
	PruneCacheStatsHistory(olderThan time.Time) (int64, error)
	Close() error
}
