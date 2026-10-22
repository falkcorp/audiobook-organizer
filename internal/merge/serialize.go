// file: internal/merge/serialize.go
// version: 1.5.0
// guid: b1e7d4a9-2c63-4f81-9a05-6d3e2f0c7b48
// last-edited: 2026-10-22

package merge

import (
	"sync"
	"sync/atomic"
)

// mergeSerializeMu serializes EVERY merge-family read-modify-write across the
// whole process. These read-modify-writes in internal/merge and
// internal/dedup acquire this one lock so any two of them are mutually
// exclusive on a shared book row:
//
//   - merge.Service.MergeBooks     (version-group merge; soft-deletes losers)
//   - merge.Service.CombineBooks   (multi-file combine; hard-deletes shells)
//   - dedup.MergeSplitBookClusterWithOptions  (moves BookFiles onto keep, recomputes
//     duration, soft-deletes each src; another
//     SEPARATE package function reaching this
//     lock via LockMergeRMW)
//   - audiobooks' trash purge of a book with no listening state
//     (purgeIfStateless: re-read, state check and DeleteBook in one hold, so
//     a merge revert putting progress back cannot slip in between). It holds
//     the lock through LockMergeRMWBookRowsOnly, which does not count the
//     hold (see MergeLockAcquisitions for why that is safe).
//
// NOT covered: internal/maintenance/jobs/dedup_books.go's ddMergeDuplicateBookSim
// / ddSoftDeleteBook do the same shape of unguarded read-modify-write (a
// legacy, separately-maintained dedup job) but do not take this lock. Tracked
// separately — out of scope for this fix, which only wires in
// dedup.MergeSplitBookClusterWithOptions (DA-01).
//
// Why one shared lock, not one per path: each does an unguarded
// GetBookByID -> mutate -> UpdateBook / DeleteBook / SoftDeleteBook / external-ID
// reassignment with no transaction. They run concurrently — CombineBooks runs in
// the dedup.book-combine op (sharing dedup.book-merge's concurrency key, but
// the service is also called directly by the review queue), and
// dedup.MergeSplitBookClusterWithOptions is reachable from both an async bulk-merge op
// (whose ConcurrencyKey only serializes it against itself) and a synchronous
// HTTP handler with no concurrency key at all — so two of them (or one racing
// MergeBooks) can interleave writes to the same book and leave it
// both primary AND soft-deleted, strand a version group across two ulids,
// hard-delete a book another path just promoted, or soft-delete the winner.
// Separate locks would leave the cross-path races open; only a single shared
// lock makes every merge atomic w.r.t. every other merge. (Originally #1930 put
// this mutex on merge.Service; it moved to package level so dedup's package
// functions, which cannot reach a *merge.Service instance, can share the exact
// same lock. merge.Service is a process singleton, so this is
// behaviorally identical for the Service methods and strictly safer.)
//
// Scope: hold it only around the read-modify-write itself; nothing
// slow/blocking (network, large scan) runs while it is held.
var mergeSerializeMu countedMutex

// countedMutex is a sync.Mutex that counts its acquisitions, so a holder can
// tell whether anyone else held the lock between two of its own holds
// (MergeLockAcquisitions).
type countedMutex struct {
	mu       sync.Mutex
	acquired atomic.Uint64
}

func (m *countedMutex) Lock() {
	m.mu.Lock()
	m.acquired.Add(1)
}

// lockUncounted acquires the lock without counting the hold. Only for a
// holder that meets the contract on MergeLockAcquisitions.
func (m *countedMutex) lockUncounted() { m.mu.Lock() }

func (m *countedMutex) Unlock() { m.mu.Unlock() }

// MergeLockAcquisitions is how many times the merge serialization lock has
// been acquired, by a counted hold, since the process started. Read while
// holding the lock, it includes the caller's own acquisition: a caller that
// saw n at its previous hold and reads n+1 now knows no other COUNTED holder
// held the lock in between. The chapter-fragment fixer's apply keeps one
// whole-library snapshot (books, their book_file rows, authors) for the run
// on that evidence: it catches the snapshot up point by point from the book
// change log (database.BooksChangedSinceOf, which logs every book create,
// update and delete with its id), and lists the whole library again (~40k
// books and ~742k file rows on prod, ~460 MB transient) whenever this count
// moved by more than its own hold, because a merge-family writer can move
// book_file rows without writing a book, and the change log does not see that.
//
// The contract, then: a hold may go uncounted (LockMergeRMWBookRowsOnly) only
// when every write it makes under the lock that such a snapshot reads is a
// book-row write the book change log records (CreateBook, UpdateBook,
// ModifyBook, DeleteBook all are), and it never writes a book_file row. A
// holder that may move, create or rewrite a file row, or that writes anything
// else a counter consumer reads without logging it, must count its hold
// (LockMergeRMW). Writes no consumer reads (a book tombstone snapshot, an
// external-ID tombstone) do not matter.
//
// The trash purge is the one uncounted holder: per trashed book it re-reads
// the row, checks listening state, and deletes the row (DeleteBook, logged),
// writing a tombstone snapshot and external-ID tombstones beside it, and it
// never touches a file row (it refuses a book that still owns any:
// database.ErrBookOwnsFiles). Counted, it bumped this once per trashed book,
// so a purge running beside a fixer apply made the fixer list the whole
// library again before nearly every row (round-4 review S1 of #4064). One
// hold per partition was the alternative and was rejected: partitions purge
// concurrently, so it would still bump the count between the fixer's rows.
func MergeLockAcquisitions() uint64 { return mergeSerializeMu.acquired.Load() }

// LockMergeRMW acquires the shared merge serialization lock for a caller OUTSIDE
// this package (specifically dedup.MergeSplitBookClusterWithOptions, whose read-modify-writes must be mutually exclusive with
// merge.Service.MergeBooks / CombineBooks on shared book rows). Pair it with a
// deferred UnlockMergeRMW. Non-reentrant: a caller must not already hold this
// lock and must not call another merge-family path while holding it.
func LockMergeRMW() { mergeSerializeMu.Lock() }

// LockMergeRMWBookRowsOnly acquires the same lock as LockMergeRMW (the holder
// is just as exclusive against every merge-family path) but does not count
// the hold in MergeLockAcquisitions. Only for a holder whose writes under the
// lock meet the contract documented there: book rows the book change log
// records, never a book_file row. Pair it with a deferred UnlockMergeRMW.
// Non-reentrant, like LockMergeRMW.
func LockMergeRMWBookRowsOnly() { mergeSerializeMu.lockUncounted() }

// UnlockMergeRMW releases the lock taken by LockMergeRMW or
// LockMergeRMWBookRowsOnly.
func UnlockMergeRMW() { mergeSerializeMu.Unlock() }
