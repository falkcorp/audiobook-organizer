// file: internal/database/user_state_lock.go
// version: 1.0.0
// guid: d3079a7f-dd6e-4d4f-8f7d-f9f740ba41e0
// last-edited: 2026-10-05

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
// HELD BY (2026-10-05):
//   - the ABS write paths (handlers/abs): updateUserBookState,
//     applyProgressUpdate (PATCH/batch progress), persistProgress (session
//     sync), applyLocalSession (offline replay) and the progress reset;
//   - repairs.Writer.SetUserState (the Audible read-status import);
//   - the revert of an undo.ChangeTypeUserBookStateSet row (taken inside
//     merge.LockMergeRMW, the one lock ever held around it).
//
// NOT YET HELD BY, so a write from these can still land between another
// holder's read and write: readstatus.RecomputeUserBookState / SetManualStatus
// / RebuildUserBookState and the position writes that precede them (the web
// reading heartbeat in handlers/reading.go, the iTunes position sync), the
// iTunes position backfill job, and the merge follow / combine paths (which
// hold merge.LockMergeRMW instead). Every holder above re-reads under the lock
// and compares, so such a write is caught when it lands first; one that
// lands inside the window is not.
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
