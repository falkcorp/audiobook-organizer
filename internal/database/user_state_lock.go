// file: internal/database/user_state_lock.go
// version: 1.6.0
// guid: d3079a7f-dd6e-4d4f-8f7d-f9f740ba41e0
// last-edited: 2026-10-26

package database

import (
	"hash/fnv"
	"slices"
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
//   - readstatus.SetManualStatus (the web "mark as" endpoints) and
//     readstatus.RebuildUserBookState (the web status repair), each taking
//     it itself; readstatus.RecomputeUserBookStateLocked takes none, its
//     caller holds it;
//   - the web reading heartbeat (handlers.ReadingHandler.SetPosition): its
//     position write and the RecomputeUserBookStateLocked after it, in one
//     hold;
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
//     the pending-repair sweep, the stranded-state repair) or under merge's
//     syncRepointMu (FollowBookIDChange, the scanner's version-link path,
//     which holds no merge lock);
//   - the JOURNALED follows (merge.CombineBooks and
//     merge.FollowAbsorbedJournaled: sibling moves, split-book and chapter
//     merges, retire-into, carry-before-delete), inside merge.LockMergeRMW:
//     every listed user's stripes on both books through LockUserBookStates,
//     from the before-snapshot through the after-snapshot, so the journal
//     the undo restores from describes exactly what the follow moved (the
//     per-user moves then run their ...Locked variants);
//   - merge.DiscardUserStateThenHardDeleteLocked, inside merge.LockMergeRMW: every
//     user's stripe on the book through LockUserBookStates, from the first
//     read through the hard delete.
//
// NOT HELD BY: nothing that reads and then writes these rows. Outside the
// PebbleStore methods themselves (pebble_store_playback.go), the only other
// writers are whole-store paths that read nothing first: PebbleStore.Reset
// (the factory reset's DeleteRange of every key) and the offline backup
// restore, which replaces the database files. Read-only users (listings, the
// ABS reads, the user-state probe) do not take it.
//
// LOCK ORDER: merge.LockMergeRMW (or merge's syncRepointMu), then this
// stripe, then anything else (session.mu, a book's write stripe -- the
// follow's carryPlayCountMark runs ModifyBook under it). Nothing takes this
// stripe and then the merge lock or syncRepointMu, nothing takes it inside a
// ModifyBook/UpdateBook callback, and no holder takes a metadata field-state
// stripe (database.LockMetadataState) under it.
//
// Striped, not per key: two keys can share a stripe. A holder of one stripe
// never takes a second user-state lock while holding it; a path that needs
// several keys at once takes them all in one LockUserBookStates (or
// LockUserBookStatePair) call, which orders the stripes. Not reentrant: a
// function that runs both with and without the stripe held comes as a
// ...Locked variant (the caller holds it) and a wrapper that takes it.
func LockUserBookState(userID, bookID string) (unlock func()) {
	mu := &userStateLocks[userStateStripe(userID, bookID)]
	mu.Lock()
	return mu.Unlock
}

// LockUserBookStatePair takes the user-state stripes of one user's state on
// two books and returns one release for both: LockUserBookStates of the two
// keys. The merge follow holds it: it reads both books, writes the survivor
// and drains the loser as one step.
func LockUserBookStatePair(userID, bookA, bookB string) (unlock func()) {
	return LockUserBookStates(UserBookKey{userID, bookA}, UserBookKey{userID, bookB})
}

// UserBookKey names one user's listening state on one book.
type UserBookKey struct {
	UserID string
	BookID string
}

// LockUserBookStates takes the user-state stripes of every key and returns
// one release for all of them. The journaled merge follow holds every
// followed user's stripes on both books from its before-snapshot through its
// after-snapshot, and the owner's discard holds every user's stripe on the
// book through the hard delete.
//
// The distinct stripes are taken in ascending stripe order, each once: keys
// that hash to one stripe share it (a second Lock of it would self-deadlock).
// With every single-stripe holder taking no second stripe and every
// multi-stripe holder taking its stripes in ascending order, a wait-for
// cycle cannot form. Not reentrant, like LockUserBookState: the caller must
// hold no user-state stripe.
func LockUserBookStates(keys ...UserBookKey) (unlock func()) {
	idx := make([]uint32, 0, len(keys))
	for _, k := range keys {
		idx = append(idx, userStateStripe(k.UserID, k.BookID))
	}
	slices.Sort(idx)
	idx = slices.Compact(idx)
	for _, i := range idx {
		userStateLocks[i].Lock()
	}
	return func() {
		for _, i := range slices.Backward(idx) {
			userStateLocks[i].Unlock()
		}
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
