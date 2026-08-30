// file: internal/database/activity_storer.go
// version: 1.6.0
// guid: a1b2c3d4-e5f6-0001-abcd-000000000001
// last-edited: 2026-08-29

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

// ActivityRetention covers pruning, compaction and migration.
type ActivityRetention interface {
	Prune(olderThan time.Time, tier string) (int, error)
	// WipeAllActivity deletes every activity entry. It takes a context because
	// it is reachable from a live request (handleWipe): an abandoned wipe
	// request must stop scanning promptly instead of running every tier to
	// completion server-side regardless of the client. On cancellation it
	// returns the count of rows ACTUALLY deleted so far (never a fabricated
	// full or zero count) alongside ctx.Err(); rows not yet reached are left
	// untouched, so the store is left in a state a plain retry can finish —
	// there is no partial-tier bookkeeping to resume, the retry just rescans
	// and deletes whatever remains.
	//
	// The count is a LOWER BOUND on keys removed, not an exact one: a backend
	// may additionally sweep rows it could not enumerate individually (the
	// Pebble implementation range-deletes the whole activity prefix on a
	// completed run, which also removes rows whose stored JSON will not decode
	// and index entries whose row was already gone). Under-reporting is the
	// deliberate direction — a caller may rely on the number never claiming
	// more than it can name.
	WipeAllActivity(ctx context.Context) (int64, error)
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
	// the compiler says widening breaks ONE test fake
	// (metafetch.capturingActivityStore) beyond the three real
	// implementations. Checkpointable earns its type assertion by hanging off
	// database.Store, which has ~398 methods and many mocks; ActivityStorer has
	// four implementations. The assertion would also add a fallback branch that
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
// activity.Writer. PebbleActivityStore is the production implementation
// (NutsActivityStore retired as of TASK-22, retained unwired pending removal).
//
// Query, GetDistinctSources and WipeAllActivity take a context and there is
// deliberately NO context-free variant of any of them. All three walk the
// activity log, and on production an abandoned request whose scan could not
// be cancelled kept decoding entries after the client had disconnected: 30
// goroutines held 30.8 GB inside the scan with ZERO connected clients, and
// only a restart freed it. A parallel non-context method would let the next
// caller reintroduce that outage, so the cancellable path is the only path.
// WipeAllActivity is included because it is reachable from handleWipe, a real
// HTTP handler, not merely a scheduled maintenance job.
//
// Prune and MigrateSystemActivityLogs remain intentionally context-free per
// scope: they share the same defect shape but are not reachable from a live
// request path today, so widening them is left as a follow-up. (Summarize and
// CompactByDay already took a context before this change.)
//
// Split into the 4 interfaces above on 2026-08-18. This name is retained as
// their composition so the method set is byte-identical and no consumer moves; the
// type checker proves it, because every implementation -- PebbleStore (496 methods)
// and database.MockStore (399) among them -- fails to compile on a dropped or
// re-signatured method.
type ActivityStorer interface {
	ActivityWriter
	ActivityReader
	ActivityRetention
	ActivityLifecycle
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
