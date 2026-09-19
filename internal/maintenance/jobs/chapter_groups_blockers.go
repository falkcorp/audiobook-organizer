// file: internal/maintenance/jobs/chapter_groups_blockers.go
// version: 1.1.0
// guid: ef5d7b19-e54c-4787-ba5b-cbc96add5858
// last-edited: 2026-09-19

package jobs

import (
	"fmt"
	"slices"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// chapterBlockerStore is what the carry check reads beyond the job store. Every
// database.Store satisfies it; the job asserts it once per run.
type chapterBlockerStore interface {
	merge.UserProgressMerger
	ListUserPlaylists(playlistType string, limit, offset int) ([]database.UserPlaylist, int, error)
	GetBookAuthors(bookID string) ([]database.BookAuthor, error)
}

// chapterCarryContext is read once per run.
type chapterCarryContext struct {
	store     chapterBlockerStore
	users     []database.User
	playlists []database.UserPlaylist
	canFollow bool
}

func newChapterCarryContext(store chapterBlockerStore) (*chapterCarryContext, error) {
	users, err := store.ListUsers()
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	cc := &chapterCarryContext{store: store, users: users, canFollow: database.AsSyncIdentityStore(store) != nil}
	const page = 500
	for off := 0; ; off += page {
		pls, _, err := store.ListUserPlaylists("", page, off)
		if err != nil {
			return nil, fmt.Errorf("list playlists: %w", err)
		}
		cc.playlists = append(cc.playlists, pls...)
		if len(pls) < page {
			break
		}
	}
	return cc, nil
}

// chapterGroupBlockers lists what a merge of this group would lose. The merge
// carries files, external IDs, file sync identity, every user's listening
// progress and the book's sync identity (merge.FollowAbsorbedJournaled), all
// journaled for undo. It does NOT carry:
//
//   - bookmarks (keyed by the source's sync item id; they would strand),
//   - static playlist entries naming a source (the entry would point at a
//     soft-deleted book),
//   - user ratings/notes on a source,
//   - listening progress on a store that cannot follow it,
//   - metadata the primary lacks on which two sources DISAGREE (conflict).
//
// Metadata the primary lacks and the sources agree on (ASIN, narrator,
// series, author) is not a blocker: the merge fills it onto the primary's
// EMPTY fields, never over a set or user-locked one, journaled so undo
// empties it again (dedup.PlanFillEmpty / SplitMergeOptions.FillEmpty).
//
// Any blocker holds the group back: it is reported and left alone rather than
// losing the data at the purge. Returns the reasons (empty means safe) and the
// metadata fields the merge would fill.
func (cc *chapterCarryContext) chapterGroupBlockers(primary *database.Book, sources []*database.Book) ([]string, []string) {
	var out []string
	add := func(format string, a ...any) { out = append(out, fmt.Sprintf(format, a...)) }
	// database.AsBookmarkStore unwraps the server's store decorators (a bare
	// type assertion misses on the prod indexedStore). A store without it
	// fails the check closed: bookmarks it cannot see it cannot protect.
	bm := database.AsBookmarkStore(cc.store)
	canListBookmarks := bm != nil
	sync := database.AsSyncIdentityStore(cc.store)
	for _, src := range sources {
		if !cc.canFollow {
			if has, err := merge.BookHasUserProgress(cc.store, src.ID); err != nil || has {
				add("%s: listening progress cannot be carried by this store (%v)", src.ID, err)
			}
		}
		if !canListBookmarks {
			add("%s: bookmarks cannot be checked on this store", src.ID)
		} else {
			itemIDs := []string{src.ID}
			if sync != nil {
				if sid, ok, err := sync.GetSyncIDForBook(src.ID); err != nil {
					add("%s: sync id not readable: %v", src.ID, err)
				} else if ok && sid != "" {
					itemIDs = append(itemIDs, sid)
				}
			}
			for _, u := range cc.users {
				for _, item := range itemIDs {
					marks, err := bm.ListBookmarks(u.ID, item)
					if err != nil {
						add("%s: bookmarks not readable: %v", src.ID, err)
					} else if len(marks) > 0 {
						add("%s: %d bookmark(s) of user %s would not follow the merge", src.ID, len(marks), u.ID)
					}
				}
			}
		}
		for _, pl := range cc.playlists {
			if slices.Contains(pl.BookIDs, src.ID) {
				add("%s: listed in playlist %q", src.ID, pl.Name)
			}
		}
		if src.UserRatingOverall != nil || src.UserRatingStory != nil || src.UserRatingPerformance != nil || src.UserRatingNotes != nil {
			add("%s: has a user rating or notes", src.ID)
		}
	}
	plan, err := dedup.PlanFillEmpty(cc.store, primary, sources)
	if err != nil {
		add("metadata fill plan not readable: %v", err)
	}
	for _, c := range plan.Conflicts {
		add("metadata conflict: %s", c)
	}
	return out, plan.Fields
}
