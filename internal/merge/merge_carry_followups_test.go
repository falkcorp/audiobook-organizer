// file: internal/merge/merge_carry_followups_test.go
// version: 1.0.0
// guid: d83771a3-8e26-4d42-94a4-742624eb59ee
// last-edited: 2026-10-06

package merge

import (
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
