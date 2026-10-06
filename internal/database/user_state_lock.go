// file: internal/database/user_state_lock.go
// version: 1.2.0
// guid: d3079a7f-dd6e-4d4f-8f7d-f9f740ba41e0
// last-edited: 2026-10-06

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
// HELD BY (2026-10-05), each from before its read until after its write:
//   - the ABS write paths (handlers/abs): updateUserBookState,
//     applyProgressUpdate (PATCH/batch progress), applySessionUpdate (session
//     sync: taken before readStoredProgress, held through persistProgress,
//     then session.mu inside it), applyLocalSession (offline replay) and the
//     progress reset;
//   - readstatus.SetManualStatus (the web "mark as" endpoints);
//   - the iTunes position sync's "seed finished" check-then-write (then the
//     book's write stripe inside ModifyBook);
//   - repairs.Writer.SetUserState (the Audible read-status import), taken
//     inside merge.LockMergeRMW;
//   - the revert of an undo.ChangeTypeUserBookStateSet row, taken inside
//     merge.LockMergeRMW;
//   - the merge undo restore (merge.RestoreFollowedProgress, inside
//     merge.LockMergeRMW): restoreAbsorbedSide across its read and write of
//     the absorbed book, and reconcileTouchedSurvivor across its re-read
//     and every write (positions, marker, state) on the survivor.
//
// LOCK ORDER: merge.LockMergeRMW, then this stripe, then anything else
// (session.mu, a book's write stripe). Nothing takes this stripe and then the
// merge lock.
//
// NOT YET HELD BY, so a write from these can still land between another
// holder's read and write: readstatus.RecomputeUserBookState /
// RebuildUserBookState and the position writes that precede them (the web
// reading heartbeat in handlers/reading.go, the iTunes position sync's
// position write), the iTunes position backfill job, and the merge follow /
// combine carry itself (which holds merge.LockMergeRMW only). Every holder above
// re-reads under the lock and compares, so such a write is caught when it
// lands first; one that lands inside the window is not.
//
// Striped, not per key: two keys can share a stripe, so a holder must never
// take a second user-state lock while holding one (none does). Not reentrant.
func LockUserBookState(userID, bookID string) (unlock func()) {
	h := fnv.New32a()
	_, _ = h.Write([]byte(userID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(bookID))
	mu := &userStateLocks[h.Sum32()%userStateStripes]
	mu.Lock()
	return mu.Unlock
}
