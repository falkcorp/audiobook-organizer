// file: internal/merge/pending_repair.go
// version: 1.0.2
// guid: 4c8a2e71-9f3d-4b06-8e5a-1d7c3b9f2e84
// last-edited: 2026-09-26

package merge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// PendingUserStateRepairPrefix keys the durable record a merge follow leaves
// when it could not move every user's state (upos, ubs, bookmarks) from a
// merged-away book onto its survivor.
//
// The record is written BEFORE the follow moves anything and deleted only
// after the follow moved everything, so a crash, a store error, or a user
// whose rows could not be read (and so was not journaled or moved) leaves it
// behind. maintenance.repair-merged-user-state completes it on a schedule
// (CompletePendingUserStateRepair). Until 2026-09-26 a failed move was only
// logged: the state stayed under the merged-away id, GET progress on the
// survivor answered 404, and nothing ever came back for it.
const PendingUserStateRepairPrefix = "merge_user_state_pending:"

// PendingUserStateRepair is one loser -> survivor move still owed.
type PendingUserStateRepair struct {
	LoserBookID  string    `json:"loser_book_id"`
	WinnerBookID string    `json:"winner_book_id"`
	RecordedAt   time.Time `json:"recorded_at"`
	// Slice is set for a chapter / split-part merge: the sweep must apply the
	// slice rule (followSliceFor), not the whole-book rule.
	Slice *SliceMapping `json:"slice,omitempty"`
	// BookIDChange is set by FollowBookIDChange (a version-link, not a
	// merge): the old row stays live by design, so completion does not defer
	// on a live loser and records no sync redirect (the identity was repointed).
	BookIDChange bool `json:"book_id_change,omitempty"`
}

func pendingRepairKey(loserBookID, winnerBookID string) string {
	return PendingUserStateRepairPrefix + loserBookID + ":" + winnerBookID
}

// pendingRepairKV is the raw-key surface the record needs.
type pendingRepairKV interface {
	SetRaw(key string, value []byte) error
	DeleteRaw(key string) error
}

func putPendingRepair(db pendingRepairKV, rec PendingUserStateRepair) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal pending user-state repair: %w", err)
	}
	if err := db.SetRaw(pendingRepairKey(rec.LoserBookID, rec.WinnerBookID), data); err != nil {
		return fmt.Errorf("write pending user-state repair %s->%s: %w", rec.LoserBookID, rec.WinnerBookID, err)
	}
	return nil
}

// ListPendingUserStateRepairs returns every recorded repair. An undecodable
// record is returned in errs, never dropped: it names a move still owed.
func ListPendingUserStateRepairs(db interface {
	ScanPrefix(prefix string) ([]database.KVPair, error)
}) (recs []PendingUserStateRepair, undecodable []string, err error) {
	rows, err := db.ScanPrefix(PendingUserStateRepairPrefix)
	if err != nil {
		return nil, nil, fmt.Errorf("list pending user-state repairs: %w", err)
	}
	for _, r := range rows {
		var rec PendingUserStateRepair
		if uerr := json.Unmarshal(r.Value, &rec); uerr != nil || rec.LoserBookID == "" || rec.WinnerBookID == "" {
			undecodable = append(undecodable, r.Key)
			continue
		}
		recs = append(recs, rec)
	}
	return recs, undecodable, nil
}

// CompletePendingUserStateRepair re-runs the follow for one record over EVERY
// user and deletes the record when it succeeds. The survivor is resolved
// forward first (it may itself have been merged away since), via
// ResolveSurvivor. Caller holds LockMergeRMW or is otherwise exclusive with
// merges touching the same books.
func CompletePendingUserStateRepair(db UserProgressMerger, rec PendingUserStateRepair) error {
	winner, err := ResolveSurvivor(db, rec.WinnerBookID)
	if err != nil {
		return fmt.Errorf("resolve survivor of %s: %w", rec.WinnerBookID, err)
	}
	if rec.BookIDChange {
		if err := mergeUserProgress(db, rec.LoserBookID, winner); err != nil {
			return err
		}
		if err := db.DeleteRaw(pendingRepairKey(rec.LoserBookID, rec.WinnerBookID)); err != nil {
			return fmt.Errorf("state moved but pending record not deleted (a re-run is a no-op): %w", err)
		}
		return nil
	}
	// A loser that is still live was not retired (the merge failed after the
	// follow, or was undone): draining it now would strip state off a book
	// the user can still see. The record stays; a re-run of the merge or an
	// undo resolves it.
	if loser, err := db.GetBookByID(rec.LoserBookID); err != nil {
		return fmt.Errorf("read loser %s: %w", rec.LoserBookID, err)
	} else if loser != nil && !loser.IsSoftDeleted() {
		return fmt.Errorf("%w: %s", ErrPendingLoserLive, rec.LoserBookID)
	}
	follower := asFollower(db)
	if moveErr := moveLoserState(db, follower, winner, rec.LoserBookID, nil, rec.Slice); moveErr != nil {
		return moveErr
	}
	if err := db.DeleteRaw(pendingRepairKey(rec.LoserBookID, rec.WinnerBookID)); err != nil {
		return fmt.Errorf("state moved but pending record not deleted (a re-run is a no-op): %w", err)
	}
	return nil
}

// asFollower returns db's sync-identity capability as a SyncFollower, or nil.
// A nil *interface* (not a typed nil) is what followMerge checks for.
func asFollower(db any) SyncFollower {
	if ids := database.AsSyncIdentityStore(db); ids != nil {
		return ids
	}
	return nil
}

// ErrPendingLoserLive: the record's loser is live again (the merge failed
// after the follow). The record is kept and the item reported as deferred,
// not as a failure.
var ErrPendingLoserLive = errors.New("loser is still live; record kept until the merge completes")

// ErrNoLiveSurvivor is returned by ResolveSurvivor when the chain from a book
// does not end at a live (not soft-deleted) book.
var ErrNoLiveSurvivor = errors.New("merge: no live survivor")

// ResolveSurvivor follows a book forward to the live book that now holds it:
// the sync redirect chain first (the identity a client resolves), then
// MergedIntoBookID. Returns bookID itself when it is live. Capped at 10 hops.
func ResolveSurvivor(db interface {
	GetBookByID(id string) (*database.Book, error)
}, bookID string) (string, error) {
	ids := database.AsSyncIdentityStore(db)
	cur := bookID
	seen := map[string]bool{}
	for range 10 {
		if seen[cur] {
			break
		}
		seen[cur] = true
		b, err := db.GetBookByID(cur)
		if err != nil {
			return "", err
		}
		if b != nil && !b.IsSoftDeleted() {
			return cur, nil
		}
		next := ""
		if ids != nil {
			if sid, has, err := ids.GetSyncIDForBook(cur); err != nil {
				return "", err
			} else if has {
				item, err := ids.ResolveSyncItem(sid)
				if err != nil && !errors.Is(err, database.ErrSyncRedirectChainBroken) {
					return "", err
				}
				if item != nil && item.CurrentBookID != "" && item.CurrentBookID != cur {
					next = item.CurrentBookID
				}
			}
		}
		if next == "" && b != nil && b.MergedIntoBookID != nil && *b.MergedIntoBookID != "" && *b.MergedIntoBookID != cur {
			next = *b.MergedIntoBookID
		}
		if next == "" {
			break
		}
		cur = next
	}
	return "", fmt.Errorf("%w for %s", ErrNoLiveSurvivor, bookID)
}

// logPendingLeft records, once per loser, that a pending record now holds the move.
func logPendingLeft(loserBookID, winnerBookID string, cause error) {
	mlog.Error("merge-follow: user state of loser=%s NOT fully moved to winner=%s; pending-repair record %s holds it for maintenance.repair-merged-user-state: %s",
		logger.SanitizeLogValue(loserBookID), logger.SanitizeLogValue(winnerBookID),
		logger.SanitizeLogValue(pendingRepairKey(loserBookID, winnerBookID)),
		logger.SanitizeLogValue(fmt.Sprint(cause)))
}

// PendingSweepResult counts one pass over the pending records.
type PendingSweepResult struct {
	Completed, Deferred, Failed, Skipped, Undecodable int
	// Remaining is the number of records left after the pass.
	Remaining int
}

// CompletePendingUserStateRepairs completes every pending record accepted by
// keep, one at a time. The caller holds LockMergeRMW (a merge in progress)
// or takes it per record via lock (the ticker). A record whose loser is live
// again is deferred and kept; any other failure is logged and kept.
func CompletePendingUserStateRepairs(db UserProgressMerger, keep func(PendingUserStateRepair) bool, lock func() func()) (PendingSweepResult, error) {
	var res PendingSweepResult
	recs, undecodable, err := ListPendingUserStateRepairs(db)
	if err != nil {
		return res, err
	}
	res.Undecodable = len(undecodable)
	for _, rec := range recs {
		if keep != nil && !keep(rec) {
			res.Skipped++
			continue
		}
		unlock := func() {}
		if lock != nil {
			unlock = lock()
		}
		err := CompletePendingUserStateRepair(db, rec)
		unlock()
		switch {
		case err == nil:
			res.Completed++
		case errors.Is(err, ErrPendingLoserLive):
			res.Deferred++
		default:
			res.Failed++
			mlog.Warn("merge user-state repair: record %s -> %s not completed: %s",
				logger.SanitizeLogValue(rec.LoserBookID), logger.SanitizeLogValue(rec.WinnerBookID), logger.SanitizeLogValue(fmt.Sprint(err)))
		}
	}
	res.Remaining = res.Undecodable + res.Skipped + res.Deferred + res.Failed
	return res, nil
}

// completePendingInvolving completes, before a merge moves anything, the
// pending records left by earlier merges of the same books, so a merge never
// builds on top of a move that is still owed. Runs under the caller's merge
// lock. Best-effort: whatever it cannot complete stays recorded for the ticker.
func completePendingInvolving(db UserProgressMerger, bookIDs []string) {
	involved := make(map[string]bool, len(bookIDs))
	for _, id := range bookIDs {
		if id != "" {
			involved[id] = true
		}
	}
	if _, err := CompletePendingUserStateRepairs(db, func(r PendingUserStateRepair) bool {
		return involved[r.LoserBookID] || involved[r.WinnerBookID]
	}, nil); err != nil {
		mlog.Warn("merge: could not list pending user-state repairs before the merge: %s", logger.SanitizeLogValue(fmt.Sprint(err)))
	}
}

// PendingRepairLoop completes pending records older than minAge every
// interval until ctx is done, each record under the merge lock. onPass, when
// set, gets each pass's result (the server feeds the pending-count gauge).
// It returns when ctx is cancelled.
func PendingRepairLoop(ctx context.Context, db UserProgressMerger, interval, minAge time.Duration, onPass func(PendingSweepResult)) {
	t := time.NewTicker(interval)
	defer t.Stop()
	pass := func() {
		cutoff := time.Now().Add(-minAge)
		res, err := CompletePendingUserStateRepairs(db,
			func(r PendingUserStateRepair) bool { return !r.RecordedAt.After(cutoff) },
			func() func() { LockMergeRMW(); return UnlockMergeRMW })
		if err != nil {
			mlog.Warn("merge user-state repair ticker: %s", logger.SanitizeLogValue(fmt.Sprint(err)))
			return
		}
		if res.Completed+res.Deferred+res.Failed > 0 {
			mlog.Info("merge user-state repair ticker: completed=%d deferred=%d failed=%d remaining=%d",
				res.Completed, res.Deferred, res.Failed, res.Remaining)
		}
		if onPass != nil {
			onPass(res)
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pass()
		}
	}
}
