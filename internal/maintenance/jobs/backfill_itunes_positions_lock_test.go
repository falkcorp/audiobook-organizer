// file: internal/maintenance/jobs/backfill_itunes_positions_lock_test.go
// version: 1.0.0
// guid: 37e519cf-4e29-4e3e-ba70-ebf4b512885c
// last-edited: 2026-10-18

package jobs_test

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance/jobs"
)

// stateReadHookStore runs onRead once, after the backfill's read of the
// user's state on the book: between its read and its writes.
type stateReadHookStore struct {
	*database.PebbleStore
	once   sync.Once
	onRead func()
}

func (s *stateReadHookStore) GetUserBookState(userID, bookID string) (*database.UserBookState, error) {
	st, err := s.PebbleStore.GetUserBookState(userID, bookID)
	if s.onRead != nil {
		s.once.Do(s.onRead)
	}
	return st, err
}

// The per-book migration waits for the (user, book) user-state stripe.
func TestBackfillITunesPositions_HoldsUserStateLock(t *testing.T) {
	store := newSyncPebbleStore(t)
	user := newPositionUser(t, store, "owner")
	t.Setenv(jobs.ITunesPositionBackfillUserIDEnv, user.ID)
	book := seedBookmarkedBook(t, store, "locked", new(int64(600000)), []int{1000, 1000}, nil)

	unlock := database.LockUserBookState(user.ID, book.ID)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runPositionBackfill(t, store, false)
	}()
	select {
	case <-done:
		unlock()
		t.Fatal("the backfill migrated the book while the user-state lock was held")
	case <-time.After(200 * time.Millisecond):
	}
	unlock()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the backfill never finished after the lock was released")
	}
	require.NotNil(t, wholeBookPosition(t, store, user.ID, book.ID))
}

// A device write landing between the backfill's read of the state and its
// state write is kept: the backfill holds the stripe, so the client waits and
// writes on top. Before 2026-10-18 the backfill took no stripe and its state
// write, built from the stale read, erased the client's hide flag.
func TestBackfillITunesPositions_UserStateRaceKeepsLockedWrite(t *testing.T) {
	base := newSyncPebbleStore(t)
	user := newPositionUser(t, base, "owner")
	t.Setenv(jobs.ITunesPositionBackfillUserIDEnv, user.ID)
	book := seedBookmarkedBook(t, base, "raced", new(int64(600000)), []int{1000, 1000}, nil)

	var wait func() error
	store := &stateReadHookStore{PebbleStore: base}
	store.onRead = func() {
		done := make(chan error, 1)
		go func() {
			defer database.LockUserBookState(user.ID, book.ID)()
			cur, err := base.GetUserBookState(user.ID, book.ID)
			if err != nil {
				done <- err
				return
			}
			next := database.UserBookState{UserID: user.ID, BookID: book.ID}
			if cur != nil {
				next = *cur
			}
			next.HideFromContinueListening = true
			done <- base.SetUserBookState(&next)
		}()
		var landedErr error
		landed := false
		select {
		case landedErr = <-done:
			landed = true
		case <-time.After(200 * time.Millisecond):
		}
		wait = func() error {
			if landed {
				return landedErr
			}
			return <-done
		}
	}
	runPositionBackfill(t, store, false)
	require.NotNil(t, wait, "the backfill never read the state")
	require.NoError(t, wait())

	st, err := base.GetUserBookState(user.ID, book.ID)
	require.NoError(t, err)
	require.NotNil(t, st)
	require.True(t, st.HideFromContinueListening,
		"the client's hide flag, written between the backfill's read and write, was lost")
	require.NotNil(t, wholeBookPosition(t, base, user.ID, book.ID), "the backfill's own position is missing")
}
