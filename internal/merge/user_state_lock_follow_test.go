// file: internal/merge/user_state_lock_follow_test.go
// version: 1.1.1
// guid: bd9bd3d5-42dd-4e0b-92dd-b5ab18ca5f90
// last-edited: 2026-10-23

package merge

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// stateReadHook runs onRead once, after the first read of book's rows has
// returned: GetUserBookState, or with onPositions ListUserPositionsForBook.
// That is between the writer's read and its write.
type stateReadHook struct {
	*database.PebbleStore
	book        string
	onPositions bool
	once        sync.Once
	onRead      func()
}

func (s *stateReadHook) GetUserBookState(userID, bookID string) (*database.UserBookState, error) {
	st, err := s.PebbleStore.GetUserBookState(userID, bookID)
	if !s.onPositions && bookID == s.book && s.onRead != nil {
		s.once.Do(s.onRead)
	}
	return st, err
}

func (s *stateReadHook) ListUserPositionsForBook(userID, bookID string) ([]database.UserPosition, error) {
	rows, err := s.PebbleStore.ListUserPositionsForBook(userID, bookID)
	if s.onPositions && bookID == s.book && s.onRead != nil {
		s.once.Do(s.onRead)
	}
	return rows, err
}

// hasPosition reports whether user has seg=sec on any of books.
func hasPosition(t *testing.T, s *database.PebbleStore, user, seg string, sec float64, books ...string) bool {
	t.Helper()
	for _, b := range books {
		rows, err := s.ListUserPositionsForBook(user, b)
		require.NoError(t, err)
		for _, p := range rows {
			if p.SegmentID == seg && p.PositionSeconds == sec {
				return true
			}
		}
	}
	return false
}

func seedFollowPair(t *testing.T) (*database.PebbleStore, string, string, string) {
	t.Helper()
	s := setupTestStore(t).(*database.PebbleStore)
	winner, loser := seedSyncBooks(t, s)
	u := seedSyncUser(t, s)
	require.NoError(t, s.SetUserPositionAt(u.ID, loser, "a", 1500, rfT1))
	require.NoError(t, s.SetUserBookState(&database.UserBookState{
		UserID: u.ID, BookID: loser, Status: database.UserBookStatusInProgress, ProgressPct: 50, LastActivityAt: rfT1, LastSegmentID: "a", TotalListenedSeconds: 1500,
	}))
	require.NoError(t, s.SetUserPositionAt(u.ID, winner, "w", 100, rfT0))
	require.NoError(t, s.SetUserBookState(&database.UserBookState{
		UserID: u.ID, BookID: winner, Status: database.UserBookStatusInProgress, ProgressPct: 10, LastActivityAt: rfT0, LastSegmentID: "w", TotalListenedSeconds: 100,
	}))
	return s, winner, loser, u.ID
}

// A device sync to the merge LOSER landing after the follow read the loser's
// positions is not drained away with the loser: the follow holds both books'
// stripes (LockUserBookStatePair), so the client waits and its position
// lands after the follow -- on the loser, where the pending-repair sweep
// carries it. Before 2026-10-18 the follow took no stripe, the client's
// position landed at once, and the drain's ClearUserPositions removed it:
// listening progress gone from every book.
func TestFollowMerge_UserStateRaceLoserWriteNotDrained(t *testing.T) {
	s, winner, loser, user := seedFollowPair(t)
	var wait func() error
	hooked := &stateReadHook{PebbleStore: s, book: loser, onPositions: true}
	hooked.onRead = func() {
		wait = stripedClientWrite(user, loser, func() error {
			return s.SetUserPositionAt(user, loser, "late", 777, time.Now().UTC())
		})
	}
	require.NoError(t, FollowMerge(hooked, nil, winner, []string{loser}))
	require.NotNil(t, wait, "the follow never read the loser's positions")
	require.NoError(t, wait())
	require.True(t, hasPosition(t, s, user, "late", 777, loser, winner),
		"the client's position written during the follow is on neither book: drained with the loser")
	require.True(t, hasPosition(t, s, user, "a", 1500, winner), "the follow did not carry the loser's own position")
}

// A device write to the merge WINNER landing after the follow read the
// winner is not overwritten by the merged state built from that stale read:
// the client waits for the follow's stripes and writes on top.
func TestFollowMerge_UserStateRaceWinnerWriteKept(t *testing.T) {
	s, winner, loser, user := seedFollowPair(t)
	var wait func() error
	hooked := &stateReadHook{PebbleStore: s, book: winner}
	hooked.onRead = func() {
		wait = stripedClientWrite(user, winner, func() error {
			cur, err := s.GetUserBookState(user, winner)
			if err != nil || cur == nil {
				return err
			}
			next := *cur
			next.HideFromContinueListening = true
			return s.SetUserBookState(&next)
		})
	}
	require.NoError(t, FollowMerge(hooked, nil, winner, []string{loser}))
	require.NotNil(t, wait, "the follow never read the winner's state")
	require.NoError(t, wait())
	st, err := s.GetUserBookState(user, winner)
	require.NoError(t, err)
	require.NotNil(t, st)
	require.True(t, st.HideFromContinueListening,
		"the client's hide flag on the winner, written during the follow, was overwritten by the merged state")
	require.Equal(t, 50, st.ProgressPct, "the follow's own merged state is missing")
}

// The slice follow (chapter / split-part merges) holds both stripes the same
// way: a device sync to the absorbed slice landing during the follow is not
// drained away.
func TestFollowSlice_UserStateRaceAbsorbedWriteNotDrained(t *testing.T) {
	s, survivor, absorbed, user := seedFollowPair(t)
	var wait func() error
	hooked := &stateReadHook{PebbleStore: s, book: absorbed, onPositions: true}
	hooked.onRead = func() {
		wait = stripedClientWrite(user, absorbed, func() error {
			return s.SetUserPositionAt(user, absorbed, "late", 777, time.Now().UTC())
		})
	}
	require.NoError(t, followSliceFor(hooked, user, survivor, absorbed, SliceMapping{OffsetSeconds: 300, Mappable: true}))
	require.NotNil(t, wait, "the slice follow never read the absorbed book's positions")
	require.NoError(t, wait())
	require.True(t, hasPosition(t, s, user, "late", 777, absorbed, survivor),
		"the client's position written during the slice follow is on neither book")
}

// The follow waits for either book's stripe: a writer holding the loser's or
// the winner's keeps it out.
func TestFollowMerge_UserStateHoldsBothStripes(t *testing.T) {
	for _, held := range []string{"loser", "winner"} {
		t.Run(held, func(t *testing.T) {
			s, winner, loser, user := seedFollowPair(t)
			book := loser
			if held == "winner" {
				book = winner
			}
			unlock := database.LockUserBookState(user, book)
			done := make(chan error, 1)
			go func() { done <- FollowMerge(s, nil, winner, []string{loser}) }()
			select {
			case err := <-done:
				unlock()
				t.Fatalf("the follow ran while the %s's user-state stripe was held: %v", held, err)
			case <-time.After(150 * time.Millisecond):
			}
			unlock()
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(10 * time.Second):
				t.Fatal("the follow never ran after the stripe was released")
			}
		})
	}
}

// The owner's discard waits for the user-state stripe across its read and
// clear, so a device sync cannot land between them and be wiped unseen.
func TestDiscardUserStateThenHardDelete_HoldsUserStateLock(t *testing.T) {
	s, _, loser, user := seedFollowPair(t)
	unlock := database.LockUserBookState(user, loser)
	done := make(chan error, 1)
	go func() {
		_, err := discardUserStateThenHardDelete(s, loser, nil, func() error { return nil })
		done <- err
	}()
	select {
	case err := <-done:
		unlock()
		t.Fatalf("the discard ran while the user-state stripe was held: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	unlock()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("the discard never ran after the stripe was released")
	}
	rows, err := s.ListUserPositionsForBook(user, loser)
	require.NoError(t, err)
	require.Empty(t, rows)
}

// FollowAbsorbedJournaled (sibling moves, split-book and chapter merges,
// retire-into, carry-before-delete) holds the user's stripes on both books
// from its before-snapshot through its after-snapshot: a device write to the
// absorbed book after the before-snapshot read waits, lands after the follow,
// and the restore keeps it. Unlocked it was carried to the survivor, then
// lost on restore (in neither the absorbed snapshot nor the survivor's
// before-state).
func TestFollowAbsorbedJournaled_UserStateRaceAbsorbedWriteKeptByRestore(t *testing.T) {
	s, survivor, absorbed, user := seedFollowPair(t)
	var wait func() error
	hooked := &stateReadHook{PebbleStore: s, book: absorbed, onPositions: true}
	hooked.onRead = func() {
		wait = stripedClientWrite(user, absorbed, func() error {
			return s.SetUserPositionAt(user, absorbed, "late", 777, time.Now().UTC())
		})
	}
	progress, redirected, err := FollowAbsorbedJournaled(hooked, survivor, absorbed, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, wait, "the journaled follow never read the absorbed book's positions")
	require.NoError(t, wait())
	LockMergeRMW()
	_, err = RestoreFollowedProgress(s, survivor, absorbed, redirected, progress)
	UnlockMergeRMW()
	require.NoError(t, err)
	require.True(t, hasPosition(t, s, user, "late", 777, absorbed, survivor),
		"the device write made during the journaled follow is on neither book after the restore")
}

// The discard holds every user's stripe through the hard delete: a device
// write started from inside del waits until the delete has run.
func TestDiscardUserStateThenHardDelete_UserStateHeldThroughDelete(t *testing.T) {
	s, _, loser, user := seedFollowPair(t)
	var blocked bool
	_, err := discardUserStateThenHardDelete(s, loser, nil, func() error {
		done := make(chan struct{})
		go func() {
			defer close(done)
			database.LockUserBookState(user, loser)()
		}()
		select {
		case <-done:
		case <-time.After(150 * time.Millisecond):
			blocked = true
		}
		return nil
	})
	require.NoError(t, err)
	require.True(t, blocked, "a writer took the user-state stripe inside the discard's hard delete")
}
