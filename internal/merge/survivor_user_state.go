// file: internal/merge/survivor_user_state.go
// version: 1.0.0
// guid: 2a6d8f31-7c4e-4b19-9e02-5f3b1c8a7d64
// last-edited: 2026-09-26

package merge

import (
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Survivor preference for user state (owner-approved 2026-09-26).
//
// Every merge that retires a book a listener has state on creates an alias:
// the client keeps the old libraryItemId, and its progress and bookmarks have
// to be moved (sync_follow.go) and read through redirects. The cheapest alias
// is the one never created, so an AUTOMATIC survivor election prefers the one
// candidate some user already has client-visible state on -- progress, a
// finished flag, a bookmark, or a recorded sync alias pointing at it.
//
// It never overrides a hard rule. An explicit keep/primary id from the caller
// always wins (this is only consulted when the election is automatic), and the
// stateful candidate is preferred only when it is at least as eligible as the
// book the existing rule picked: live, an audio route if the pick has one, not
// an iTunes-ghost path if the pick is not one, and library_state=organized if
// the pick is organized. When MORE than one candidate carries state the
// existing rule stands and the state move reconciles the two.

// stateStateReader is what probing a book for user state needs.
type userStateReader interface {
	ListUsers() ([]database.User, error)
	GetUserBookState(userID, bookID string) (*database.UserBookState, error)
	ListUserPositionsForBook(userID, bookID string) ([]database.UserPosition, error)
}

// BooksWithClientVisibleState returns which of bookIDs some user has
// client-visible state on. An error means the answer is unknown; callers
// electing a survivor then fall back to the existing rule.
func BooksWithClientVisibleState(db userStateReader, bookIDs []string) (map[string]bool, error) {
	users, err := db.ListUsers()
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	ids := database.AsSyncIdentityStore(db)
	bs := database.AsBookmarkStore(db)
	out := make(map[string]bool, len(bookIDs))
	for _, bookID := range bookIDs {
		has, err := bookHasClientVisibleState(db, ids, bs, users, bookID)
		if err != nil {
			return nil, fmt.Errorf("book %s: %w", bookID, err)
		}
		if has {
			out[bookID] = true
		}
	}
	return out, nil
}

func bookHasClientVisibleState(db userStateReader, ids database.SyncIdentityStore, bs database.BookmarkStore, users []database.User, bookID string) (bool, error) {
	for _, u := range users {
		if u.ID == "" {
			continue
		}
		st, err := db.GetUserBookState(u.ID, bookID)
		if err != nil {
			return false, err
		}
		pos, err := db.ListUserPositionsForBook(u.ID, bookID)
		if err != nil {
			return false, err
		}
		if hasCarryableState(st, pos) {
			return true, nil
		}
	}
	if ids == nil {
		return false, nil
	}
	syncID, has, err := ids.GetSyncIDForBook(bookID)
	if err != nil || !has {
		return false, err
	}
	aliases, err := ids.ListSyncAliases(syncID)
	if err != nil {
		return false, err
	}
	if len(aliases) > 0 {
		return true, nil // clients already hold ids that resolve here
	}
	if bs == nil {
		return false, nil
	}
	for _, u := range users {
		if u.ID == "" {
			continue
		}
		marks, err := bs.ListBookmarks(u.ID, syncID)
		if err != nil {
			return false, err
		}
		if len(marks) > 0 {
			return true, nil
		}
	}
	return false, nil
}

func isOrganized(b *database.Book) bool {
	return b.LibraryState != nil && *b.LibraryState == "organized"
}

// PreferUserStateSurvivor adjusts an automatic election (electedIdx, from
// ElectPrimary) toward the single candidate with client-visible user state.
// It returns the index to keep; it equals electedIdx whenever the preference
// does not apply (none or several stateful candidates, or the stateful one
// fails a hard rule the elected one passes).
func PreferUserStateSurvivor(books []*database.Book, filesByID map[string][]database.BookFile, stateful map[string]bool, electedIdx int) int {
	if electedIdx < 0 || len(stateful) == 0 {
		return electedIdx
	}
	cand := -1
	for i, b := range books {
		if b.IsSoftDeleted() || !stateful[b.ID] {
			continue
		}
		if cand >= 0 {
			return electedIdx // more than one: keep the current rule
		}
		cand = i
	}
	if cand < 0 || cand == electedIdx {
		return electedIdx
	}
	s, e := books[cand], books[electedIdx]
	if HasAudioRoute(e, filesByID[e.ID]) && !HasAudioRoute(s, filesByID[s.ID]) {
		return electedIdx
	}
	if IsITunesGhostPath(s.FilePath) && !IsITunesGhostPath(e.FilePath) {
		return electedIdx
	}
	if isOrganized(e) && !isOrganized(s) {
		return electedIdx
	}
	return cand
}
