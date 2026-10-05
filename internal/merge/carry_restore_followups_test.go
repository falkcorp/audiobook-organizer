// file: internal/merge/carry_restore_followups_test.go
// version: 1.0.0
// guid: beba4668-6a7b-4db7-b6b6-636dd756c5e7
// last-edited: 2026-10-05

package merge

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

var (
	rfT0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	rfT1 = rfT0.Add(time.Hour)
	rfT2 = rfT0.Add(2 * time.Hour)
)

func rfPos(seg string, sec float64, at time.Time) database.UserPosition {
	return database.UserPosition{SegmentID: seg, PositionSeconds: sec, UpdatedAt: at}
}

// #3770 review S1a: finished is sticky, but not over a newer deliberate
// mark-unfinished on the same book (readstatus sets StatusManual and stamps
// LastActivityAt). The newer manual status wins.
func TestCombineOnRestore_NewerManualStatusBeatsStickyFinished(t *testing.T) {
	fin := rfT0
	snap := userStateSide{
		state:     &database.UserBookState{Status: database.UserBookStatusFinished, ProgressPct: 100, FinishedAt: &fin, LastActivityAt: rfT0, TotalListenedSeconds: 9000},
		positions: []database.UserPosition{rfPos("a", 500, rfT0)},
	}
	unfinished := &database.UserBookState{Status: database.UserBookStatusInProgress, StatusManual: true, ProgressPct: 30, LastActivityAt: rfT1}
	st, _ := combineOnRestore("u", "bk", snap, userStateSide{state: unfinished, positions: []database.UserPosition{rfPos("a", 150, rfT0.Add(time.Minute))}})
	require.Equal(t, database.UserBookStatusInProgress, st.Status, "the newer mark-unfinished wins over sticky finished")
	require.True(t, st.StatusManual)
	require.Equal(t, 30, st.ProgressPct)
	require.Nil(t, st.FinishedAt)
	require.Equal(t, 9000.0, st.TotalListenedSeconds, "listened time is still the larger")

	// An automatic (not manual) newer status does not beat it.
	auto := *unfinished
	auto.StatusManual = false
	st, _ = combineOnRestore("u", "bk", snap, userStateSide{state: &auto, positions: []database.UserPosition{rfPos("a", 150, rfT1)}})
	require.Equal(t, database.UserBookStatusFinished, st.Status)
	require.Equal(t, 100, st.ProgressPct)
}

// #3770 review S1b: the reset path ORs hide and tombstones the older side's
// positions and its own discarded positions, in both directions.
func TestCombineOnRestore_ResetKeptTombstonesTheOlderSide(t *testing.T) {
	t.Run("snapshot holds the newer reset", func(t *testing.T) {
		reset := &database.UserBookState{Status: database.UserBookStatusUnstarted, LastActivityAt: rfT2, ProgressResetAt: &rfT2, ProgressResetPositions: []float64{900}}
		older := &database.UserBookState{Status: database.UserBookStatusInProgress, ProgressPct: 40, TotalListenedSeconds: 700, LastActivityAt: rfT1,
			HideFromContinueListening: true, ProgressResetPositions: []float64{11}}
		st, got := combineOnRestore("u", "bk", userStateSide{state: reset},
			userStateSide{state: older, positions: []database.UserPosition{rfPos("a", 321, rfT1)}})
		require.Empty(t, got, "the older positions are not kept over the reset")
		require.Equal(t, database.UserBookStatusUnstarted, st.Status)
		require.Zero(t, st.TotalListenedSeconds)
		require.Empty(t, st.LastSegmentID, "no position remains for LastSegmentID to name")
		require.True(t, st.HideFromContinueListening, "hide is OR'd")
		require.Equal(t, []float64{11, 321, 900}, st.ProgressResetPositions, "older tombstone, discarded positions, then the reset's own (newest last)")
		require.True(t, st.ProgressResetAt.Equal(rfT2))
	})
	t.Run("current holds the newer reset", func(t *testing.T) {
		snap := userStateSide{state: &database.UserBookState{Status: database.UserBookStatusInProgress, ProgressPct: 40, LastActivityAt: rfT0, HideFromContinueListening: true},
			positions: []database.UserPosition{rfPos("a", 400, rfT0)}}
		reset := &database.UserBookState{Status: database.UserBookStatusUnstarted, LastActivityAt: rfT1, ProgressResetAt: &rfT1}
		st, got := combineOnRestore("u", "bk", snap, userStateSide{state: reset})
		require.Empty(t, got)
		require.True(t, st.HideFromContinueListening)
		require.Equal(t, []float64{400}, st.ProgressResetPositions, "the snapshot's position cannot be replayed back over the reset")
	})
}

// #3770 review S1b, through the store: when the snapshot holds the newer
// reset, the current positions it leaves out are cleared off the book, so
// the rows on the book agree with the state.
func TestRestoreAbsorbedSide_ResetInSnapshotClearsOlderRows(t *testing.T) {
	s := setupTestStore(t).(*database.PebbleStore)
	_, book := seedSyncBooks(t, s)
	u := seedSyncUser(t, s)
	require.NoError(t, s.SetUserBookState(&database.UserBookState{UserID: u.ID, BookID: book, Status: database.UserBookStatusInProgress, ProgressPct: 40, LastActivityAt: rfT1}))
	require.NoError(t, s.SetUserPositionAt(u.ID, book, "a", 321, rfT1))

	reset := &database.UserBookState{UserID: u.ID, BookID: book, Status: database.UserBookStatusUnstarted, LastActivityAt: rfT2, ProgressResetAt: &rfT2}
	_, err := restoreAbsorbedSide(s, u.ID, book, reset, nil)
	require.NoError(t, err)
	pos, err := s.ListUserPositionsForBook(u.ID, book)
	require.NoError(t, err)
	require.Empty(t, pos, "the positions the reset discards are cleared, not left beside it")
	st, err := s.GetUserBookState(u.ID, book)
	require.NoError(t, err)
	require.Equal(t, database.UserBookStatusUnstarted, st.Status)
	require.Equal(t, []float64{321}, st.ProgressResetPositions)
}

// #3770 review S1c (owner decision 2026-10-05): when either side of a
// position has no timestamp, the farther-ahead position wins, per segment
// and for which side's state stands.
func TestCombineOnRestore_UndatedPositionFartherAheadWins(t *testing.T) {
	t.Run("legacy current row ahead of a dated snapshot", func(t *testing.T) {
		snap := userStateSide{state: &database.UserBookState{Status: database.UserBookStatusInProgress, ProgressPct: 70, LastActivityAt: rfT0},
			positions: []database.UserPosition{rfPos("a", 3500, rfT0)}}
		now := userStateSide{state: &database.UserBookState{Status: database.UserBookStatusInProgress, ProgressPct: 80},
			positions: []database.UserPosition{rfPos("a", 4000, time.Time{})}}
		st, got := combineOnRestore("u", "bk", snap, now)
		require.Equal(t, []database.UserPosition{rfPos("a", 4000, time.Time{})}, got, "4000 is not rewound to 3500")
		require.Equal(t, 80, st.ProgressPct, "the farther-ahead side's state stands")
	})
	t.Run("legacy snapshot row ahead of a dated current row", func(t *testing.T) {
		snap := userStateSide{state: &database.UserBookState{Status: database.UserBookStatusInProgress, ProgressPct: 80},
			positions: []database.UserPosition{rfPos("a", 4000, time.Time{})}}
		now := userStateSide{state: &database.UserBookState{Status: database.UserBookStatusInProgress, ProgressPct: 70, LastActivityAt: rfT1},
			positions: []database.UserPosition{rfPos("a", 3500, rfT1)}}
		st, got := combineOnRestore("u", "bk", snap, now)
		require.Equal(t, []database.UserPosition{rfPos("a", 4000, time.Time{})}, got)
		require.Equal(t, 80, st.ProgressPct)
	})
	t.Run("both dated: newer still wins even when behind", func(t *testing.T) {
		snap := userStateSide{positions: []database.UserPosition{rfPos("a", 4000, rfT0)}}
		now := userStateSide{positions: []database.UserPosition{rfPos("a", 30, rfT1)}}
		_, got := combineOnRestore("u", "bk", snap, now)
		require.Equal(t, []database.UserPosition{rfPos("a", 30, rfT1)}, got)
	})
}

// #3770 review S2: a survivor the user listened to during the carry window
// is reconciled, not left as is: what the follow carried onto it goes back
// to the absorbed book only, and the user's own listening there since stays,
// so the listened time is on exactly one book with no double count.
func TestRestoreFollowedProgress_TouchedSurvivorIsReconciled(t *testing.T) {
	s := setupTestStore(t).(*database.PebbleStore)
	keep, dup := seedSyncBooks(t, s)
	u := seedSyncUser(t, s)
	ids := database.AsSyncIdentityStore(s)
	for _, id := range []string{keep, dup} {
		_, err := ids.MintOrGetSyncID(id)
		require.NoError(t, err)
	}
	// keep: its own older listen; dup: a newer, longer one that wins the merge.
	require.NoError(t, s.SetUserBookState(&database.UserBookState{UserID: u.ID, BookID: keep, Status: database.UserBookStatusInProgress, ProgressPct: 10, TotalListenedSeconds: 500, LastActivityAt: rfT0}))
	require.NoError(t, s.SetUserPositionAt(u.ID, keep, "a", 100, rfT0))
	require.NoError(t, s.SetUserBookState(&database.UserBookState{UserID: u.ID, BookID: dup, Status: database.UserBookStatusInProgress, ProgressPct: 40, TotalListenedSeconds: 3000, LastActivityAt: rfT1}))
	require.NoError(t, s.SetUserPositionAt(u.ID, dup, "a", 1200, rfT1))

	progress, redirected, err := FollowAbsorbedJournaled(s, keep, dup, nil, nil)
	require.NoError(t, err)
	st, err := s.GetUserBookState(u.ID, keep)
	require.NoError(t, err)
	require.Equal(t, 3000.0, st.TotalListenedSeconds, "precondition: the follow carried dup's listened time onto keep")

	// The user listens to keep during the carry window: a new segment row
	// and 100 more seconds.
	require.NoError(t, s.SetUserPosition(u.ID, keep, "b", 50))
	st.TotalListenedSeconds += 100
	st.LastActivityAt = time.Now()
	require.NoError(t, s.SetUserBookState(st))

	warnings, err := RestoreFollowedProgress(s, keep, dup, redirected, progress)
	require.NoError(t, err)
	require.Len(t, warnings, 1)
	require.True(t, strings.Contains(warnings[0], "taken back"), warnings[0])

	dst, err := s.GetUserBookState(u.ID, dup)
	require.NoError(t, err)
	kst, err := s.GetUserBookState(u.ID, keep)
	require.NoError(t, err)
	require.Equal(t, 3000.0, dst.TotalListenedSeconds, "dup has its own listened time back")
	require.Equal(t, 600.0, kst.TotalListenedSeconds, "keep: its own 500 plus the 100 listened since, not the carried 3000")
	require.Equal(t, 3600.0, dst.TotalListenedSeconds+kst.TotalListenedSeconds, "no double count across the two books")
	require.Equal(t, 10, kst.ProgressPct, "the status group the follow wrote goes back")
	require.Equal(t, "b", kst.LastSegmentID)

	kpos, err := s.ListUserPositionsForBook(u.ID, keep)
	require.NoError(t, err)
	got := map[string]float64{}
	for _, p := range kpos {
		got[p.SegmentID] = p.PositionSeconds
	}
	require.Equal(t, map[string]float64{"a": 100, "b": 50}, got, "the carried row goes back to keep's own; the user's new row stays")
	dpos, err := s.ListUserPositionsForBook(u.ID, dup)
	require.NoError(t, err)
	require.Len(t, dpos, 1)
	require.Equal(t, 1200.0, dpos[0].PositionSeconds)
}
