// file: internal/merge/pending_repair.go
// version: 1.0.0
// guid: 4c8a2e71-9f3d-4b06-8e5a-1d7c3b9f2e84
// last-edited: 2026-09-26

package merge

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
// (CompletePendingUserStateRepairs). Until 2026-09-26 a failed move was only
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
	// A loser that is still live was not retired (the merge failed after the
	// follow, or was undone): draining it now would strip state off a book
	// the user can still see. The record stays; a re-run of the merge or an
	// undo resolves it.
	if loser, err := db.GetBookByID(rec.LoserBookID); err != nil {
		return fmt.Errorf("read loser %s: %w", rec.LoserBookID, err)
	} else if loser != nil && !loser.IsSoftDeleted() {
		return fmt.Errorf("loser %s is still live; record kept until the merge completes", rec.LoserBookID)
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
		logger.SanitizeLogValue(strings.TrimSpace(pendingRepairKey(loserBookID, winnerBookID))),
		logger.SanitizeLogValue(fmt.Sprint(cause)))
}
