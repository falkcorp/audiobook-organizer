// file: internal/merge/bookmark_copy.go
// version: 1.0.0
// guid: 7e2d9a14-3b6c-4f58-b0e1-8c4a5d2f9b37
// last-edited: 2026-09-26

package merge

import (
	"errors"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/syncapi/progress"
)

// Bookmarks across a merge.
//
// A bookmark is keyed by (user, libraryItemId, time), and libraryItemId is a
// syncID. A merge redirects the loser's syncID to the survivor's but never
// moved the bookmarks, so they were reachable only through the alias-set read
// in internal/server/handlers/abs/bookmarks.go. They are now COPIED onto the
// survivor's syncID, de-duplicated by time (a bookmark the survivor already
// has at that instant wins untouched).
//
// Copied, not moved: the loser's rows stay under its syncID, so UndoCombine
// (which clears the redirect) gives the restored book its own bookmarks back
// with no journal change, and the alias read keeps working -- it collapses the
// two rows at one instant into one (bookmarkDTOs), and a delete or rename
// through the survivor acts on every holder (bookmarkHolders).

// BookmarkCopyResult counts one copy pass.
type BookmarkCopyResult struct {
	// Owed is the number of alias bookmarks the canonical item lacked.
	Owed int
	// Copied is how many of those were written (0 in a dry run).
	Copied int
}

// CopyAliasBookmarks copies userID's bookmarks from every alias of
// canonicalSyncID (plus extraSources, e.g. a loser whose redirect has not been
// recorded yet) onto canonicalSyncID. dryRun counts without writing. A store
// with no bookmark keyspace has nothing to copy and returns zero.
func CopyAliasBookmarks(db any, userID, canonicalSyncID string, extraSources []string, dryRun bool) (BookmarkCopyResult, error) {
	var res BookmarkCopyResult
	bs := database.AsBookmarkStore(db)
	if bs == nil || userID == "" || canonicalSyncID == "" {
		return res, nil
	}
	ids := database.AsSyncIdentityStore(db)
	if ids == nil {
		return res, nil
	}
	aliases, err := ids.ListSyncAliases(canonicalSyncID)
	if err != nil {
		return res, fmt.Errorf("list aliases of %s: %w", canonicalSyncID, err)
	}
	sources := append(append([]string{}, aliases...), extraSources...)
	if len(sources) == 0 {
		return res, nil
	}
	have, err := bs.ListBookmarks(userID, canonicalSyncID)
	if err != nil {
		return res, fmt.Errorf("list canonical bookmarks: %w", err)
	}
	present := make(map[string]bool, len(have))
	for i := range have {
		present[progress.CanonicalTimeKey(have[i].TimeSec)] = true
	}
	var copier database.BookmarkCopier
	if !dryRun {
		if copier = database.AsBookmarkCopier(db); copier == nil {
			return res, errors.New("store has bookmarks but cannot copy them (no BookmarkCopier)")
		}
	}
	seenSrc := map[string]bool{canonicalSyncID: true}
	for _, src := range sources {
		if src == "" || seenSrc[src] {
			continue
		}
		seenSrc[src] = true
		rows, err := bs.ListBookmarks(userID, src)
		if err != nil {
			return res, fmt.Errorf("list bookmarks of alias %s: %w", src, err)
		}
		for _, b := range rows {
			key := progress.CanonicalTimeKey(b.TimeSec)
			if present[key] {
				continue
			}
			present[key] = true
			res.Owed++
			if dryRun {
				continue
			}
			b.UserID = userID
			b.ItemID = canonicalSyncID
			if _, err := copier.CopyBookmarkIfAbsent(b); err != nil {
				return res, fmt.Errorf("copy bookmark at %s from %s: %w", key, src, err)
			}
			res.Copied++
		}
	}
	return res, nil
}

// copyBookmarksForMerge runs CopyAliasBookmarks for every user after a
// loser -> winner follow. The loser's own syncID is passed as an extra source
// so its bookmarks are copied even if recording the redirect failed.
func copyBookmarksForMerge(db UserProgressMerger, users []database.User, loserBookID, winnerBookID string) error {
	if database.AsBookmarkStore(db) == nil {
		return nil
	}
	ids := database.AsSyncIdentityStore(db)
	if ids == nil {
		return nil
	}
	winnerSync, has, err := ids.GetSyncIDForBook(winnerBookID)
	if err != nil {
		return fmt.Errorf("read survivor syncID: %w", err)
	}
	if !has {
		// No client-visible identity on the survivor means no client ever
		// bookmarked it through the sync layer, and a loser with bookmarks
		// would have made followMerge mint one. Nothing to do.
		return nil
	}
	var extra []string
	if loserSync, ok, err := ids.GetSyncIDForBook(loserBookID); err != nil {
		return fmt.Errorf("read loser syncID: %w", err)
	} else if ok {
		extra = []string{loserSync}
	}
	var errs []error
	for _, u := range users {
		if u.ID == "" {
			continue
		}
		if _, err := CopyAliasBookmarks(db, u.ID, winnerSync, extra, false); err != nil {
			errs = append(errs, fmt.Errorf("user %s: %w", u.ID, err))
		}
	}
	return errors.Join(errs...)
}
