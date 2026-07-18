// file: internal/itunes/service/position_sync.go
// version: 2.1.1
// guid: 9f7a8b5c-0d6e-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-07-18
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
	"github.com/falkcorp/audiobook-organizer/internal/readstatus"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

const adminUserID = "_local"

// positionSyncStore is the narrow slice of the service's Store that
// PositionSync needs. Carries the full (Book + File + UserPosition)
// surface since the pull/push code paths both read books and positions
// and write back to user_book_state.
type positionSyncStore interface {
	database.BookStore
	database.BookFileStore
	database.UserPositionStore
}

// PositionSync runs the bidirectional bookmark/play-count sync between
// iTunes ITL data and per-user positions. Owned by the Service; scheduler
// triggers Sync() on its cadence.
type PositionSync struct {
	store    positionSyncStore
	enqueuer Enqueuer
}

// newPositionSync constructs a PositionSync wired with the given store
// and enqueuer. A nil enqueuer disables the push direction (pull still
// runs) — useful for tests that only verify seeding behavior.
func newPositionSync(store positionSyncStore, enqueuer Enqueuer) *PositionSync {
	return &PositionSync{store: store, enqueuer: enqueuer}
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
		slog.Warn("itunes position sync list books", "err", err)
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
			slog.Warn("seed position for", "book", book.ID, "err", err)
			continue
		}

		// Recompute the derived book state from the seeded position.
		if _, err := readstatus.RecomputeUserBookState(p.store, adminUserID, book.ID); err != nil {
			slog.Warn("recompute state for after bookmark seed", "book", book.ID, "err", err)
		}
		seeded++
	}

	// Also seed "finished" from iTunes play_count > 0 with no existing state.
	for _, book := range books {
		if book.ITunesPlayCount == nil || *book.ITunesPlayCount <= 0 {
			continue
		}
		state, _ := p.store.GetUserBookState(adminUserID, book.ID)
		if state != nil {
			continue
		}
		if _, err := readstatus.SetManualStatus(p.store, adminUserID, book.ID, database.UserBookStatusFinished); err != nil {
			slog.Warn("seed finished for", "book", book.ID, "err", err)
			continue
		}
		seeded++
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
		slog.Warn("itunes position push list positions", "err", err)
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
	seen := map[string]bool{}
	for _, pos := range positions {
		if seen[pos.BookID] {
			continue
		}
		seen[pos.BookID] = true

		book, err := p.store.GetBookByID(pos.BookID)
		if err != nil {
			lookupErrs++
			continue
		}
		if book == nil || book.ITunesPersistentID == nil {
			continue
		}

		// Update bookmark via the batcher (it updates the ITL on flush).
		bookmarkMs := int64(pos.PositionSeconds * 1000)
		book.ITunesBookmark = &bookmarkMs
		if _, err := p.store.UpdateBook(book.ID, book); err != nil {
			slog.Warn("update bookmark for", "book", book.ID, "err", err)
			continue
		}
		p.enqueuer.Enqueue(book.ID)
		pushed++

		// If the book is marked finished and iTunes play count hasn't
		// been bumped, increment it.
		state, _ := p.store.GetUserBookState(adminUserID, pos.BookID)
		if state != nil && state.Status == database.UserBookStatusFinished {
			pc := 0
			if book.ITunesPlayCount != nil {
				pc = *book.ITunesPlayCount
			}
			newPC := pc + 1
			now := time.Now()
			book.ITunesPlayCount = &newPC
			book.ITunesLastPlayed = &now
			if _, err := p.store.UpdateBook(book.ID, book); err != nil {
				slog.Warn("bump play count for", "book", book.ID, "err", err)
			}
		}
	}

	if lookupErrs > 0 {
		slog.Warn("itunes position push: book lookup errors", "errors", lookupErrs, "total_positions", len(positions))
	}

	return pushed
}
