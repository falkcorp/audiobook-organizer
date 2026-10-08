// file: internal/itunes/service/position_sync.go
// version: 2.7.0
// guid: 9f7a8b5c-0d6e-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-10-05
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

		existing, err := p.store.GetUserPosition(adminUserID, book.ID)
		if err != nil {
			// Unreadable is not "no position": seeding here would replace
			// the listener's real position with the iTunes bookmark.
			p.log.Warn("itunes position sync: read position for %s: %v; not seeding the bookmark", book.ID, err)
			continue
		}
		if existing != nil {
			continue
		}

		// Find the first segment to use as the position target.
		files, err := p.store.GetBookFiles(book.ID)
		if err != nil {
			p.log.Warn("itunes position sync: read files for %s: %v; not seeding the bookmark", book.ID, err)
			continue
		}
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
	markErrs := 0 // books whose "already counted" mark failed; not seeded
	for _, book := range books {
		if book.ITunesPlayCount == nil || *book.ITunesPlayCount <= 0 {
			continue
		}
		// The "no state yet" check and the seed are one step under the
		// per-(user, book) user-state stripe (database.LockUserBookState), so
		// an ABS sync or the Repairs writer cannot write a state between
		// them that the seed would then overwrite. Order: stripe, then the
		// book's write stripe inside ModifyBook; nothing takes them the
		// other way round.
		ok := func() bool {
			defer database.LockUserBookState(adminUserID, book.ID)()
			state, err := p.store.GetUserBookState(adminUserID, book.ID)
			if err != nil {
				// Unreadable is not "no state": seeding here would write a
				// fresh Finished over whatever the row holds.
				stateErrs++
				p.log.Warn("itunes position sync: read state for %s: %v; not seeding finished", book.ID, err)
				return false
			}
			if state != nil {
				return false
			}
			// This finish came FROM iTunes' play count, so it has already been
			// counted there. The book is marked as counted first, and the
			// Finished state is written only once that mark is stored, carrying
			// the same stamp. A finish can then never exist unmarked: if the
			// mark fails, nothing is seeded and the next run tries again. If
			// the state write fails, the mark is left without a finish, which
			// is harmless because a later real finish is dated after it.
			finish := time.Now()
			if _, err := p.store.ModifyBook(book.ID, func(b *database.Book) error {
				if b.ITunesPlayCountBumpedAt != nil && !finish.After(*b.ITunesPlayCountBumpedAt) {
					return database.ErrSkipBookWrite
				}
				b.ITunesPlayCountBumpedAt = &finish
				return nil
			}); err != nil {
				markErrs++
				p.log.Warn("itunes position sync: mark the iTunes finish of %s as counted: %v; not seeding finished", book.ID, err)
				return false
			}
			// With no stored row, SetUserBookState keeps a stamp the caller
			// supplies, so the seeded finish is dated exactly at the mark. The
			// fields are the ones readstatus.SetManualStatus writes for a book
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
				return false
			}
			return true
		}()
		if !ok {
			continue
		}
		seeded++
	}
	if stateErrs > 0 {
		p.log.Warn("itunes position sync: %d read-state errors while seeding finished status", stateErrs)
	}
	if markErrs > 0 {
		p.log.Warn("itunes position sync: %d books not seeded as finished because marking their iTunes finish as counted failed", markErrs)
	}

	return seeded
}
