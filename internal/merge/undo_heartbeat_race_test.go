// file: internal/merge/undo_heartbeat_race_test.go
// version: 1.0.0
// guid: fa81dcd0-b54f-4e6e-b6c0-e3426100e477
// last-edited: 2026-10-18

package merge

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/readstatus"
)

// replaceHookStore runs onReplace once, when the undo's survivor put-back
// calls ReplaceUserPositions on the survivor: after the put-back read the
// survivor and decided what to write, before it writes.
type replaceHookStore struct {
	*database.PebbleStore
	survivor  string
	once      sync.Once
	onReplace func()
}

func (s *replaceHookStore) ReplaceUserPositions(userID, bookID string, positions []database.UserPosition) error {
	if bookID == s.survivor && s.onReplace != nil {
		s.once.Do(s.onReplace)
	}
	return s.PebbleStore.ReplaceUserPositions(userID, bookID, positions)
}

// Round-5 probe P1, through a real writer: a web reading heartbeat (a
// position write and readstatus' recompute, in one hold of the user-state
// stripe -- what handlers.ReadingHandler.SetPosition runs; handlers imports
// merge, so it is reproduced here) fires while the combine undo's survivor
// put-back is between its read and its ReplaceUserPositions. The heartbeat
// waits for the put-back's stripe and lands on top, so its position survives
// the undo. Before 2026-10-18 the heartbeat took no stripe: it landed at
// once, and the put-back's ReplaceUserPositions erased it.
//
// The original P1 probe (scratchpad undo_rev5/renamed/probe_rev5_test.go)
// cannot pass: its "client" write is a raw store call made synchronously
// INSIDE ReplaceUserPositions, on the undo's own goroutine, while the undo
// holds the stripe. No lock covers a write made from inside the lock
// holder's own store call, and making that write take the stripe would
// self-deadlock. It models an unlocked writer; this test replaces it with
// the real one.
func TestUndoCombine_UserStateRaceHeartbeatDuringPutBackKept(t *testing.T) {
	store := setupTestStore(t).(*database.PebbleStore)
	f := seedUndoFixture(t, store)
	at := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	require.NoError(t, store.SetUserBookState(&database.UserBookState{UserID: f.user.ID, BookID: f.absA, Status: "in_progress", ProgressPct: 40, LastActivityAt: at, LastSegmentID: "seg-a", TotalListenedSeconds: 600}))
	require.NoError(t, store.SetUserBookState(&database.UserBookState{UserID: f.user.ID, BookID: f.survivor, Status: "in_progress", ProgressPct: 10, LastActivityAt: at.Add(-time.Hour), LastSegmentID: "seg-s", TotalListenedSeconds: 100}))
	res, err := NewService(store).CombineBooks([]string{f.survivor, f.absA, f.absB}, f.survivor, nil)
	require.NoError(t, err)

	hb := make(chan error, 1)
	stillBlocked := false
	w := &replaceHookStore{PebbleStore: store, survivor: f.survivor}
	w.onReplace = func() {
		go func() {
			defer database.LockUserBookState(f.user.ID, f.survivor)()
			if err := store.SetUserPosition(f.user.ID, f.survivor, "seg-heartbeat", 4321); err != nil {
				hb <- err
				return
			}
			_, err := readstatus.RecomputeUserBookStateLocked(store, f.user.ID, f.survivor)
			hb <- err
		}()
		select {
		case err := <-hb:
			hb <- err // landed inside the put-back: re-queue for the join below
		case <-time.After(200 * time.Millisecond):
			stillBlocked = true
		}
	}
	_, err = NewService(w).UndoCombine(res.JournalID)
	require.NoError(t, err)
	require.True(t, stillBlocked, "the heartbeat landed inside the put-back's read-decide-write instead of waiting for its stripe")
	require.NoError(t, <-hb)

	rows, err := store.ListUserPositionsForBook(f.user.ID, f.survivor)
	require.NoError(t, err)
	found := false
	for _, p := range rows {
		if p.SegmentID == "seg-heartbeat" && p.PositionSeconds == 4321 {
			found = true
		}
	}
	require.True(t, found, "the heartbeat's position was erased by the undo's put-back: %+v", rows)
	st, err := store.GetUserBookState(f.user.ID, f.survivor)
	require.NoError(t, err)
	require.NotNil(t, st)
	require.Equal(t, "seg-heartbeat", st.LastSegmentID, "the heartbeat's recompute did not land last")
}
