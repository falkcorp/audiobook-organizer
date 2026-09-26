// file: internal/merge/repair_user_state.go
// version: 1.0.0
// guid: 6f3c1a92-8e5b-4d07-b4a1-3c9e7f2d5b18
// last-edited: 2026-09-26

package merge

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"golang.org/x/sync/errgroup"
)

// UserStateRepairStore is what RepairMergedUserState needs: the follow's own
// surface plus the reads that find stranded rows. Every method is on
// database.Store, so the production indexedStore forwards all of them.
type UserStateRepairStore interface {
	UserProgressMerger
	ScanPrefix(prefix string) ([]database.KVPair, error)
	ListUserPositionsSince(userID string, t time.Time) ([]database.UserPosition, error)
}

// UserStateRepairOptions selects what RepairMergedUserState does.
type UserStateRepairOptions struct {
	// Apply moves state. False (the default) only reports.
	Apply bool
	// PendingOnly restricts the run to the durable pending-repair records a
	// failed merge follow left (pending_repair.go). The scheduled sweep uses
	// it: those are explicit loser -> survivor pairs, so completing them
	// needs no inference.
	PendingOnly bool
}

// UserStateRepairItem is one (user, stranded book) or pending record.
type UserStateRepairItem struct {
	Kind       string `json:"kind"` // "pending", "state", "bookmarks"
	UserID     string `json:"user_id,omitempty"`
	FromBookID string `json:"from_book_id,omitempty"`
	ToBookID   string `json:"to_book_id,omitempty"`
	FromSyncID string `json:"from_sync_id,omitempty"`
	ToSyncID   string `json:"to_sync_id,omitempty"`
	HasState   bool   `json:"has_state,omitempty"`
	Positions  int    `json:"positions,omitempty"`
	Bookmarks  int    `json:"bookmarks,omitempty"`
	Moved      bool   `json:"moved"`
	Error      string `json:"error,omitempty"`
}

// maxRepairItems caps the item list kept on the report; the counts are
// always complete.
const maxRepairItems = 1000

// UserStateRepairReport is the op's result. Counts are exact; Items is
// capped at maxRepairItems (ItemsTruncated says so).
type UserStateRepairReport struct {
	Apply              bool                  `json:"apply"`
	PendingOnly        bool                  `json:"pending_only"`
	Users              int                   `json:"users"`
	PendingRecords     int                   `json:"pending_records"`
	PendingCompleted   int                   `json:"pending_completed"`
	PendingUndecodable int                   `json:"pending_undecodable"`
	StrandedBooks      int                   `json:"stranded_books"`
	StateRows          int                   `json:"state_rows"`
	PositionRows       int                   `json:"position_rows"`
	StateMoved         int                   `json:"state_moved"`
	BookmarksOwed      int                   `json:"bookmarks_owed"`
	BookmarksCopied    int                   `json:"bookmarks_copied"`
	UnresolvedBooks    int                   `json:"unresolved_books"`
	UnresolvedRows     int                   `json:"unresolved_rows"`
	UndecodableRows    int                   `json:"undecodable_rows"`
	Errors             int                   `json:"errors"`
	Items              []UserStateRepairItem `json:"items"`
	ItemsTruncated     bool                  `json:"items_truncated,omitempty"`
}

func (r *UserStateRepairReport) add(it UserStateRepairItem) {
	if it.Error != "" {
		r.Errors++
	}
	if len(r.Items) >= maxRepairItems {
		r.ItemsTruncated = true
		return
	}
	r.Items = append(r.Items, it)
}

// strandedRef is one user's state on one book, found by the scan.
type strandedRef struct {
	userID    string
	bookID    string
	hasState  bool
	positions int
}

// RepairMergedUserState finds user state (ubs, upos, bookmarks) still stored
// under merged-away, soft-deleted or purged book ids whose live survivor is
// known (ResolveSurvivor: sync redirect chain, then MergedIntoBookID), and
// with opts.Apply moves it using the same conflict rule as a merge
// (user_state_merge.go). Without Apply it writes nothing.
//
// A book whose survivor cannot be resolved is reported (UnresolvedBooks) and
// left alone: a soft-deleted book with no merge link may be one the user
// deleted, and guessing a survivor would put their position on a book they
// never listened to.
func RepairMergedUserState(ctx context.Context, db UserStateRepairStore, opts UserStateRepairOptions) (*UserStateRepairReport, error) {
	rep := &UserStateRepairReport{Apply: opts.Apply, PendingOnly: opts.PendingOnly, Items: []UserStateRepairItem{}}

	// 1. Pending records a failed follow left.
	recs, undecodable, err := ListPendingUserStateRepairs(db)
	if err != nil {
		return nil, err
	}
	rep.PendingRecords = len(recs)
	rep.PendingUndecodable = len(undecodable)
	for _, rec := range recs {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		it := UserStateRepairItem{Kind: "pending", FromBookID: rec.LoserBookID, ToBookID: rec.WinnerBookID}
		if opts.Apply {
			// Under the merge lock: the record's books may be mid-merge.
			LockMergeRMW()
			err := CompletePendingUserStateRepair(db, rec)
			UnlockMergeRMW()
			if err != nil {
				it.Error = err.Error()
			} else {
				it.Moved = true
				rep.PendingCompleted++
			}
		}
		rep.add(it)
	}
	if opts.PendingOnly {
		return rep, nil
	}

	users, err := db.ListUsers()
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	rep.Users = len(users)

	// 2. Every book some user has ubs/upos on.
	var refs []strandedRef
	bookSet := map[string]bool{}
	for _, u := range users {
		if u.ID == "" {
			continue
		}
		userRefs, bad, err := userStateRefs(db, u.ID)
		if err != nil {
			return nil, fmt.Errorf("scan state of user %s: %w", u.ID, err)
		}
		rep.UndecodableRows += bad
		for _, r := range userRefs {
			bookSet[r.bookID] = true
		}
		refs = append(refs, userRefs...)
	}

	// 3. Resolve each distinct book to its live survivor. Library-scale (one
	// entry per book any user touched), a point read or two each, so it runs
	// on a bounded pool (CLAUDE.md). Each worker writes only its own map
	// entry under mu.
	survivor, unresolved, err := resolveStrandedBooks(ctx, db, bookSet)
	if err != nil {
		return rep, err
	}

	// 4. Report / move. Sequential on purpose: the set is small (only rows on
	// dead books), and each move is a read-modify-write of the SURVIVOR's
	// row, which two stranded books merged into one survivor would race on.
	// Each move holds LockMergeRMW so it cannot interleave with a live merge.
	stranded := map[string]bool{}
	for _, r := range refs {
		to, ok := survivor[r.bookID]
		if !ok {
			if unresolved[r.bookID] {
				rep.UnresolvedRows++
			}
			continue
		}
		stranded[r.bookID] = true
		if r.hasState {
			rep.StateRows++
		}
		rep.PositionRows += r.positions
		it := UserStateRepairItem{Kind: "state", UserID: r.userID, FromBookID: r.bookID, ToBookID: to, HasState: r.hasState, Positions: r.positions}
		if opts.Apply {
			if err := ctx.Err(); err != nil {
				return rep, err
			}
			LockMergeRMW()
			err := mergeUserProgressFor(db, r.userID, r.bookID, to)
			UnlockMergeRMW()
			if err != nil {
				it.Error = err.Error()
			} else {
				it.Moved = true
				rep.StateMoved++
			}
		}
		rep.add(it)
	}
	rep.StrandedBooks = len(stranded)
	rep.UnresolvedBooks = len(unresolved)

	// 5. Bookmarks still only under an alias of their item.
	if err := repairAliasBookmarks(ctx, db, users, opts.Apply, rep); err != nil {
		return rep, err
	}
	return rep, nil
}

// userStateRefs lists the books one user has carryable state or positions on.
func userStateRefs(db UserStateRepairStore, userID string) ([]strandedRef, int, error) {
	byBook := map[string]*strandedRef{}
	bad := 0
	prefix := "ubs:" + userID + ":"
	rows, err := db.ScanPrefix(prefix)
	if err != nil {
		return nil, 0, err
	}
	for _, kv := range rows {
		var st database.UserBookState
		if err := json.Unmarshal(kv.Value, &st); err != nil {
			bad++
			continue
		}
		bookID := strings.TrimPrefix(kv.Key, prefix)
		if bookID == "" || !hasCarryableState(&st, nil) {
			continue
		}
		byBook[bookID] = &strandedRef{userID: userID, bookID: bookID, hasState: true}
	}
	positions, err := db.ListUserPositionsSince(userID, time.Time{})
	if err != nil {
		return nil, 0, err
	}
	for _, p := range positions {
		r := byBook[p.BookID]
		if r == nil {
			r = &strandedRef{userID: userID, bookID: p.BookID}
			byBook[p.BookID] = r
		}
		r.positions++
	}
	out := make([]strandedRef, 0, len(byBook))
	for _, r := range byBook {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].bookID < out[j].bookID })
	return out, bad, nil
}

// resolveStrandedBooks maps each dead book (missing or soft-deleted) to its
// live survivor, or marks it unresolved. Live books are in neither map.
func resolveStrandedBooks(ctx context.Context, db UserStateRepairStore, books map[string]bool) (survivor map[string]string, unresolved map[string]bool, err error) {
	survivor = map[string]string{}
	unresolved = map[string]bool{}
	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.NumCPU())
	for bookID := range books {
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			b, err := db.GetBookByID(bookID)
			if err != nil {
				return fmt.Errorf("read book %s: %w", bookID, err)
			}
			if b != nil && !b.IsSoftDeleted() {
				return nil
			}
			to, rerr := ResolveSurvivor(db, bookID)
			mu.Lock()
			defer mu.Unlock()
			if rerr != nil || to == bookID {
				unresolved[bookID] = true
				return nil
			}
			survivor[bookID] = to
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, nil, err
	}
	return survivor, unresolved, nil
}

// repairAliasBookmarks copies (or counts) bookmarks stored under an item id
// that now redirects, onto the item it resolves to.
func repairAliasBookmarks(ctx context.Context, db UserStateRepairStore, users []database.User, apply bool, rep *UserStateRepairReport) error {
	bs := database.AsBookmarkStore(db)
	ids := database.AsSyncIdentityStore(db)
	if bs == nil || ids == nil {
		return nil
	}
	for _, u := range users {
		if u.ID == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		marks, err := bs.ListBookmarksForUser(u.ID)
		if err != nil {
			return fmt.Errorf("list bookmarks of user %s: %w", u.ID, err)
		}
		items := map[string]bool{}
		for _, m := range marks {
			items[m.ItemID] = true
		}
		// canonical syncID -> the alias item ids found under it.
		canon := map[string][]string{}
		for itemID := range items {
			resolved, err := ids.ResolveSyncItem(itemID)
			if err != nil || resolved == nil || resolved.SyncID == itemID {
				continue // live item, unknown id, or broken chain: nothing to move to
			}
			canon[resolved.SyncID] = append(canon[resolved.SyncID], itemID)
		}
		keys := make([]string, 0, len(canon))
		for k := range canon {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, to := range keys {
			from := canon[to]
			sort.Strings(from)
			res, err := CopyAliasBookmarks(db, u.ID, to, from, !apply)
			it := UserStateRepairItem{Kind: "bookmarks", UserID: u.ID, FromSyncID: strings.Join(from, ","), ToSyncID: to, Bookmarks: res.Owed, Moved: apply && err == nil && res.Owed > 0}
			if err != nil {
				it.Error = err.Error()
			}
			rep.BookmarksOwed += res.Owed
			rep.BookmarksCopied += res.Copied
			if res.Owed > 0 || err != nil {
				rep.add(it)
			}
		}
	}
	return nil
}
