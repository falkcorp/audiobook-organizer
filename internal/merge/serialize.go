// file: internal/merge/serialize.go
// version: 1.1.0
// guid: b1e7d4a9-2c63-4f81-9a05-6d3e2f0c7b48
// last-edited: 2026-09-10

package merge

import "sync"

// mergeSerializeMu serializes EVERY merge-family read-modify-write across the
// whole process. These four read-modify-writes in internal/merge and
// internal/dedup acquire this one lock so any two of them are mutually
// exclusive on a shared book row:
//
//   - merge.Service.MergeBooks     (version-group merge; soft-deletes losers)
//   - merge.Service.CombineBooks   (multi-file combine; hard-deletes shells)
//   - dedup.MergeBooks             (iTunes-metadata transfer + hard-delete;
//     a SEPARATE package function, not on Service,
//     which reaches this lock via LockMergeRMW)
//   - dedup.MergeSplitBookCluster  (moves BookFiles onto keep, recomputes
//     duration, soft-deletes each src; another
//     SEPARATE package function reaching this
//     lock via LockMergeRMW)
//
// NOT covered: internal/maintenance/jobs/dedup_books.go's ddMergeDuplicateBook
// / ddSoftDeleteBook do the same shape of unguarded read-modify-write (a
// legacy, separately-maintained dedup job) but do not take this lock. Tracked
// separately — out of scope for this fix, which only wires in
// dedup.MergeSplitBookCluster (DA-01).
//
// Why one shared lock, not one per path: each does an unguarded
// GetBookByID -> mutate -> UpdateBook / DeleteBook / SoftDeleteBook / external-ID
// reassignment with no transaction. They run concurrently — CombineBooks is a
// synchronous HTTP handler with no concurrency key, dedup.MergeBooks is
// reachable from two async ops with DIFFERENT ConcurrencyKeys, and
// dedup.MergeSplitBookCluster is reachable from both an async bulk-merge op
// (whose ConcurrencyKey only serializes it against itself) and a synchronous
// HTTP handler with no concurrency key at all — so two of them (or one racing
// MergeBooks) can interleave writes to the same book and leave it
// both primary AND soft-deleted, strand a version group across two ulids,
// hard-delete a book another path just promoted, or soft-delete the winner.
// Separate locks would leave the cross-path races open; only a single shared
// lock makes every merge atomic w.r.t. every other merge. (Originally #1930 put
// this mutex on merge.Service; it moved to package level so dedup.MergeBooks —
// which cannot reach a *merge.Service instance from the reconcile op path — can
// share the exact same lock. merge.Service is a process singleton, so this is
// behaviorally identical for the Service methods and strictly safer.)
//
// Scope: hold it only around the read-modify-write itself; nothing
// slow/blocking (network, large scan) runs while it is held.
var mergeSerializeMu sync.Mutex

// LockMergeRMW acquires the shared merge serialization lock for a caller OUTSIDE
// this package (specifically dedup.MergeBooks and dedup.MergeSplitBookCluster,
// whose read-modify-writes must be mutually exclusive with
// merge.Service.MergeBooks / CombineBooks on shared book rows). Pair it with a
// deferred UnlockMergeRMW. Non-reentrant: a caller must not already hold this
// lock and must not call another merge-family path while holding it.
func LockMergeRMW() { mergeSerializeMu.Lock() }

// UnlockMergeRMW releases the lock taken by LockMergeRMW.
func UnlockMergeRMW() { mergeSerializeMu.Unlock() }
