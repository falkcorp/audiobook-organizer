// file: internal/merge/sync_follow.go
// version: 1.0.0
// guid: 50421381-9def-4b19-bd23-6fa1a03c24d3
// last-edited: 2026-07-30

// Package merge: sync-identity follow hooks.
//
// The ABS-compatible sync layer exposes a durable `libraryItemId` (a syncID,
// see internal/database/pebble_store_syncid.go) rather than the raw Book ULID,
// because this app's core loop -- moving, retagging and merging books --
// churns ULIDs. Every code path that retires or replaces a Book ULID must
// therefore carry the syncID (and the per-user listening position keyed to the
// old ULID) forward, or a device's place in a book is silently orphaned. On the
// HARD-delete paths (dedup.MergeBooks, CombineBooks) there is no surviving row
// to repoint afterwards, so an un-followed merge there is unrecoverable.
//
// See docs/specs/2026-07-29-abs-sync-api-design.md §4.2 (model), §4.3 (test
// bar) and §5.5 (progress on merge).
package merge

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// SyncFollower is satisfied by anything that can record a merge in the
// sync-identity layer (internal/database's SyncIdentityStore). Optional: nil
// is a valid, no-op value -- a store that does not implement it (a mock, a
// MemStore) simply means merges do not touch sync identity.
type SyncFollower interface {
	MintOrGetSyncID(bookID string) (string, error)
	RecordSyncMerge(loserBookID, winnerBookID string) error
}

// syncRepointMu serializes FollowBookIDChange's read-then-repoint. Unlike the
// merge paths (which run inside mergeSerializeMu already), the scanner's
// version-link path is NOT under any merge lock and runs on parallel scan
// workers, and RepointSyncItem's own GetSyncIDForBook -> batch-commit is not
// atomic. Without this, two workers version-linking against the SAME
// predecessor book could both read its syncID and both write a reverse index,
// leaving two live books pointing at one syncID. Deliberately NOT
// mergeSerializeMu: that lock is held across whole merges and a scan worker
// blocking on it (or vice versa) is a needless coupling of two unrelated
// subsystems.
var syncRepointMu sync.Mutex

// FollowMerge carries sync identity and per-user progress from every loser
// onto the winner of a merge. Call it while still holding whatever lock
// guards the merge's read-modify-write, BEFORE any hard delete of a loser row.
//
// Best-effort by design, matching this package's convention for optional
// side-effects (eidStore, writeBackBatcher): a sync-identity hiccup must never
// fail a merge that would otherwise succeed. Every failure is logged at
// ERROR with both book IDs, because a silent failure here loses a user's
// listening position with no trace.
//
// Exactly-once: the store primitives it calls are idempotent
// (MintOrGetSyncID returns the existing id; RecordSyncMerge returns early when
// the redirect is already recorded and de-duplicates MergedFrom), so a retried
// or concurrently repeated merge cannot double-redirect or grow a chain.
func FollowMerge(db database.Store, follower SyncFollower, winnerBookID string, loserBookIDs []string) {
	if follower == nil || db == nil || winnerBookID == "" {
		return
	}

	// Ensure the winner has a client-visible identity before redirecting
	// anything at it. If this fails, redirecting a loser would point at
	// nothing, so stop here rather than record a dangling redirect.
	if _, err := follower.MintOrGetSyncID(winnerBookID); err != nil {
		slog.Error("sync-identity merge-follow: could not mint winner syncID; losers NOT redirected",
			"winner", winnerBookID, "losers", loserBookIDs, "err", err)
		return
	}

	for _, loserID := range loserBookIDs {
		if loserID == "" || loserID == winnerBookID {
			continue
		}
		if err := follower.RecordSyncMerge(loserID, winnerBookID); err != nil {
			slog.Error("sync-identity merge-follow: redirect NOT recorded; a client holding the loser's id will not resolve",
				"loser", loserID, "winner", winnerBookID, "err", err)
		}
		if err := mergeUserProgress(db, loserID, winnerBookID); err != nil {
			slog.Error("sync-identity merge-follow: progress NOT merged onto winner",
				"loser", loserID, "winner", winnerBookID, "err", err)
		}
	}
}

// FollowMergeWithStore is FollowMerge for callers that only hold a
// database.Store and cannot reach a *Service (dedup.MergeBooks). It derives
// the follower by type assertion; a store that does not implement
// SyncIdentityStore yields a nil follower and the whole call is a no-op.
func FollowMergeWithStore(db database.Store, winnerBookID string, loserBookIDs []string) {
	follower := database.AsSyncIdentityStore(db)
	if follower == nil {
		// Warn, not debug: the only in-tree caller is a HARD-delete merge, so
		// a silently-skipped follow there is unrecoverable.
		slog.Warn("sync-identity merge-follow: store does not implement SyncIdentityStore; this merge will NOT carry identity or progress",
			"winner", winnerBookID, "losers", loserBookIDs)
	}
	FollowMerge(db, follower, winnerBookID, loserBookIDs)
}

// FollowBookIDChange carries a book's sync identity and per-user progress from
// oldBookID to newBookID when a Book ULID is REPLACED rather than merged --
// the untagged-move case, where a moved file is re-scanned, matched to its
// predecessor by hash, and a brand-new ULID is minted for the new path
// (internal/scanner's version-link path). Both rows survive there, so this is
// a repoint (RepointSyncItem), not a redirect.
//
// No-op when oldBookID has no syncID yet: there is no client-visible identity
// to carry, and in that case the per-user progress rows are deliberately left
// where they are -- moving them would change long-standing scanner behaviour
// for installs that do not use the sync layer at all.
//
// Idempotent: a second call finds no syncID on oldBookID (the reverse index
// moved) and returns without touching anything.
func FollowBookIDChange(db database.Store, oldBookID, newBookID string) {
	if db == nil || oldBookID == "" || newBookID == "" || oldBookID == newBookID {
		return
	}
	ids := database.AsSyncIdentityStore(db)
	if ids == nil {
		// Debug, not warn (unlike FollowMergeWithStore): both rows survive a
		// version-link, so a skipped follow here is recoverable by repointing
		// later, and this runs on every hash-duplicate import.
		slog.Debug("sync-identity follow: store does not implement SyncIdentityStore; skipping repoint",
			"old_book", oldBookID, "new_book", newBookID)
		return
	}

	syncRepointMu.Lock()
	defer syncRepointMu.Unlock()

	syncID, has, err := ids.GetSyncIDForBook(oldBookID)
	if err != nil {
		slog.Error("sync-identity follow: could not read the old book's syncID; identity NOT carried forward",
			"old_book", oldBookID, "new_book", newBookID, "err", err)
		return
	}
	if !has {
		return
	}

	if err := ids.RepointSyncItem(oldBookID, newBookID); err != nil {
		slog.Error("sync-identity follow: repoint FAILED; clients still resolve to the retired book id",
			"sync_id", syncID, "old_book", oldBookID, "new_book", newBookID, "err", err)
		return
	}
	// The identity now resolves to newBookID, so progress keyed to oldBookID
	// would look lost to a client. It is still on disk under the old id (this
	// only fails forward, never destroys), but it must be logged loudly.
	if err := mergeUserProgress(db, oldBookID, newBookID); err != nil {
		slog.Error("sync-identity follow: identity repointed but progress NOT migrated; positions still keyed to the old book id",
			"sync_id", syncID, "old_book", oldBookID, "new_book", newBookID, "err", err)
		return
	}
	slog.Info("sync-identity followed a book id change",
		"sync_id", syncID, "old_book", oldBookID, "new_book", newBookID)
}

// mergeUserProgress merges every user's listening progress on loserBookID onto
// winnerBookID, then drains the loser side.
//
// The loop is over USERS, not books: UserBookState/UserPosition are keyed
// user-first (`ubs:<userID>:<bookID>`, `upos:<userID>:<bookID>:<segmentID>`)
// with no book -> users reverse index, so there is no way to ask "who has
// progress on this book". This is bounded by the number of accounts on the
// instance (a household, not the library), so a plain sequential loop is
// correct here -- deliberately NOT a worker pool, per CLAUDE.md's own
// "whole-library-scale" threshold.
func mergeUserProgress(db database.Store, loserBookID, winnerBookID string) error {
	users, err := db.ListUsers()
	if err != nil {
		return fmt.Errorf("list users: %w", err)
	}
	var firstErr error
	for _, u := range users {
		if u.ID == "" {
			continue
		}
		if err := mergeUserProgressFor(db, u.ID, loserBookID, winnerBookID); err != nil {
			// Keep going: one user's failure must not strand every other
			// user's position.
			slog.Error("sync-identity merge-follow: progress merge failed for one user",
				"user", u.ID, "loser", loserBookID, "winner", winnerBookID, "err", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// mergeUserProgressFor merges one user's progress from loser onto winner.
//
// "Furthest position" rule (deliberately narrower than §5's full live-PATCH
// policy, which resolves two updates to the SAME item): compare
// UserBookState.ProgressPct, an already-normalized 0-100 value that is
// comparable across two different-length books -- raw PositionSeconds is not,
// since two books have unrelated segment schemes and durations. Ties go to the
// more recent LastActivityAt.
//
// The loser side is drained ONLY after a successful copy, so a mid-way store
// failure can never destroy the position it was supposed to carry forward.
func mergeUserProgressFor(db database.Store, userID, loserBookID, winnerBookID string) error {
	loserState, err := db.GetUserBookState(userID, loserBookID)
	if err != nil {
		return fmt.Errorf("get loser state: %w", err)
	}
	loserPositions, err := db.ListUserPositionsForBook(userID, loserBookID)
	if err != nil {
		return fmt.Errorf("list loser positions: %w", err)
	}
	if loserState == nil && len(loserPositions) == 0 {
		return nil // nothing to merge for this user
	}

	winnerState, err := db.GetUserBookState(userID, winnerBookID)
	if err != nil {
		return fmt.Errorf("get winner state: %w", err)
	}

	if loserIsFurther(loserState, winnerState) {
		if loserState != nil {
			carried := *loserState
			carried.BookID = winnerBookID
			if err := db.SetUserBookState(&carried); err != nil {
				return fmt.Errorf("carry state onto winner: %w", err)
			}
		}
		// Segment IDs are opaque per-user bookkeeping, not meaningfully
		// comparable across books either way, so they are carried as-is.
		for _, pos := range loserPositions {
			if err := db.SetUserPosition(userID, winnerBookID, pos.SegmentID, pos.PositionSeconds); err != nil {
				return fmt.Errorf("carry position %s onto winner: %w", pos.SegmentID, err)
			}
		}
	}

	// Drain the loser regardless of which side won: its book is merged away,
	// and progress that is only resolvable under a retired book id is a leak
	// (it can still surface in a per-status listing for a book that no longer
	// exists).
	if len(loserPositions) > 0 {
		if err := db.ClearUserPositions(userID, loserBookID); err != nil {
			return fmt.Errorf("clear loser positions: %w", err)
		}
	}
	if loserState != nil {
		// There is no DeleteUserBookState in the database layer, so the row is
		// neutralized in place. An empty Status also removes it from the
		// ubs status index (see PebbleStore.SetUserBookState), which is what
		// keeps a merged-away book out of "in progress" listings.
		drained := *loserState
		drained.Status = ""
		drained.StatusManual = false
		drained.ProgressPct = 0
		drained.TotalListenedSeconds = 0
		drained.LastSegmentID = ""
		if err := db.SetUserBookState(&drained); err != nil {
			return fmt.Errorf("drain loser state: %w", err)
		}
	}
	return nil
}

// loserIsFurther reports whether the losing book's state should win. A winner
// with no state at all always loses (nothing to preserve on that side); two
// nil states mean there is only position data to carry.
func loserIsFurther(loserState, winnerState *database.UserBookState) bool {
	if winnerState == nil {
		return true
	}
	if loserState == nil {
		return false
	}
	if loserState.ProgressPct != winnerState.ProgressPct {
		return loserState.ProgressPct > winnerState.ProgressPct
	}
	return loserState.LastActivityAt.After(winnerState.LastActivityAt)
}
