// file: internal/merge/serialize.go
// version: 1.0.0
// guid: b1e7d4a9-2c63-4f81-9a05-6d3e2f0c7b48
// last-edited: 2026-07-13

package merge

import "sync"

// mergeSerializeMu serializes EVERY merge-family read-modify-write across the
// whole process. All three unguarded paths in the codebase acquire this one
// lock so any two of them are mutually exclusive on a shared book row:
//
//   - merge.Service.MergeBooks   (version-group merge; soft-deletes losers)
//   - merge.Service.CombineBooks (multi-file combine; hard-deletes shells)
//   - dedup.MergeBooks           (iTunes-metadata transfer + hard-delete;
//     a SEPARATE package function, not on Service,
//     which reaches this lock via LockMergeRMW)
//
// Why one shared lock, not one per path: each does an unguarded
// GetBookByID -> mutate -> UpdateBook / DeleteBook / SoftDeleteBook / external-ID
// reassignment with no transaction. They run concurrently — CombineBooks is a
// synchronous HTTP handler with no concurrency key, and dedup.MergeBooks is
// reachable from two async ops with DIFFERENT ConcurrencyKeys — so two of them
// (or one racing MergeBooks) can interleave writes to the same book and leave it
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
// this package (specifically dedup.MergeBooks, whose read-modify-write must be
// mutually exclusive with merge.Service.MergeBooks / CombineBooks on shared
// book rows). Pair it with a deferred UnlockMergeRMW. Non-reentrant: a caller
// must not already hold this lock and must not call another merge-family path
// while holding it.
func LockMergeRMW() { mergeSerializeMu.Lock() }

// UnlockMergeRMW releases the lock taken by LockMergeRMW.
func UnlockMergeRMW() { mergeSerializeMu.Unlock() }
