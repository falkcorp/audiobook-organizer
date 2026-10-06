// file: internal/merge/progress_safety_followups_test.go
// version: 1.0.0
// guid: 9e82a272-8905-49f4-b073-a818dfc67963
// last-edited: 2026-10-05

package merge

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/syncapi/progress"
)

// Review follow-ups to #3771 / #3772 (progress safety).

// C: combining two copies keeps the LARGER listened time, whichever side
// wins the position (owner decision 2026-10-05). The reviewer's probe: a
// survivor with 5000s listened must not drop to the loser's fresher 200s.
func TestPlanUserStateMerge_ListenedTimeIsTheLarger(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	older, newer := t0, t0.Add(time.Hour)

	t.Run("newer loser with less listened time does not lower the survivor", func(t *testing.T) {
		w := st(database.UserBookStatusInProgress, 60, older)
		w.TotalListenedSeconds = 5000
		l := st(database.UserBookStatusInProgress, 5, newer)
		l.TotalListenedSeconds = 200
		p := planUserStateMerge("u", "W", userStateSide{l, pos("a", newer)}, userStateSide{w, pos("b", older)})
		require.True(t, p.loserPositionsWin, "the newer loser still wins the position")
		require.Equal(t, 5, p.state.ProgressPct, "position-linked fields come from the newer side")
		require.EqualValues(t, 5000, p.state.TotalListenedSeconds, "listened time is the larger, not the newer side's")
	})
	t.Run("older loser with more listened time raises the survivor", func(t *testing.T) {
		w := st(database.UserBookStatusInProgress, 10, newer)
		w.TotalListenedSeconds = 300
		l := st(database.UserBookStatusInProgress, 70, older)
		l.TotalListenedSeconds = 7000
		p := planUserStateMerge("u", "W", userStateSide{l, pos("a", older)}, userStateSide{w, pos("b", newer)})
		require.False(t, p.loserPositionsWin)
		require.EqualValues(t, 7000, p.state.TotalListenedSeconds)
	})
	t.Run("never summed", func(t *testing.T) {
		w := st(database.UserBookStatusInProgress, 10, newer)
		w.TotalListenedSeconds = 1000
		l := st(database.UserBookStatusInProgress, 10, older)
		l.TotalListenedSeconds = 1000
		p := planUserStateMerge("u", "W", userStateSide{l, pos("a", older)}, userStateSide{w, pos("b", newer)})
		require.EqualValues(t, 1000, p.state.TotalListenedSeconds)
	})
}

// D2: an undated position decides only positions. Which side's status
// stands is decided by time, so a newer deliberate mark-unfinished beats a
// finished snapshot even when the snapshot holds a farther-ahead undated row.
func TestCombineOnRestore_UndatedRowDoesNotDecideStatus(t *testing.T) {
	snap := userStateSide{
		state:     &database.UserBookState{Status: database.UserBookStatusFinished, ProgressPct: 100, LastActivityAt: rfT0},
		positions: []database.UserPosition{rfPos("a", 9000, time.Time{})},
	}
	now := userStateSide{
		state:     &database.UserBookState{Status: database.UserBookStatusInProgress, StatusManual: true, ProgressPct: 20, LastActivityAt: rfT1},
		positions: []database.UserPosition{rfPos("a", 600, rfT1)},
	}
	st, got := combineOnRestore("u", "bk", snap, now)
	require.Equal(t, database.UserBookStatusInProgress, st.Status, "the newer deliberate mark-unfinished stands over the sticky finished")
	require.True(t, st.StatusManual)
	require.Equal(t, 20, st.ProgressPct)
	require.Equal(t, []database.UserPosition{rfPos("a", 9000, time.Time{})}, got, "the undated farther-ahead row still wins its segment")
}

// D2: positions are compared only within one segment; an undated row far
// into segment 1 does not outrank a newer listen in segment 3 for status.
func TestCombineOnRestore_FurthestIsPerSegment(t *testing.T) {
	snap := userStateSide{
		state:     &database.UserBookState{Status: database.UserBookStatusInProgress, ProgressPct: 30, LastActivityAt: rfT0},
		positions: []database.UserPosition{rfPos("1", 4000, time.Time{})},
	}
	now := userStateSide{
		state:     &database.UserBookState{Status: database.UserBookStatusInProgress, ProgressPct: 90, LastActivityAt: rfT1},
		positions: []database.UserPosition{rfPos("3", 100, rfT1)},
	}
	st, got := combineOnRestore("u", "bk", snap, now)
	require.Equal(t, 90, st.ProgressPct, "the newer side's state stands")
	require.ElementsMatch(t, []database.UserPosition{rfPos("1", 4000, time.Time{}), rfPos("3", 100, rfT1)}, got, "both segments are kept")
	require.Equal(t, "3", st.LastSegmentID)
}

// positionWriteFaultStore fails every per-row position write after the
// first, so a clear-then-write sequence stops half way.
type positionWriteFaultStore struct {
	*database.PebbleStore
	mu     sync.Mutex
	writes int
}

func (s *positionWriteFaultStore) SetUserPositionAt(userID, bookID, seg string, sec float64, at time.Time) error {
	s.mu.Lock()
	s.writes++
	n := s.writes
	s.mu.Unlock()
	if n > 1 {
		return errors.New("injected position write failure")
	}
	return s.PebbleStore.SetUserPositionAt(userID, bookID, seg, sec, at)
}

// D1: writeProgress replaces the position rows in one atomic write. With
// the old ClearUserPositions-then-write-each-row sequence a failing second
// row write left the book with its old rows gone and only one new row.
func TestWriteProgress_ReplacesPositionsAtomically(t *testing.T) {
	s := setupTestStore(t).(*database.PebbleStore)
	_, book := seedSyncBooks(t, s)
	u := seedSyncUser(t, s)
	require.NoError(t, s.SetUserPositionAt(u.ID, book, "old", 77, rfT0))
	fs := &positionWriteFaultStore{PebbleStore: s}

	want := []database.UserPosition{rfPos("a", 100, rfT0), rfPos("b", 200, rfT1), rfPos("c", 300, time.Time{})}
	require.NoError(t, writeProgress(fs, u.ID, book, nil, want, nil))
	got, err := s.ListUserPositionsForBook(u.ID, book)
	require.NoError(t, err)
	require.Len(t, got, 3)
	by := map[string]database.UserPosition{}
	for _, p := range got {
		by[p.SegmentID] = p
	}
	require.NotContains(t, by, "old")
	require.Equal(t, 200.0, by["b"].PositionSeconds)
	require.True(t, by["b"].UpdatedAt.Equal(rfT1), "UpdatedAt kept")
	require.True(t, by["c"].UpdatedAt.IsZero(), "an undated row stays undated (E)")

	// writePositionsDiff dropping a segment goes through the same replace.
	require.NoError(t, writePositionsDiff(fs, u.ID, book, got, []database.UserPosition{rfPos("a", 150, rfT2), rfPos("d", 9, rfT2)}))
	got, err = s.ListUserPositionsForBook(u.ID, book)
	require.NoError(t, err)
	require.ElementsMatch(t, []database.UserPosition{
		{UserID: u.ID, BookID: book, SegmentID: "a", PositionSeconds: 150, UpdatedAt: rfT2},
		{UserID: u.ID, BookID: book, SegmentID: "d", PositionSeconds: 9, UpdatedAt: rfT2},
	}, normalizeUTC(got))
}

func normalizeUTC(ps []database.UserPosition) []database.UserPosition {
	out := make([]database.UserPosition, len(ps))
	for i, p := range ps {
		p.UpdatedAt = p.UpdatedAt.UTC()
		out[i] = p
	}
	return out
}

// touchedSurvivorSetup is TestRestoreFollowedProgress_TouchedSurvivorIsReconciled's
// scenario up to the put-back: keep 500s, dup 3000s, the follow carried
// dup onto keep, and the user listened 100s more on keep since.
func touchedSurvivorSetup(t *testing.T) (s *database.PebbleStore, u *database.User, keep, dup string, progress []CombineUserProgress, redirected bool) {
	t.Helper()
	s = setupTestStore(t).(*database.PebbleStore)
	keep, dup = seedSyncBooks(t, s)
	u = seedSyncUser(t, s)
	ids := database.AsSyncIdentityStore(s)
	for _, id := range []string{keep, dup} {
		_, err := ids.MintOrGetSyncID(id)
		require.NoError(t, err)
	}
	require.NoError(t, s.SetUserBookState(&database.UserBookState{UserID: u.ID, BookID: keep, Status: database.UserBookStatusInProgress, ProgressPct: 10, TotalListenedSeconds: 500, LastActivityAt: rfT0}))
	require.NoError(t, s.SetUserPositionAt(u.ID, keep, "a", 100, rfT0))
	require.NoError(t, s.SetUserBookState(&database.UserBookState{UserID: u.ID, BookID: dup, Status: database.UserBookStatusInProgress, ProgressPct: 40, TotalListenedSeconds: 3000, LastActivityAt: rfT1}))
	require.NoError(t, s.SetUserPositionAt(u.ID, dup, "a", 1200, rfT1))
	var err error
	progress, redirected, err = FollowAbsorbedJournaled(s, keep, dup, nil, nil)
	require.NoError(t, err)
	st, err := s.GetUserBookState(u.ID, keep)
	require.NoError(t, err)
	require.NoError(t, s.SetUserPosition(u.ID, keep, "b", 50))
	st.TotalListenedSeconds += 100
	st.LastActivityAt = time.Now()
	require.NoError(t, s.SetUserBookState(st))
	return s, u, keep, dup, progress, redirected
}

func reconcileMarkers(t *testing.T, s *database.PebbleStore) []string {
	t.Helper()
	rows, err := s.ScanPrefix(survivorReconcilePrefix)
	require.NoError(t, err)
	var keys []string
	for _, r := range rows {
		keys = append(keys, r.Key)
	}
	return keys
}

// clientWriteDuringReconcile lands a client write (50s more listened) on
// the survivor right when the reconcile re-reads it before its write.
type clientWriteDuringReconcile struct {
	*database.PebbleStore
	t        *testing.T
	survivor string
	mu       sync.Mutex
	reads    int
}

func (s *clientWriteDuringReconcile) GetUserBookState(userID, bookID string) (*database.UserBookState, error) {
	if bookID == s.survivor {
		s.mu.Lock()
		s.reads++
		n := s.reads
		s.mu.Unlock()
		if n == 2 { // RestoreFollowedProgress read it (1); this is the re-read
			cur, err := s.PebbleStore.GetUserBookState(userID, bookID)
			require.NoError(s.t, err)
			cur.TotalListenedSeconds += 50
			require.NoError(s.t, s.PebbleStore.SetUserBookState(cur))
		}
	}
	return s.PebbleStore.GetUserBookState(userID, bookID)
}

// D3: a client write landing between the reconcile's read and its write is
// not overwritten by the stale reconcile: it is redone from fresh reads, so
// the 50s listened in that window are kept.
func TestReconcileTouchedSurvivor_RedoesOnAWriteMeanwhile(t *testing.T) {
	s, u, keep, dup, progress, redirected := touchedSurvivorSetup(t)
	cw := &clientWriteDuringReconcile{PebbleStore: s, t: t, survivor: keep}
	_, err := RestoreFollowedProgress(cw, keep, dup, redirected, progress)
	require.NoError(t, err)
	kst, err := s.GetUserBookState(u.ID, keep)
	require.NoError(t, err)
	require.Equal(t, 650.0, kst.TotalListenedSeconds, "keep's own 500 plus 100 and the 50 written during the reconcile")
}

// failSurvivorStateOnce fails the first SetUserBookState onto one book.
type failSurvivorStateOnce struct {
	*database.PebbleStore
	book   string
	mu     sync.Mutex
	failed bool
}

func (s *failSurvivorStateOnce) SetUserBookState(st *database.UserBookState) error {
	s.mu.Lock()
	fail := !s.failed && st.BookID == s.book
	if fail {
		s.failed = true
	}
	s.mu.Unlock()
	if fail {
		return errors.New("injected survivor state write failure")
	}
	return s.PebbleStore.SetUserBookState(st)
}

// D3: a reconcile whose state write fails drops the marker it wrote, and
// the retry still takes the carried seconds back exactly once.
func TestReconcileTouchedSurvivor_FailedWriteDropsMarker(t *testing.T) {
	s, u, keep, dup, progress, redirected := touchedSurvivorSetup(t)
	fs := &failSurvivorStateOnce{PebbleStore: s, book: keep}
	_, err := RestoreFollowedProgress(fs, keep, dup, redirected, progress)
	require.Error(t, err)
	require.Empty(t, reconcileMarkers(t, s), "the failed write's marker is dropped")

	_, err = RestoreFollowedProgress(fs, keep, dup, redirected, progress)
	require.NoError(t, err)
	kst, err := s.GetUserBookState(u.ID, keep)
	require.NoError(t, err)
	require.Equal(t, 600.0, kst.TotalListenedSeconds)
}

// D4: the markers of a put-back are dropped once nothing can re-run it.
func TestDropSurvivorReconcileMarkers(t *testing.T) {
	s, _, keep, dup, progress, redirected := touchedSurvivorSetup(t)
	_, err := RestoreFollowedProgress(s, keep, dup, redirected, progress)
	require.NoError(t, err)
	keys := reconcileMarkers(t, s)
	require.Len(t, keys, 1)
	require.True(t, strings.HasPrefix(keys[0], survivorReconcilePrefix+keep+":"+dup+":"))
	dropSurvivorReconcileMarkers(s, keep, dup, progress)
	require.Empty(t, reconcileMarkers(t, s))
}

// bookmarkOnly seeds two books and one user whose only state is a bookmark
// on doomed (no state row, no position).
func bookmarkOnly(t *testing.T) (s *database.PebbleStore, u *database.User, keep, doomed, doomedSync string) {
	t.Helper()
	s = setupTestStore(t).(*database.PebbleStore)
	keep, doomed = seedSyncBooks(t, s)
	u = seedSyncUser(t, s)
	var err error
	doomedSync, err = database.AsSyncIdentityStore(s).MintOrGetSyncID(doomed)
	require.NoError(t, err)
	require.NoError(t, s.CreateBookmark(progress.Bookmark{UserID: u.ID, ItemID: doomedSync, TimeSec: 42, Title: "the good part"}))
	return s, u, keep, doomed, doomedSync
}

// B: a bookmark is listening state. A book whose only state is a bookmark
// counts as carryable, so no automatic hard delete drops it.
func TestUserStateProbe_BookmarkOnlyIsCarryable(t *testing.T) {
	s, _, _, doomed, _ := bookmarkOnly(t)
	has, err := BookHasCarryableUserState(s, doomed)
	require.NoError(t, err)
	require.True(t, has)
	st, err := s.GetUserBookState("nobody", doomed)
	require.NoError(t, err)
	require.Nil(t, st)
}

// B: the carry moves a bookmark-only book's bookmark to the kept book (a
// copy under its sync id, plus the redirect), after which the doomed book
// no longer reads as holding state, so the carry completes instead of
// putting itself back.
func TestCarryStateBeforeHardDelete_MovesBookmarkOnlyState(t *testing.T) {
	s, u, keep, doomed, _ := bookmarkOnly(t)
	require.NoError(t, CarryStateBeforeHardDelete(s, keep, doomed))
	keepSync, ok, err := database.AsSyncIdentityStore(s).GetSyncIDForBook(keep)
	require.NoError(t, err)
	require.True(t, ok)
	marks, err := s.ListBookmarks(u.ID, keepSync)
	require.NoError(t, err)
	require.Len(t, marks, 1)
	require.Equal(t, 42.0, marks[0].TimeSec)
	has, err := BookHasCarryableUserState(s, doomed)
	require.NoError(t, err)
	require.False(t, has, "the copied bookmark is no longer owed")
}

// B: a redirected book whose bookmark the survivor lacks (the copy never
// happened) still holds that bookmark as state.
func TestUserStateProbe_RedirectedButUncopiedBookmarkIsOwed(t *testing.T) {
	s, _, keep, doomed, _ := bookmarkOnly(t)
	ids := database.AsSyncIdentityStore(s)
	_, err := ids.MintOrGetSyncID(keep)
	require.NoError(t, err)
	require.NoError(t, ids.RecordSyncMerge(doomed, keep))
	has, err := BookHasCarryableUserState(s, doomed)
	require.NoError(t, err)
	require.True(t, has)
}

// B: discard clears a bookmark-only book's bookmark and counts its user.
func TestDiscardUserState_BookmarkOnly(t *testing.T) {
	s, u, _, doomed, doomedSync := bookmarkOnly(t)
	deleted := false
	res, err := DiscardUserStateThenHardDelete(s, doomed, nil, func() error { deleted = true; return nil })
	require.NoError(t, err)
	require.True(t, deleted)
	require.Equal(t, 1, res.Users, "the bookmark-only user is counted")
	require.Equal(t, 1, res.Bookmarks)
	marks, err := s.ListBookmarks(u.ID, doomedSync)
	require.NoError(t, err)
	require.Empty(t, marks)
}
