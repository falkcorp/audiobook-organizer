// file: internal/merge/carry_before_delete_test.go
// version: 1.4.0
// guid: 8b2e4d61-3c7a-4f95-b1d8-6a0c9e2f5b47
// last-edited: 2026-10-05

package merge

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/syncapi/progress"
)

// carryFaultStore fails chosen writes of the carry, per user and book.
type carryFaultStore struct {
	*database.PebbleStore
	mu sync.Mutex
	// failState fails SetUserBookState for "user/book".
	failState map[string]bool
	// failBookmarks fails every bookmark copy (CopyBookmarkIfAbsent).
	failBookmarks bool
	// failRedirect fails RecordSyncMerge.
	failRedirect bool
	// failClearRedirect fails ClearSyncMerge, the put-back's first step.
	failClearRedirect bool
	// afterClear runs after each ClearUserPositions, as a client writing
	// progress to the book while the carry runs would.
	afterClear func(userID, bookID string)
}

func (s *carryFaultStore) ClearUserPositions(userID, bookID string) error {
	if err := s.PebbleStore.ClearUserPositions(userID, bookID); err != nil {
		return err
	}
	s.mu.Lock()
	hook := s.afterClear
	s.mu.Unlock()
	if hook != nil {
		hook(userID, bookID)
	}
	return nil
}

func (s *carryFaultStore) SetUserBookState(st *database.UserBookState) error {
	s.mu.Lock()
	fail := s.failState[st.UserID+"/"+st.BookID]
	s.mu.Unlock()
	if fail {
		return fmt.Errorf("injected SetUserBookState failure for %s/%s", st.UserID, st.BookID)
	}
	return s.PebbleStore.SetUserBookState(st)
}

func (s *carryFaultStore) CopyBookmarkIfAbsent(b progress.Bookmark) (bool, error) {
	s.mu.Lock()
	fail := s.failBookmarks
	s.mu.Unlock()
	if fail {
		return false, fmt.Errorf("injected bookmark copy failure")
	}
	return s.PebbleStore.CopyBookmarkIfAbsent(b)
}

func (s *carryFaultStore) RecordSyncMerge(loser, winner string) error {
	s.mu.Lock()
	fail := s.failRedirect
	s.mu.Unlock()
	if fail {
		return fmt.Errorf("injected RecordSyncMerge failure")
	}
	return s.PebbleStore.RecordSyncMerge(loser, winner)
}

func (s *carryFaultStore) ClearSyncMerge(loser, winner string) error {
	s.mu.Lock()
	fail := s.failClearRedirect
	s.mu.Unlock()
	if fail {
		return fmt.Errorf("injected ClearSyncMerge failure")
	}
	return s.PebbleStore.ClearSyncMerge(loser, winner)
}

type carryFixture struct {
	s          *database.PebbleStore
	fs         *carryFaultStore
	keep, dup  string
	userA, usB *database.User
}

// newCarryFixture: two users, each with progress on dup (A 40%, B 70%), and
// both books with a sync id, as ABS-visible books have.
func newCarryFixture(t *testing.T) *carryFixture {
	t.Helper()
	s := setupTestStore(t).(*database.PebbleStore)
	keep, dup := seedSyncBooks(t, s)
	a, err := s.CreateUser("a", "a@example.com", "argon2id", "x", []string{"user"}, "active")
	require.NoError(t, err)
	b, err := s.CreateUser("b", "b@example.com", "argon2id", "x", []string{"user"}, "active")
	require.NoError(t, err)
	seedProgress(t, s, a.ID, dup, 40)
	seedProgress(t, s, b.ID, dup, 70)
	ids := database.AsSyncIdentityStore(s)
	for _, id := range []string{keep, dup} {
		_, err := ids.MintOrGetSyncID(id)
		require.NoError(t, err)
	}
	return &carryFixture{s: s, fs: &carryFaultStore{PebbleStore: s, failState: map[string]bool{}}, keep: keep, dup: dup, userA: a, usB: b}
}

func (c *carryFixture) pct(t *testing.T, u *database.User, book string) int {
	return sfProgress(t, c.s, u.ID, book)
}

func TestCarryStateBeforeHardDelete_MovesEverything(t *testing.T) {
	c := newCarryFixture(t)
	require.NoError(t, CarryStateBeforeHardDelete(c.fs, c.keep, c.dup))
	require.Equal(t, 40, c.pct(t, c.userA, c.keep))
	require.Equal(t, 70, c.pct(t, c.usB, c.keep))
	has, err := BookHasCarryableUserState(c.s, c.dup)
	require.NoError(t, err)
	require.False(t, has)
	require.Empty(t, pendingKeys(t, c.s))
	require.Equal(t, c.keep, sfCurrentBook(t, c.s, c.dup), "ABS identity follows")
}

// A partial carry (A moves, B's write onto keep fails) is reversed: both
// users end on dup, the book that stays, nothing on keep, no record left.
func TestCarryStateBeforeHardDelete_PartialIsPutBack(t *testing.T) {
	c := newCarryFixture(t)
	c.fs.failState[c.usB.ID+"/"+c.keep] = true
	err := CarryStateBeforeHardDelete(c.fs, c.keep, c.dup)
	require.ErrorIs(t, err, ErrStateCarryIncomplete)
	require.Equal(t, 40, c.pct(t, c.userA, c.dup), "A's moved state is back on the kept-live book")
	require.Equal(t, 70, c.pct(t, c.usB, c.dup))
	require.Zero(t, c.pct(t, c.userA, c.keep))
	require.Zero(t, c.pct(t, c.usB, c.keep))
	require.Empty(t, pendingKeys(t, c.s), "nothing is owed: the state is where it was")
	require.Equal(t, c.dup, sfCurrentBook(t, c.s, c.dup), "the redirect is cleared")
}

// The reversal fails too (B's write onto keep fails, then clearing the
// redirect fails). A record holds the owed move, and once the store is
// healthy the next attempt completes it forward. (Failing B's writes onto
// dup no longer fails the reversal: B never left dup, so the put-back leaves
// B's unchanged rows alone instead of rewriting them.)
func TestCarryStateBeforeHardDelete_FailedPutBackKeepsTheRecord(t *testing.T) {
	c := newCarryFixture(t)
	c.fs.failState[c.usB.ID+"/"+c.keep] = true
	c.fs.failClearRedirect = true
	err := CarryStateBeforeHardDelete(c.fs, c.keep, c.dup)
	require.ErrorIs(t, err, ErrStateCarryIncomplete)
	require.ErrorContains(t, err, "a pending user-state repair holds the move")
	require.Contains(t, pendingKeys(t, c.s), pendingRepairKey(c.dup, c.keep))

	c.fs.mu.Lock()
	c.fs.failState = map[string]bool{}
	c.fs.failClearRedirect = false
	c.fs.mu.Unlock()
	require.NoError(t, CarryStateBeforeHardDelete(c.fs, c.keep, c.dup))
	require.Equal(t, 40, c.pct(t, c.userA, c.keep))
	require.Equal(t, 70, c.pct(t, c.usB, c.keep))
	require.Empty(t, pendingKeys(t, c.s))
}

// The post-check covers what the state probe cannot see: a bookmark copy or
// the ABS redirect that failed refuses the carry and puts the state back.
func TestCarryStateBeforeHardDelete_BookmarksAndRedirectAreChecked(t *testing.T) {
	t.Run("bookmark copy fails", func(t *testing.T) {
		c := newCarryFixture(t)
		ids := database.AsSyncIdentityStore(c.s)
		dupSync, _, err := ids.GetSyncIDForBook(c.dup)
		require.NoError(t, err)
		require.NoError(t, c.s.CreateBookmark(progress.Bookmark{UserID: c.userA.ID, ItemID: dupSync, TimeSec: 12, Title: "mark"}))
		c.fs.failBookmarks = true
		err = CarryStateBeforeHardDelete(c.fs, c.keep, c.dup)
		require.ErrorIs(t, err, ErrStateCarryIncomplete)
		require.Equal(t, 40, c.pct(t, c.userA, c.dup))
		require.Empty(t, pendingKeys(t, c.s))
	})
	t.Run("redirect fails", func(t *testing.T) {
		c := newCarryFixture(t)
		c.fs.failRedirect = true
		err := CarryStateBeforeHardDelete(c.fs, c.keep, c.dup)
		require.ErrorIs(t, err, ErrStateCarryIncomplete)
		require.Equal(t, 70, c.pct(t, c.usB, c.dup))
		require.Equal(t, c.dup, sfCurrentBook(t, c.s, c.dup))
	})
}

// State that lands on dup after the follow drained it (a client still
// writing to it) is caught by the re-read, and the carry is put back. The
// put-back does not rewind that write (#3769 review SF1): the client's newer
// position stays the dup's current one, and the user's state from before the
// carry (40%) is back on the dup with it.
func TestCarryStateBeforeHardDelete_StateWrittenDuringCarryRefuses(t *testing.T) {
	c := newCarryFixture(t)
	var once sync.Once
	c.fs.afterClear = func(userID, bookID string) {
		if userID == c.userA.ID && bookID == c.dup {
			once.Do(func() {
				require.NoError(t, c.s.SetUserPosition(userID, bookID, "seg", 99))
			})
		}
	}
	err := CarryStateBeforeHardDelete(c.fs, c.keep, c.dup)
	require.ErrorIs(t, err, ErrStateCarryIncomplete)
	require.ErrorContains(t, err, "still on")
	require.Equal(t, 70, c.pct(t, c.usB, c.dup), "the carry was put back")

	pos, err := c.s.ListUserPositionsForBook(c.userA.ID, c.dup)
	require.NoError(t, err)
	require.Len(t, pos, 1)
	require.Equal(t, 99.0, pos[0].PositionSeconds, "the client's newer position is not rewound")
	require.Equal(t, 40, c.pct(t, c.userA, c.dup), "the user's state from before the carry is back on the dup")
	st, err := c.s.GetUserBookState(c.userA.ID, c.dup)
	require.NoError(t, err)
	require.Equal(t, database.UserBookStatusInProgress, st.Status)
	require.Zero(t, c.pct(t, c.userA, c.keep), "nothing is left on keep")
	require.Empty(t, pendingKeys(t, c.s))
}

// combineOnRestore keeps the newer side's position and loses nothing else.
func TestCombineOnRestore(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Hour)
	ubs := func(status string, pct int, listened float64, at time.Time) *database.UserBookState {
		return &database.UserBookState{Status: status, ProgressPct: pct, TotalListenedSeconds: listened, LastActivityAt: at}
	}
	pos := func(seg string, sec float64, at time.Time) database.UserPosition {
		return database.UserPosition{SegmentID: seg, PositionSeconds: sec, UpdatedAt: at}
	}

	t.Run("drained residue plus a newer position", func(t *testing.T) {
		snap := userStateSide{state: ubs(database.UserBookStatusInProgress, 40, 3600, t0), positions: []database.UserPosition{pos("a", 40, t0), pos("b", 5, t0)}}
		now := userStateSide{state: drainedUserState(*ubs(database.UserBookStatusInProgress, 40, 3600, t0)), positions: []database.UserPosition{pos("a", 99, t1)}}
		st, got := combineOnRestore("u", "bk", snap, now)
		require.Equal(t, 40, st.ProgressPct, "a drained row does not win with an empty status")
		require.Equal(t, database.UserBookStatusInProgress, st.Status)
		require.Equal(t, 3600.0, st.TotalListenedSeconds)
		require.Equal(t, "a", st.LastSegmentID)
		require.ElementsMatch(t, []database.UserPosition{pos("a", 99, t1), pos("b", 5, t0)}, got, "newer per segment, the other segment kept")
	})
	t.Run("newer client state wins position, listened time is not lost, finished stays sticky", func(t *testing.T) {
		snap := userStateSide{state: ubs(database.UserBookStatusFinished, 100, 9000, t0), positions: []database.UserPosition{pos("a", 500, t0)}}
		now := userStateSide{state: ubs(database.UserBookStatusInProgress, 3, 20, t1), positions: []database.UserPosition{pos("a", 30, t1)}}
		st, got := combineOnRestore("u", "bk", snap, now)
		require.Equal(t, database.UserBookStatusFinished, st.Status)
		require.Equal(t, 9000.0, st.TotalListenedSeconds)
		require.True(t, st.LastActivityAt.Equal(t1))
		require.Equal(t, []database.UserPosition{pos("a", 30, t1)}, got)
	})
	t.Run("older current residue of a partial follow", func(t *testing.T) {
		snap := userStateSide{state: ubs(database.UserBookStatusInProgress, 40, 100, t0), positions: []database.UserPosition{pos("a", 40, t0)}}
		now := userStateSide{state: ubs(database.UserBookStatusInProgress, 40, 100, t0)}
		st, got := combineOnRestore("u", "bk", snap, now)
		require.Equal(t, 40, st.ProgressPct)
		require.Equal(t, []database.UserPosition{pos("a", 40, t0)}, got)
	})
	t.Run("a reset since is kept", func(t *testing.T) {
		snap := userStateSide{state: ubs(database.UserBookStatusInProgress, 40, 100, t0), positions: []database.UserPosition{pos("a", 40, t0)}}
		reset := ubs(database.UserBookStatusUnstarted, 0, 0, t1)
		reset.ProgressResetAt = &t1
		st, got := combineOnRestore("u", "bk", snap, userStateSide{state: reset})
		require.Equal(t, database.UserBookStatusUnstarted, st.Status)
		require.Zero(t, st.TotalListenedSeconds)
		require.Empty(t, got, "the reset position is not brought back")
	})
}

// Through a decorator that hides the concrete store, as the production
// indexedStore does: the sync layer, bookmarks and copier still resolve, so
// the carry moves the redirect and the bookmarks, not only the progress.
func TestCarryStateBeforeHardDelete_ThroughDecorator(t *testing.T) {
	c := newCarryFixture(t)
	ids := database.AsSyncIdentityStore(c.s)
	dupSync, _, err := ids.GetSyncIDForBook(c.dup)
	require.NoError(t, err)
	keepSync, _, err := ids.GetSyncIDForBook(c.keep)
	require.NoError(t, err)
	require.NoError(t, c.s.CreateBookmark(progress.Bookmark{UserID: c.userA.ID, ItemID: dupSync, TimeSec: 12, Title: "mark"}))

	require.NoError(t, CarryStateBeforeHardDelete(probeDecorator{Store: c.s}, c.keep, c.dup))
	require.Equal(t, c.keep, sfCurrentBook(t, c.s, c.dup), "the redirect is set")
	marks, err := c.s.ListBookmarks(c.userA.ID, keepSync)
	require.NoError(t, err)
	require.Len(t, marks, 1, "the bookmark landed on keep")
	require.Equal(t, 40, c.pct(t, c.userA, c.keep))
}

// An undecodable repair record blocks only the carry its key names.
func TestCarryStateBeforeHardDelete_UndecodableRecordBlocksOnlyItsPair(t *testing.T) {
	c := newCarryFixture(t)
	require.NoError(t, c.s.SetRaw(PendingUserStateRepairPrefix+"elsewhere:other", []byte("{bad")))
	require.NoError(t, CarryStateBeforeHardDelete(c.fs, c.keep, c.dup), "an unrelated bad record does not block")

	c2 := newCarryFixture(t)
	require.NoError(t, c2.s.SetRaw(PendingUserStateRepairPrefix+"x:"+c2.dup, []byte("{bad")))
	require.ErrorIs(t, CarryStateBeforeHardDelete(c2.fs, c2.keep, c2.dup), ErrStateCarryIncomplete)
	require.Equal(t, 40, c2.pct(t, c2.userA, c2.dup))
}

// An open repair naming dup (a move still owed into it) refuses the carry.
func TestCarryStateBeforeHardDelete_OpenRepairOnDoomedRefuses(t *testing.T) {
	c := newCarryFixture(t)
	other, _ := seedSyncBooks(t, c.s)
	require.NoError(t, putPendingRepair(c.s, PendingUserStateRepair{LoserBookID: other, WinnerBookID: c.dup}))
	err := CarryStateBeforeHardDelete(c.fs, c.keep, c.dup)
	require.ErrorIs(t, err, ErrStateCarryIncomplete)
	require.Equal(t, 40, c.pct(t, c.userA, c.dup))
}

// #3764 follow-up NIT 7: an undecodable user row makes the probe fail closed,
// on the bare store and through a decorator (AsCapability unwraps).
func TestUserStateProbe_UndecodableUserRowFailsClosed(t *testing.T) {
	c := newCarryFixture(t)
	require.NoError(t, c.s.SetRaw("u:broken", []byte("{not json")))
	users, err := c.s.ListUsers()
	require.NoError(t, err, "ListUsers still skips it for its other callers")
	require.Len(t, users, 2)

	_, err = NewUserStateProbe(c.s)
	require.ErrorIs(t, err, database.ErrUndecodableUserRows)
	_, err = BookHasCarryableUserState(probeDecorator{Store: c.s}, c.dup)
	require.ErrorIs(t, err, database.ErrUndecodableUserRows, "resolved through the decorator")
	require.ErrorIs(t, CarryStateBeforeHardDelete(c.fs, c.keep, c.dup), ErrStateCarryIncomplete)
}

// probeDecorator hides the concrete store, as the production indexedStore
// does, and opts into unwrapping.
type probeDecorator struct{ database.Store }

func (d probeDecorator) Unwrap() database.Store { return d.Store }

// HardDeleteWithoutUserState runs the delete only when the re-check finds
// nothing, and says whether a refusal is state found or a check that failed.
func TestHardDeleteWithoutUserState(t *testing.T) {
	c := newCarryFixture(t)
	ran := false
	del := func() error { ran = true; return nil }

	err := HardDeleteWithoutUserState(c.fs, c.dup, del)
	require.ErrorIs(t, err, ErrUserStateOnDoomedBook, "dup holds A's and B's state")
	require.False(t, ran)

	require.NoError(t, HardDeleteWithoutUserState(c.fs, c.keep, del))
	require.True(t, ran, "keep holds nothing")

	ran = false
	require.NoError(t, c.s.SetRaw("u:broken", []byte("{not json")))
	err = HardDeleteWithoutUserState(c.fs, c.keep, del)
	require.ErrorIs(t, err, ErrUserStateCheckFailed)
	require.NotErrorIs(t, err, ErrUserStateOnDoomedBook)
	require.False(t, ran)
}

// #3770 review B1: a carry between two books that both stay live leaves no
// sync redirect between them in either direction, so each id resolves to its
// own book -- including after an earlier carry the other way.
func TestCarryStateBetweenLiveBooks_LeavesNoRedirect(t *testing.T) {
	c := newCarryFixture(t)
	require.NoError(t, c.s.RecordSyncMerge(c.keep, c.dup), "an earlier carry the other way")
	require.NoError(t, CarryStateBetweenLiveBooks(c.fs, c.keep, c.dup))
	require.Equal(t, 40, c.pct(t, c.userA, c.keep))
	require.Equal(t, 70, c.pct(t, c.usB, c.keep))
	require.Equal(t, c.keep, sfCurrentBook(t, c.s, c.keep))
	require.Equal(t, c.dup, sfCurrentBook(t, c.s, c.dup))
	require.Empty(t, pendingKeys(t, c.s))
}
