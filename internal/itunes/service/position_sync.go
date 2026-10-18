// file: internal/itunes/service/position_sync.go
// version: 2.9.0
// guid: 9f7a8b5c-0d6e-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-10-18
//
// One-way sync from the iTunes Bookmark / Play Count fields (spec 3.6
// task 4) into the app's per-user position/state tracking (spec 3.6).
//
// For each iTunes-sourced book with Bookmark > 0, seed an admin
// user_position row if one doesn't already exist. If iTunes has
// play_count > 0 but the admin has no book state, seed "finished".
//
// The push direction (app → iTunes, through the ITL write-back batcher)
// was removed with iTunes write-back on 2026-10-07: iTunes is an
// import-only source.
//
// The sync runs as a maintenance task (`itunes_position_sync`) in
// the scheduler. It can also be triggered manually from the API.

package itunesservice

import (
	"runtime"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/readstatus"
)

const adminUserID = "_local"

// positionSyncStore is what the iTunes position sync reads and writes.
//
// Measured with an empty-interface compiler probe: 3 direct calls (ModifyBook
// left on 2026-10-07 with the play-count mark it wrote), plus
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
	GetUserPosition(userID, bookID string) (*database.UserPosition, error)
	SetUserPosition(userID, bookID, segmentID string, positionSeconds float64) error
}

// PositionSync seeds per-user positions from iTunes ITL bookmark and
// play-count data. Owned by the Service.
type PositionSync struct {
	store positionSyncStore
	log   logger.Logger
}

// newPositionSync constructs a PositionSync wired with the given store.
func newPositionSync(store positionSyncStore) *PositionSync {
	return &PositionSync{store: store, log: logger.New("itunes-position-sync")}
}

// Sync seeds the admin user's positions from iTunes and returns how many
// were seeded.
func (p *PositionSync) Sync() (pulled int) {
	return p.pullBookmarks()
}

// pullBookmarks seeds admin positions from iTunes Bookmark data and
// "finished" from iTunes play counts, one book at a time on a bounded worker
// pool (NumCPU: the per-book work is a handful of Pebble reads and writes).
//
// Each book is handled by seedBook under that book's per-(user, book)
// user-state stripe, never the whole loop under one lock. Books are disjoint
// and the user is always adminUserID, so two workers never contend for one
// (user, book) key; they may share a stripe by hash, which only serializes
// those two books for a moment. No worker holds two stripes.
func (p *PositionSync) pullBookmarks() int {
	books, err := p.store.GetAllBooksCore(0, 0)
	if err != nil {
		p.log.Warn("itunes position sync: list books: %v", err)
		return 0
	}

	var seeded, stateErrs atomic.Int64
	var g errgroup.Group
	g.SetLimit(runtime.NumCPU())
	for i := range books {
		book := &books[i]
		hasBookmark := book.ITunesBookmark != nil && *book.ITunesBookmark > 0
		hasPlayCount := book.ITunesPlayCount != nil && *book.ITunesPlayCount > 0
		if !hasBookmark && !hasPlayCount {
			continue
		}
		g.Go(func() error {
			n, stateErr := p.seedBook(book, hasBookmark, hasPlayCount)
			seeded.Add(int64(n))
			if stateErr {
				stateErrs.Add(1)
			}
			return nil
		})
	}
	_ = g.Wait() // seedBook logs and never returns an error

	if n := stateErrs.Load(); n > 0 {
		p.log.Warn("itunes position sync: %d read-state errors while seeding finished status", n)
	}
	return int(seeded.Load())
}

// seedBook seeds one book's admin position from its iTunes bookmark and then
// its "finished" state from its iTunes play count, and returns how many seeds
// it wrote (0-2) and whether the finished seed's state read failed.
//
// Both seeds are check-then-write, and the bookmark seed's recompute is a
// read-derive-write of the state, so the whole of it is one hold of the
// (adminUserID, book) user-state stripe (database.LockUserBookState): an ABS
// sync, the Repairs writer or a merge undo's put-back cannot land between a
// check and its seed, or between the recompute's read and its write, and be
// overwritten. The recompute is RecomputeUserBookStateLocked because this
// already holds the stripe. The order is the order the two seeds always ran
// in: a bookmark seed that recomputed a state makes the finished seed find
// that state and skip. Lock order: this stripe only (it writes no book row).
func (p *PositionSync) seedBook(book *database.BookCore, hasBookmark, hasPlayCount bool) (seeded int, stateErr bool) {
	defer database.LockUserBookState(adminUserID, book.ID)()
	if hasBookmark && p.seedBookmarkLocked(book) {
		seeded++
	}
	if hasPlayCount {
		ok, readErr := p.seedFinishedLocked(book)
		if ok {
			seeded++
		}
		stateErr = readErr
	}
	return seeded, stateErr
}

// seedBookmarkLocked creates the admin position from the iTunes bookmark when
// none exists yet, then recomputes the derived state. The caller holds the
// book's user-state stripe.
func (p *PositionSync) seedBookmarkLocked(book *database.BookCore) bool {
	existing, err := p.store.GetUserPosition(adminUserID, book.ID)
	if err != nil {
		// Unreadable is not "no position": seeding here would replace
		// the listener's real position with the iTunes bookmark.
		p.log.Warn("itunes position sync: read position for %s: %v; not seeding the bookmark", book.ID, err)
		return false
	}
	if existing != nil {
		return false
	}

	// Find the first segment to use as the position target.
	files, err := p.store.GetBookFiles(book.ID)
	if err != nil {
		p.log.Warn("itunes position sync: read files for %s: %v; not seeding the bookmark", book.ID, err)
		return false
	}
	segmentID := ""
	if len(files) > 0 {
		segmentID = files[0].ID
	}
	if segmentID == "" {
		return false
	}

	bookmarkSeconds := float64(*book.ITunesBookmark) / 1000.0
	if err := p.store.SetUserPosition(adminUserID, book.ID, segmentID, bookmarkSeconds); err != nil {
		p.log.Warn("itunes position sync: seed position for %s: %v", book.ID, err)
		return false
	}

	// Recompute the derived book state from the seeded position.
	if _, err := readstatus.RecomputeUserBookStateLocked(p.store, adminUserID, book.ID); err != nil {
		p.log.Warn("itunes position sync: recompute state for %s after bookmark seed: %v", book.ID, err)
	}
	return true
}

// seedFinishedLocked seeds "finished" from an iTunes play count > 0 when the
// admin has no state on the book yet. The caller holds the book's user-state
// stripe, so the "no state yet" check and the seed are one step. readErr
// reports a failed state read (counted by the caller).
func (p *PositionSync) seedFinishedLocked(book *database.BookCore) (ok, readErr bool) {
	state, err := p.store.GetUserBookState(adminUserID, book.ID)
	if err != nil {
		// Unreadable is not "no state": seeding here would write a
		// fresh Finished over whatever the row holds.
		p.log.Warn("itunes position sync: read state for %s: %v; not seeding finished", book.ID, err)
		return false, true
	}
	if state != nil {
		return false, false
	}
	// Nothing is pushed back to iTunes (import-only since
	// 2026-10-07), so this seed no longer stamps the book's
	// ITunesPlayCountBumpedAt: that mark only stopped the removed
	// push from counting an iTunes-sourced finish twice.
	finish := time.Now()
	// With no stored row, SetUserBookState keeps the stamp the caller
	// supplies. The fields are the ones readstatus.SetManualStatus writes for a book
	// with no state.
	if err := p.store.SetUserBookState(&database.UserBookState{
		UserID:         adminUserID,
		BookID:         book.ID,
		Status:         database.UserBookStatusFinished,
		StatusManual:   true,
		LastActivityAt: finish,
		FinishedAt:     &finish,
	}); err != nil {
		p.log.Warn("itunes position sync: seed finished for %s: %v", book.ID, err)
		return false, false
	}
	return true, false
}
