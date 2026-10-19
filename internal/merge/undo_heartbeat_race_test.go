// file: internal/merge/undo_heartbeat_race_test.go
// version: 1.1.0
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

// combineHookStore runs onAbsPositions once at the combine's first read of
// the absorbed book's positions (the before-snapshot), and onAbsClear once
// when the follow drains the absorbed book's positions (its last move,
// before the after-snapshot).
type combineHookStore struct {
	*database.PebbleStore
	absorbed       string
	posOnce        sync.Once
	clearOnce      sync.Once
	onAbsPositions func()
	onAbsClear     func()
}

func (s *combineHookStore) ListUserPositionsForBook(userID, bookID string) ([]database.UserPosition, error) {
	rows, err := s.PebbleStore.ListUserPositionsForBook(userID, bookID)
	if bookID == s.absorbed && s.onAbsPositions != nil {
		s.posOnce.Do(s.onAbsPositions)
	}
	return rows, err
}

func (s *combineHookStore) ClearUserPositions(userID, bookID string) error {
	err := s.PebbleStore.ClearUserPositions(userID, bookID)
	if bookID == s.absorbed && s.onAbsClear != nil {
		s.clearOnce.Do(s.onAbsClear)
	}
	return err
}

// stripedWriteAsync starts write under the (user, book) stripe and reports
// whether it was still blocked 200ms later; wait joins it.
func stripedWriteAsync(user, book string, write func() error) (blocked bool, wait func() error) {
	done := make(chan error, 1)
	go func() {
		defer database.LockUserBookState(user, book)()
		done <- write()
	}()
	select {
	case err := <-done:
		return false, func() error { return err }
	case <-time.After(200 * time.Millisecond):
		return true, func() error { return <-done }
	}
}

func positionOn(t *testing.T, s *database.PebbleStore, user, seg string, books ...string) bool {
	t.Helper()
	for _, b := range books {
		rows, err := s.ListUserPositionsForBook(user, b)
		require.NoError(t, err)
		for _, p := range rows {
			if p.SegmentID == seg {
				return true
			}
		}
	}
	return false
}

// A device write to the absorbed book landing between the combine's
// before-snapshot and its follow survives the undo. The combine holds the
// user's stripes from the before-snapshot through the after-snapshot, so the
// write waits and lands on the (drained) absorbed book afterwards, where the
// undo's restore combines it with the snapshot. Before 2026-10-18 it landed
// at once: the follow carried it to the survivor, the undo put the survivor
// back to its before-state and restored the absorbed book from a snapshot
// that never held it, and it ended up on neither book.
func TestUndoCombine_UserStateRaceAbsorbedWriteBeforeFollowKept(t *testing.T) {
	store := setupTestStore(t).(*database.PebbleStore)
	f := seedUndoFixture(t, store)
	var blocked bool
	var wait func() error
	w := &combineHookStore{PebbleStore: store, absorbed: f.absA}
	w.onAbsPositions = func() {
		blocked, wait = stripedWriteAsync(f.user.ID, f.absA, func() error {
			return store.SetUserPosition(f.user.ID, f.absA, "seg-late-abs", 2222)
		})
	}
	res, err := NewService(w).CombineBooks([]string{f.survivor, f.absA, f.absB}, f.survivor, nil)
	require.NoError(t, err)
	require.NotNil(t, wait, "the combine never read the absorbed book's positions")
	require.True(t, blocked, "the device write landed inside the combine's snapshot-follow span")
	require.NoError(t, wait())
	_, err = NewService(store).UndoCombine(res.JournalID)
	require.NoError(t, err)
	require.True(t, positionOn(t, store, f.user.ID, "seg-late-abs", f.absA, f.survivor),
		"the device write made during the combine is on neither book after the undo")
}

// A device write to the survivor landing between the follow and the
// after-snapshot survives the undo. With the stripes held through the
// after-snapshot the write lands after it, so the undo sees the survivor
// touched since the follow and keeps it. Before 2026-10-18 the after-snapshot
// recorded it as the follow's own write and the undo put the survivor back
// over it.
func TestUndoCombine_UserStateRaceSurvivorWriteBeforeAfterSnapshotKept(t *testing.T) {
	store := setupTestStore(t).(*database.PebbleStore)
	f := seedUndoFixture(t, store)
	var blocked bool
	var wait func() error
	w := &combineHookStore{PebbleStore: store, absorbed: f.absA}
	w.onAbsClear = func() {
		blocked, wait = stripedWriteAsync(f.user.ID, f.survivor, func() error {
			return store.SetUserPosition(f.user.ID, f.survivor, "seg-late-surv", 3333)
		})
	}
	res, err := NewService(w).CombineBooks([]string{f.survivor, f.absA, f.absB}, f.survivor, nil)
	require.NoError(t, err)
	require.NotNil(t, wait, "the follow never drained the absorbed book's positions")
	require.True(t, blocked, "the device write landed between the follow and the after-snapshot")
	require.NoError(t, wait())
	_, err = NewService(store).UndoCombine(res.JournalID)
	require.NoError(t, err)
	require.True(t, positionOn(t, store, f.user.ID, "seg-late-surv", f.survivor, f.absA),
		"the survivor write made during the combine was put back over by the undo")
}
