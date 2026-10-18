// file: internal/database/user_state_lock.go
// version: 1.4.0
// guid: d3079a7f-dd6e-4d4f-8f7d-f9f740ba41e0
// last-edited: 2026-10-18

package database

import (
	"hash/fnv"
	"sync"
)

// userStateStripes is the size of the striped per-(user, book) lock below.
const userStateStripes = 256

var userStateLocks [userStateStripes]sync.Mutex

// LockUserBookState takes the process-wide lock for one user's listening
// state on one book (the ubs: row and the upos: rows) and returns its release.
// The store has no compare-and-set for user state, so every read-check-write
// of it that must not interleave with another holds this across the read and
// the write.
//
// HELD BY (2026-10-18), each from before its read until after its write --
// every writer of a ubs: or upos: row outside this package:
//   - the ABS write paths (handlers/abs): updateUserBookState,
//     applyProgressUpdate (PATCH/batch progress), applySessionUpdate (session
//     sync: taken before readStoredProgress, held through persistProgress,
//     then session.mu inside it), applyLocalSession (offline replay) and the
//     progress reset;
//   - readstatus.SetManualStatus (the web "mark as" endpoints),
//     readstatus.RecomputeUserBookState (a wrapper over
//     RecomputeUserBookStateLocked, whose caller holds it) and
//     readstatus.RebuildUserBookState (the web status repair);
//   - the web reading heartbeat (handlers.ReadingHandler.SetPosition): its
//     position write and the RecomputeUserBookStateLocked after it, in one
//     hold;
//   - the iTunes position sync (itunesservice.PositionSync), per book: the
//     bookmark seed's "no position yet" check, the seed and its
//     RecomputeUserBookStateLocked, then the "seed finished" check-then-write,
//     in one hold, on a NumCPU worker pool (books are disjoint, so two
//     workers never want one stripe for the same key);
//   - the iTunes position backfill job (maintenance/jobs
//     backfill-itunes-positions), per book, from its position and state
//     reads through its last state write, on its NumCPU worker pool;
//   - repairs.Writer.SetUserState (the Audible read-status import), taken
//     inside merge.LockMergeRMW;
//   - the revert of an undo.ChangeTypeUserBookStateSet row, taken inside
//     merge.LockMergeRMW;
//   - the merge undo restore (merge.RestoreFollowedProgress, inside
//     merge.LockMergeRMW): restoreAbsorbedSide across its read and write of
//     the absorbed book, and on the survivor (after the absorbed book's
//     stripe is released, never with it) restoreSurvivorSide, from before
//     its reads of the state, the positions and the put-back marker until
//     after the plain put-back's or the reconcile's last write;
//   - the merge follow / carry (merge.mergeUserProgressFor, the whole-book
//     rule, and merge.followSliceFor, the slice rule), per user, holding
//     BOTH books' stripes through LockUserBookStatePair from the loser's read
//     until the loser is drained. Reached under merge.LockMergeRMW (merges,
//     combines, the pending-repair sweep, the stranded-state repair) or
//     under merge's syncRepointMu (FollowBookIDChange, the scanner's
//     version-link path, which holds no merge lock);
//   - merge.DiscardUserStateThenHardDelete, per user, across that user's
//     read and clear, inside merge.LockMergeRMW.
//
// NOT HELD BY: nothing that writes these rows. Read-only users (listings,
// the ABS reads, snapshots taken before a follow) do not take it.
//
// LOCK ORDER: merge.LockMergeRMW (or merge's syncRepointMu), then this
// stripe, then anything else (session.mu, a book's write stripe -- the
// follow's carryPlayCountMark runs ModifyBook under it). Nothing takes this
// stripe and then the merge lock or syncRepointMu, nothing takes it inside a
// ModifyBook/UpdateBook callback, and no holder takes a metadata field-state
// stripe (database.LockMetadataState) under it.
//
// Striped, not per key: two keys can share a stripe. A holder of one stripe
// never takes a second user-state lock while holding it; the one path that
// needs two books at once takes both through LockUserBookStatePair, which
// orders them. Not reentrant: a function that runs both with and without the
// stripe held comes as a ...Locked variant (the caller holds it) and a
// wrapper that takes it.
func LockUserBookState(userID, bookID string) (unlock func()) {
	mu := &userStateLocks[userStateStripe(userID, bookID)]
	mu.Lock()
	return mu.Unlock
}

// LockUserBookStatePair takes the user-state stripes of one user's state on
// two books and returns one release for both. The merge follow holds it: it
// reads both books, writes the survivor and drains the loser as one step.
//
// The two stripes are taken in ascending stripe order, and only once when
// both books hash to the same stripe (a second Lock of it would self-
// deadlock). With every single-stripe holder taking no second stripe and
// every pair holder taking its two in ascending order, a wait-for cycle
// cannot form. Not reentrant, like LockUserBookState: the caller must hold
// no user-state stripe.
func LockUserBookStatePair(userID, bookA, bookB string) (unlock func()) {
	a, b := userStateStripe(userID, bookA), userStateStripe(userID, bookB)
	if a == b {
		mu := &userStateLocks[a]
		mu.Lock()
		return mu.Unlock
	}
	if a > b {
		a, b = b, a
	}
	first, second := &userStateLocks[a], &userStateLocks[b]
	first.Lock()
	second.Lock()
	return func() {
		second.Unlock()
		first.Unlock()
	}
}

// userStateStripe is the stripe index of (userID, bookID).
func userStateStripe(userID, bookID string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(userID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(bookID))
	return h.Sum32() % userStateStripes
}
