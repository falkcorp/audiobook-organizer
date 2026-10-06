// file: internal/merge/merge_carry_followups_test.go
// version: 1.0.0
// guid: d83771a3-8e26-4d42-94a4-742624eb59ee
// last-edited: 2026-10-06

package merge

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Review follow-ups to #3777 (merge-state carry).

// positionWriteAfterRead lands a client position write on one book right
// after the FIRST read of that book's positions has been taken, and returns
// that (now stale) read: a device sync arriving between a reader's read and
// its write.
type positionWriteAfterRead struct {
	*database.PebbleStore
	t       *testing.T
	book    string
	segment string
	seconds float64
	mu      sync.Mutex
	done    bool
}

func (s *positionWriteAfterRead) ListUserPositionsForBook(userID, bookID string) ([]database.UserPosition, error) {
	got, err := s.PebbleStore.ListUserPositionsForBook(userID, bookID)
	if err != nil || bookID != s.book {
		return got, err
	}
	s.mu.Lock()
	fire := !s.done
	s.done = true
	s.mu.Unlock()
	if fire {
		require.NoError(s.t, s.PebbleStore.SetUserPosition(userID, bookID, s.segment, s.seconds))
	}
	return got, nil
}

// Item 2: the touched-survivor reconcile wrote the positions BEFORE taking
// the per-(user, book) stripe and checking the survivor had not moved on, so
// a client position landing after its read was rewound. Segment "a" on keep
// is exactly the row the follow wrote (it matches the after-snapshot), so the
// stale reconcile would put keep's before row (100s) back over the user's
// new 2000s.
func TestReconcileTouchedSurvivor_NewerPositionIsNotRewound(t *testing.T) {
	s, u, keep, dup, progress, redirected := touchedSurvivorSetup(t)
	cw := &positionWriteAfterRead{PebbleStore: s, t: t, book: keep, segment: "a", seconds: 2000}
	_, err := RestoreFollowedProgress(cw, keep, dup, redirected, progress)
	require.NoError(t, err)

	kpos, err := s.ListUserPositionsForBook(u.ID, keep)
	require.NoError(t, err)
	got := map[string]float64{}
	for _, p := range kpos {
		got[p.SegmentID] = p.PositionSeconds
	}
	require.Equal(t, 2000.0, got["a"], "the position written after the reconcile's read is kept, not rewound to 100")
	require.Equal(t, 50.0, got["b"], "the user's earlier listening on keep stays")
}

// Item 2, absorbed side: restoreAbsorbedSide read the absorbed book and
// wrote the combined result with no stripe at all. Its read and write are
// now one step under database.LockUserBookState, so a writer holding the
// stripe (every ABS write path) cannot land between them: while one holds
// it, the restore waits.
func TestRestoreAbsorbedSide_HoldsTheUserBookStripe(t *testing.T) {
	s := setupTestStore(t).(*database.PebbleStore)
	_, dup := seedSyncBooks(t, s)
	u := seedSyncUser(t, s)
	require.NoError(t, s.SetUserPositionAt(u.ID, dup, "a", 10, rfT0))

	unlock := database.LockUserBookState(u.ID, dup)
	done := make(chan struct{})
	var restoreErr error
	go func() {
		defer close(done)
		_, restoreErr = restoreAbsorbedSide(s, u.ID, dup, nil, []database.UserPosition{rfPos("a", 500, rfT1)})
	}()
	select {
	case <-done:
		unlock()
		t.Fatal("restoreAbsorbedSide ran while another writer held the (user, book) stripe")
	case <-time.After(200 * time.Millisecond):
	}
	unlock()
	<-done
	require.NoError(t, restoreErr)
}

// syncGraphFaultStore returns a chosen error from ResolveSyncItem or
// ListSyncAliases (the alias cap is unexported in package database, so a
// real over-the-cap graph cannot be built from here).
type syncGraphFaultStore struct {
	*database.PebbleStore
	resolveErr error
	aliasErr   error
}

func (s *syncGraphFaultStore) ResolveSyncItem(syncID string) (*database.SyncItem, error) {
	if s.resolveErr != nil {
		return nil, s.resolveErr
	}
	return s.PebbleStore.ResolveSyncItem(syncID)
}

func (s *syncGraphFaultStore) ListSyncAliases(syncID string) ([]string, error) {
	if s.aliasErr != nil {
		return nil, s.aliasErr
	}
	return s.PebbleStore.ListSyncAliases(syncID)
}

// Item 4: a broken redirect chain and an alias graph over the cap are
// permanent; the probe reports each with its own sentinel (keeping the
// database error in the chain), and an ordinary read error with neither.
func TestUserStateProbe_PermanentSyncGraphErrorsAreDistinct(t *testing.T) {
	s := setupTestStore(t).(*database.PebbleStore)
	keep, dup := seedSyncBooks(t, s)
	ids := database.AsSyncIdentityStore(s)
	for _, id := range []string{keep, dup} {
		_, err := ids.MintOrGetSyncID(id)
		require.NoError(t, err)
	}

	t.Run("real dangling redirect", func(t *testing.T) {
		require.NoError(t, s.RecordSyncMerge(dup, keep))
		keepSync, _, err := s.GetSyncIDForBook(keep)
		require.NoError(t, err)
		raw, err := s.GetRaw("sync_item:" + keepSync)
		require.NoError(t, err)
		require.NotNil(t, raw, "precondition: the winner's sync item exists")
		require.NoError(t, s.DeleteRaw("sync_item:"+keepSync))
		t.Cleanup(func() { _ = s.SetRaw("sync_item:"+keepSync, raw) })

		_, err = BookHasCarryableUserState(s, dup)
		require.ErrorIs(t, err, ErrBookmarkCheckRedirectBroken)
		require.ErrorIs(t, err, database.ErrSyncRedirectChainBroken)
		require.NotErrorIs(t, err, ErrBookmarkCheckAliasLimit)
	})
	t.Run("alias cap", func(t *testing.T) {
		fs := &syncGraphFaultStore{PebbleStore: s, aliasErr: fmt.Errorf("%w: starting at x", database.ErrSyncAliasLimit)}
		_, err := BookHasCarryableUserState(fs, keep)
		require.ErrorIs(t, err, ErrBookmarkCheckAliasLimit)
		require.ErrorIs(t, err, database.ErrSyncAliasLimit)
		require.NotErrorIs(t, err, ErrBookmarkCheckRedirectBroken)
	})
	t.Run("plain read error", func(t *testing.T) {
		io := errors.New("injected pebble read error")
		fs := &syncGraphFaultStore{PebbleStore: s, resolveErr: io}
		_, err := BookHasCarryableUserState(fs, keep)
		require.ErrorIs(t, err, io)
		require.NotErrorIs(t, err, ErrBookmarkCheckRedirectBroken)
		require.NotErrorIs(t, err, ErrBookmarkCheckAliasLimit)
	})
}
