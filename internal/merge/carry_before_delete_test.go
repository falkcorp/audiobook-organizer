// file: internal/merge/carry_before_delete_test.go
// version: 1.0.0
// guid: 8b2e4d61-3c7a-4f95-b1d8-6a0c9e2f5b47
// last-edited: 2026-10-05

package merge

import (
	"fmt"
	"sync"
	"testing"

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

// The reversal fails too (B's writes fail in both directions). A record
// holds the owed move, and once the store is healthy the next attempt
// completes it forward.
func TestCarryStateBeforeHardDelete_FailedPutBackKeepsTheRecord(t *testing.T) {
	c := newCarryFixture(t)
	c.fs.failState[c.usB.ID+"/"+c.keep] = true
	c.fs.failState[c.usB.ID+"/"+c.dup] = true
	err := CarryStateBeforeHardDelete(c.fs, c.keep, c.dup)
	require.ErrorIs(t, err, ErrStateCarryIncomplete)
	require.ErrorContains(t, err, "a pending user-state repair holds the move")
	require.Contains(t, pendingKeys(t, c.s), pendingRepairKey(c.dup, c.keep))

	c.fs.mu.Lock()
	c.fs.failState = map[string]bool{}
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
