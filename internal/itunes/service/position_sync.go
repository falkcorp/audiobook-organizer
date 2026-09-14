// file: internal/itunes/service/position_sync.go
// version: 2.4.0
// guid: 9f7a8b5c-0d6e-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-09-13
//
// Bidirectional sync between the app's per-user position/state
// tracking (spec 3.6) and the iTunes Bookmark / Play Count fields
// (spec 3.6 task 4).
//
// Pull direction (iTunes → app):
//   For each iTunes-sourced book with Bookmark > 0, seed an admin
//   user_position row if one doesn't already exist. If iTunes has
//   play_count > 0 but the admin has no book state, seed "finished".
//
// Push direction (app → iTunes):
//   For the admin user's positions that changed since the last sync,
//   write Bookmark and (if finished) increment Play Count + set
//   Played Date via the ITL write-back batcher.
//
// The sync runs as a maintenance task (`itunes_position_sync`) in
// the scheduler. It can also be triggered manually from the API.

package itunesservice

import (
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/readstatus"
)

const adminUserID = "_local"

// positionSyncStore is what the iTunes position sync reads and writes.
//
// Measured with an empty-interface compiler probe: 8 direct calls (7 since
// 2026-09-13, when GetBookByID + UpdateBook became one ModifyBook), plus
// readstatus.Store because this package forwards its store to
// readstatus.RecomputeUserBookState and SetManualStatus. Embedding that
// interface states the forwarding relationship instead of duplicating its
// four methods here.
//
// Previously database.BookStore + BookFileStore + UserPositionStore, 86
// methods transitively. Narrowing it needed readstatus to name its own
// parameter interface first: until then this had to satisfy two whole
// database surfaces to call a four-method function.
type positionSyncStore interface {
	readstatus.Store

	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
	GetUserPosition(userID, bookID string) (*database.UserPosition, error)
	SetUserPosition(userID, bookID, segmentID string, positionSeconds float64) error
	ListUserPositionsSince(userID string, t time.Time) ([]database.UserPosition, error)
}

// PositionSync runs the bidirectional bookmark/play-count sync between
// iTunes ITL data and per-user positions. Owned by the Service; scheduler
// triggers Sync() on its cadence.
type PositionSync struct {
	store    positionSyncStore
	enqueuer Enqueuer
	log      logger.Logger
}

// newPositionSync constructs a PositionSync wired with the given store
// and enqueuer. A nil enqueuer disables the push direction (pull still
// runs) — useful for tests that only verify seeding behavior.
func newPositionSync(store positionSyncStore, enqueuer Enqueuer) *PositionSync {
	return &PositionSync{store: store, enqueuer: enqueuer, log: logger.New("itunes-position-sync")}
}

// Sync runs a full bidirectional position sync for the
// admin user. Pull then push order ensures we don't immediately
// overwrite a newly-seeded position.
func (p *PositionSync) Sync() (pulled, pushed int) {
	pulled = p.pullBookmarks()
	pushed = p.pushPositions()
	return pulled, pushed
}

// pullITunesBookmarks seeds admin positions from iTunes Bookmark data.
// Iterates books with an iTunes Bookmark value and creates a position
// row if none exists yet.
func (p *PositionSync) pullBookmarks() int {
	books, err := p.store.GetAllBooksCore(0, 0)
	if err != nil {
		p.log.Warn("itunes position sync: list books: %v", err)
		return 0
	}

	seeded := 0
	for _, book := range books {
		if book.ITunesBookmark == nil || *book.ITunesBookmark <= 0 {
			continue
		}

		existing, _ := p.store.GetUserPosition(adminUserID, book.ID)
		if existing != nil {
			continue
		}

		// Find the first segment to use as the position target.
		files, _ := p.store.GetBookFiles(book.ID)
		segmentID := ""
		if len(files) > 0 {
			segmentID = files[0].ID
		}
		if segmentID == "" {
			continue
		}

		bookmarkSeconds := float64(*book.ITunesBookmark) / 1000.0
		if err := p.store.SetUserPosition(adminUserID, book.ID, segmentID, bookmarkSeconds); err != nil {
			p.log.Warn("itunes position sync: seed position for %s: %v", book.ID, err)
			continue
		}

		// Recompute the derived book state from the seeded position.
		if _, err := readstatus.RecomputeUserBookState(p.store, adminUserID, book.ID); err != nil {
			p.log.Warn("itunes position sync: recompute state for %s after bookmark seed: %v", book.ID, err)
		}
		seeded++
	}

	// Also seed "finished" from iTunes play_count > 0 with no existing state.
	stateErrs := 0
	for _, book := range books {
		if book.ITunesPlayCount == nil || *book.ITunesPlayCount <= 0 {
			continue
		}
		state, err := p.store.GetUserBookState(adminUserID, book.ID)
		if err != nil {
			// Unreadable is not "no state": seeding here would write a
			// fresh Finished over whatever the row holds.
			stateErrs++
			p.log.Warn("itunes position sync: read state for %s: %v; not seeding finished", book.ID, err)
			continue
		}
		if state != nil {
			continue
		}
		seededState, err := readstatus.SetManualStatus(p.store, adminUserID, book.ID, database.UserBookStatusFinished)
		if err != nil {
			p.log.Warn("itunes position sync: seed finished for %s: %v", book.ID, err)
			continue
		}
		seeded++
		// This finish came FROM iTunes' play count, so it is already
		// counted there: record it as bumped, or the next push would add a
		// play for it.
		if seededState != nil && seededState.FinishedAt != nil {
			finish := *seededState.FinishedAt
			if _, err := p.store.ModifyBook(book.ID, func(b *database.Book) error {
				if b.ITunesPlayCountBumpedAt != nil && !finish.After(*b.ITunesPlayCountBumpedAt) {
					return database.ErrSkipBookWrite
				}
				b.ITunesPlayCountBumpedAt = &finish
				return nil
			}); err != nil {
				p.log.Warn("itunes position sync: mark seeded finish of %s as counted: %v", book.ID, err)
			}
		}
	}
	if stateErrs > 0 {
		p.log.Warn("itunes position sync: %d read-state errors while seeding finished status", stateErrs)
	}

	return seeded
}

// pushPositionsToITunes writes admin position changes back to iTunes
// via the write-back batcher. For each book where the admin's
// position was updated since the last sync, enqueue the book for
// bookmark writeback. If the book was marked finished, also enqueue
// a play-count increment.
func (p *PositionSync) pushPositions() int {
	// Get all admin positions that changed in the last 24 hours.
	// A more precise cutoff would use a last-sync-at timestamp;
	// for now 24h is a safe window for the maintenance task that
	// runs every few hours.
	cutoff := time.Now().Add(-24 * time.Hour)
	positions, err := p.store.ListUserPositionsSince(adminUserID, cutoff)
	if err != nil {
		p.log.Warn("itunes position push: list positions: %v", err)
		return 0
	}

	if p.enqueuer == nil {
		return 0
	}

	pushed := 0
	// H4 (2026-07 error-correction sweep): GetBookByID error vs. a benign
	// nil-book/no-PID skip used to be one bare `continue`. Distinguish them
	// so a store-level lookup problem is visible instead of just quietly
	// reducing how many positions get pushed.
	lookupErrs := 0
	stateErrs := 0 // GetUserBookState failures; the bookmark still went out
	seen := map[string]bool{}
	for _, pos := range positions {
		if seen[pos.BookID] {
			continue
		}
		seen[pos.BookID] = true

		// Read the finish state first: ModifyBook's callback holds the
		// book's write stripe and must stay a pure in-memory mutation.
		state, stateErr := p.store.GetUserBookState(adminUserID, pos.BookID)
		if stateErr != nil {
			// Without the state there is no knowing whether this is a new
			// finish: push the bookmark, bump nothing.
			stateErrs++
			p.log.Warn("itunes position push: read state for %s: %v; pushing the bookmark without a play-count bump", pos.BookID, stateErr)
			state = nil
		}
		bookmarkMs := int64(pos.PositionSeconds * 1000)

		// One write for the bookmark and the play-count bump, on the row
		// as it is now. Until 2026-09-13 this was two full-row UpdateBooks
		// of a copy read before either, and the bump ran on EVERY sync for
		// any finished book whose position moved in the last 24h -- so a
		// single finish added a play each time the task ran.
		noPID := false
		row, err := p.store.ModifyBook(pos.BookID, func(book *database.Book) error {
			if book.ITunesPersistentID == nil {
				noPID = true
				return database.ErrSkipBookWrite
			}
			book.ITunesBookmark = &bookmarkMs
			if finish, ok := unbumpedFinish(state, book); ok {
				pc := 0
				if book.ITunesPlayCount != nil {
					pc = *book.ITunesPlayCount
				}
				pc++
				now := time.Now()
				book.ITunesPlayCount = &pc
				book.ITunesLastPlayed = &now
				book.ITunesPlayCountBumpedAt = &finish
			}
			return nil
		})
		if err != nil {
			lookupErrs++
			p.log.Warn("itunes position push: update bookmark for %s: %v", pos.BookID, err)
			continue
		}
		if row == nil || noPID {
			continue
		}
		p.enqueuer.Enqueue(row.ID)
		pushed++
	}

	if lookupErrs > 0 {
		p.log.Warn("itunes position push: %d book lookup/write errors across %d positions", lookupErrs, len(positions))
	}
	if stateErrs > 0 {
		p.log.Warn("itunes position push: %d read-state errors across %d positions; those bookmarks were pushed without a play-count check", stateErrs, len(positions))
	}

	return pushed
}

// unbumpedFinish reports whether state is a finish not yet counted into the
// book's iTunes play count, and returns the finish's time to record. The
// finish time is the state's LastActivityAt: it does not move when the sync
// merely re-runs, so the same finish is counted once, and a later re-listen
// that finishes again (moving LastActivityAt past the recorded time) is
// counted again.
func unbumpedFinish(state *database.UserBookState, book *database.Book) (time.Time, bool) {
	if state == nil || state.Status != database.UserBookStatusFinished || state.FinishedAt == nil {
		// No stamp: a Finished row written before FinishedAt existed. Its
		// finish was already counted (every sync counted it again, until
		// 2026-09-13), so it is not counted once more.
		return time.Time{}, false
	}
	finish := *state.FinishedAt
	if book.ITunesPlayCountBumpedAt != nil && !finish.After(*book.ITunesPlayCountBumpedAt) {
		return time.Time{}, false
	}
	return finish, true
}
