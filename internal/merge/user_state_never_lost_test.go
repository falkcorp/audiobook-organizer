// file: internal/merge/user_state_never_lost_test.go
// version: 1.0.0
// guid: 5e9a3c17-2b8d-4f60-a4e7-1c6d8b0f3a92
// last-edited: 2026-09-26

package merge

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/syncapi/progress"
	"github.com/stretchr/testify/require"
)

// --- conflict rule, case by case (pure) ---

func st(status string, pct int, last time.Time) *database.UserBookState {
	return &database.UserBookState{UserID: "u", BookID: "x", Status: status, ProgressPct: pct, LastActivityAt: last}
}

func pos(seg string, at time.Time) []database.UserPosition {
	return []database.UserPosition{{UserID: "u", SegmentID: seg, PositionSeconds: 1, UpdatedAt: at}}
}

func TestPlanUserStateMerge_Rule(t *testing.T) {
	t0 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	older, newer := t0, t0.Add(time.Hour)
	fin := t0.Add(-time.Hour)

	t.Run("finished is sticky even when the other side is newer", func(t *testing.T) {
		l := st(database.UserBookStatusFinished, 100, older)
		l.FinishedAt = &fin
		w := st(database.UserBookStatusInProgress, 30, newer)
		p := planUserStateMerge("u", "W", userStateSide{l, pos("a", older)}, userStateSide{w, pos("b", newer)})
		require.Equal(t, database.UserBookStatusFinished, p.state.Status)
		require.Equal(t, &fin, p.state.FinishedAt)
		require.Equal(t, 100, p.state.ProgressPct)
		require.False(t, p.loserPositionsWin, "the newer (survivor) position still stands")
		require.True(t, p.loserFinished)
		require.Equal(t, newer, p.state.LastActivityAt, "last played = max")
	})
	t.Run("finished on the survivor survives a newer in-progress loser", func(t *testing.T) {
		w := st(database.UserBookStatusFinished, 100, older)
		l := st(database.UserBookStatusInProgress, 20, newer)
		p := planUserStateMerge("u", "W", userStateSide{l, pos("a", newer)}, userStateSide{w, pos("b", older)})
		require.Equal(t, database.UserBookStatusFinished, p.state.Status)
		require.True(t, p.loserPositionsWin, "newest position wins")
	})
	t.Run("newest wins the position over furthest", func(t *testing.T) {
		l := st(database.UserBookStatusInProgress, 90, older)
		w := st(database.UserBookStatusInProgress, 10, newer)
		p := planUserStateMerge("u", "W", userStateSide{l, pos("a", older)}, userStateSide{w, pos("b", newer)})
		require.Equal(t, 10, p.state.ProgressPct)
		require.False(t, p.loserPositionsWin)
	})
	t.Run("newer loser wins the position", func(t *testing.T) {
		l := st(database.UserBookStatusInProgress, 10, newer)
		w := st(database.UserBookStatusInProgress, 90, older)
		p := planUserStateMerge("u", "W", userStateSide{l, pos("a", newer)}, userStateSide{w, pos("b", older)})
		require.Equal(t, 10, p.state.ProgressPct)
		require.True(t, p.loserPositionsWin)
		require.Equal(t, "W", p.state.BookID)
	})
	t.Run("a tie goes to the survivor", func(t *testing.T) {
		l := st(database.UserBookStatusInProgress, 70, older)
		w := st(database.UserBookStatusInProgress, 40, older)
		p := planUserStateMerge("u", "W", userStateSide{l, nil}, userStateSide{w, nil})
		require.Equal(t, 40, p.state.ProgressPct)
		require.False(t, p.loserPositionsWin)
	})
	t.Run("hide is kept if either side has it", func(t *testing.T) {
		l := st(database.UserBookStatusInProgress, 10, older)
		l.HideFromContinueListening = true
		w := st(database.UserBookStatusInProgress, 20, newer)
		p := planUserStateMerge("u", "W", userStateSide{l, nil}, userStateSide{w, nil})
		require.True(t, p.state.HideFromContinueListening)
	})
	t.Run("reset tombstone: max time, union of positions", func(t *testing.T) {
		l := st(database.UserBookStatusInProgress, 10, older)
		lr := older
		l.ProgressResetAt, l.ProgressResetPositions = &lr, []float64{5, 6}
		w := st(database.UserBookStatusInProgress, 20, newer)
		wr := newer
		w.ProgressResetAt, w.ProgressResetPositions = &wr, []float64{6, 7}
		p := planUserStateMerge("u", "W", userStateSide{l, nil}, userStateSide{w, nil})
		require.Equal(t, wr, *p.state.ProgressResetAt)
		require.ElementsMatch(t, []float64{5, 6, 7}, p.state.ProgressResetPositions)
	})
	t.Run("an empty survivor takes the loser whole", func(t *testing.T) {
		l := st(database.UserBookStatusInProgress, 33, older)
		p := planUserStateMerge("u", "W", userStateSide{l, pos("a", older)}, userStateSide{nil, nil})
		require.Equal(t, 33, p.state.ProgressPct)
		require.True(t, p.loserPositionsWin)
	})
	t.Run("a drained row is not carryable", func(t *testing.T) {
		require.False(t, hasCarryableState(drainedUserState(*st(database.UserBookStatusFinished, 100, newer)), nil))
	})
}

// --- follow against a real store ---

// failingStore wraps a real store and fails chosen writes. Unwrap lets the
// capability lookups (sync identity, bookmarks) see the Pebble store.
type failingStore struct {
	database.Store
	failStateFor string // SetUserBookState on this book id fails
	failSetRaw   bool
}

func (f *failingStore) Unwrap() database.Store { return f.Store }

func (f *failingStore) SetUserBookState(s *database.UserBookState) error {
	if f.failStateFor != "" && s.BookID == f.failStateFor {
		return errors.New("injected state write failure")
	}
	return f.Store.SetUserBookState(s)
}

func (f *failingStore) SetRaw(key string, value []byte) error {
	if f.failSetRaw {
		return errors.New("injected raw write failure")
	}
	return f.Store.SetRaw(key, value)
}

func seedProgress(t *testing.T, store database.Store, userID, bookID string, pct int) {
	t.Helper()
	require.NoError(t, store.SetUserBookState(&database.UserBookState{
		UserID: userID, BookID: bookID, Status: database.UserBookStatusInProgress,
		ProgressPct: pct, LastActivityAt: time.Now(),
	}))
	require.NoError(t, store.SetUserPosition(userID, bookID, "seg", float64(pct)))
}

func pendingKeys(t *testing.T, store database.Store) []string {
	t.Helper()
	rows, err := store.ScanPrefix(PendingUserStateRepairPrefix)
	require.NoError(t, err)
	var keys []string
	for _, r := range rows {
		keys = append(keys, r.Key)
	}
	return keys
}

func TestFollowMerge_FailedMoveIsRecordedThenSwept(t *testing.T) {
	store := setupTestStore(t)
	user := seedSyncUser(t, store)
	winnerID, loserID := seedSyncBooks(t, store)
	seedProgress(t, store, user.ID, loserID, 42)

	fs := &failingStore{Store: store, failStateFor: winnerID}
	require.NoError(t, FollowMerge(fs, asFollower(fs), winnerID, []string{loserID}),
		"a failed move with a written record is not a merge failure")

	require.Equal(t, []string{pendingRepairKey(loserID, winnerID)}, pendingKeys(t, store), "the owed move is recorded")
	got, err := store.GetUserBookState(user.ID, loserID)
	require.NoError(t, err)
	require.Equal(t, 42, got.ProgressPct, "the loser's state is untouched, not lost")

	// The loser is retired, as the merge would; then the sweep runs.
	require.NoError(t, SoftDeleteBook(store, loserID))
	rep, err := RepairMergedUserState(context.Background(), store, UserStateRepairOptions{Apply: true, PendingOnly: true})
	require.NoError(t, err)
	require.Equal(t, 1, rep.PendingCompleted)
	require.Empty(t, pendingKeys(t, store))
	won, err := store.GetUserBookState(user.ID, winnerID)
	require.NoError(t, err)
	require.NotNil(t, won)
	require.Equal(t, 42, won.ProgressPct)
	assertLoserProgressDrained(t, store, user.ID, loserID)
}

func TestFollowMerge_FailedMoveWithoutRecordIsAnError(t *testing.T) {
	store := setupTestStore(t)
	user := seedSyncUser(t, store)
	winnerID, loserID := seedSyncBooks(t, store)
	seedProgress(t, store, user.ID, loserID, 42)

	fs := &failingStore{Store: store, failStateFor: winnerID, failSetRaw: true}
	err := FollowMerge(fs, asFollower(fs), winnerID, []string{loserID})
	require.Error(t, err, "nothing holds the owed move, so the caller must hear about it")

	// MergeBooks through the same store must not report success.
	ms := NewService(fs)
	_, err = ms.MergeBooks([]string{winnerID, loserID}, winnerID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no repair record")
}

func TestFollowMerge_ReplayIsNoOp(t *testing.T) {
	store := setupTestStore(t)
	user := seedSyncUser(t, store)
	winnerID, loserID := seedSyncBooks(t, store)
	seedProgress(t, store, user.ID, loserID, 42)
	f := asFollower(store)
	require.NoError(t, FollowMerge(store, f, winnerID, []string{loserID}))
	// The survivor moves on after the merge.
	seedProgress(t, store, user.ID, winnerID, 55)
	require.NoError(t, FollowMerge(store, f, winnerID, []string{loserID}))
	got, err := store.GetUserBookState(user.ID, winnerID)
	require.NoError(t, err)
	require.Equal(t, 55, got.ProgressPct, "a replay must not overwrite the survivor with the drained loser row")
	require.Empty(t, pendingKeys(t, store))
}

// --- bookmarks ---

func TestMergeBooks_BookmarksCopiedAndDeduplicated(t *testing.T) {
	store := setupTestStore(t)
	user := seedSyncUser(t, store)
	winnerID, loserID := seedSyncBooks(t, store)
	ids := database.AsSyncIdentityStore(store)
	bs := database.AsBookmarkStore(store)
	loserSync, err := ids.MintOrGetSyncID(loserID)
	require.NoError(t, err)
	winnerSync, err := ids.MintOrGetSyncID(winnerID)
	require.NoError(t, err)
	require.NoError(t, bs.CreateBookmark(progress.Bookmark{UserID: user.ID, ItemID: loserSync, TimeSec: 10, Title: "loser-10"}))
	require.NoError(t, bs.CreateBookmark(progress.Bookmark{UserID: user.ID, ItemID: loserSync, TimeSec: 20, Title: "loser-20"}))
	require.NoError(t, bs.CreateBookmark(progress.Bookmark{UserID: user.ID, ItemID: winnerSync, TimeSec: 20.0, Title: "winner-20"}))
	before, err := bs.ListBookmarks(user.ID, loserSync)
	require.NoError(t, err)

	_, err = NewService(store).MergeBooks([]string{winnerID, loserID}, winnerID)
	require.NoError(t, err)

	got, err := bs.ListBookmarks(user.ID, winnerSync)
	require.NoError(t, err)
	byTime := map[string]progress.Bookmark{}
	for _, b := range got {
		byTime[progress.CanonicalTimeKey(b.TimeSec)] = b
	}
	require.Len(t, byTime, 2, "one bookmark per instant")
	require.Equal(t, "winner-20", byTime[progress.CanonicalTimeKey(20)].Title, "an existing bookmark at that time wins")
	ten := byTime[progress.CanonicalTimeKey(10)]
	require.Equal(t, "loser-10", ten.Title)
	for _, b := range before {
		if b.TimeSec == 10 {
			require.Equal(t, b.CreatedAt, ten.CreatedAt, "a copy keeps the original CreatedAt")
		}
	}
	left, err := bs.ListBookmarks(user.ID, loserSync)
	require.NoError(t, err)
	require.Len(t, left, 2, "copied, not moved: undo gives the restored book its own bookmarks back")
}

// --- repair op core ---

// strandState leaves state on a soft-deleted loser whose sync id already
// redirects to the winner: the shape a pre-fix best-effort follow left.
func strandState(t *testing.T, store database.Store) (userID, winnerID, loserID string) {
	t.Helper()
	user := seedSyncUser(t, store)
	winnerID, loserID = seedSyncBooks(t, store)
	ids := database.AsSyncIdentityStore(store)
	loserSync, err := ids.MintOrGetSyncID(loserID)
	require.NoError(t, err)
	require.NoError(t, ids.RecordSyncMerge(loserID, winnerID))
	require.NoError(t, SoftDeleteBook(store, loserID))
	seedProgress(t, store, user.ID, loserID, 64)
	require.NoError(t, database.AsBookmarkStore(store).CreateBookmark(progress.Bookmark{UserID: user.ID, ItemID: loserSync, TimeSec: 7, Title: "stranded"}))
	return user.ID, winnerID, loserID
}

func TestRepairMergedUserState_PreviewWritesNothing(t *testing.T) {
	store := setupTestStore(t)
	userID, winnerID, loserID := strandState(t, store)
	winnerSync, _, err := database.AsSyncIdentityStore(store).GetSyncIDForBook(winnerID)
	require.NoError(t, err)

	rep, err := RepairMergedUserState(context.Background(), store, UserStateRepairOptions{})
	require.NoError(t, err)
	require.Equal(t, 1, rep.StrandedBooks)
	require.Equal(t, 1, rep.StateRows)
	require.Equal(t, 1, rep.PositionRows)
	require.Equal(t, 1, rep.BookmarksOwed)
	require.Zero(t, rep.StateMoved)
	require.Zero(t, rep.BookmarksCopied)

	w, err := store.GetUserBookState(userID, winnerID)
	require.NoError(t, err)
	require.Nil(t, w, "preview must not write the survivor")
	l, err := store.GetUserBookState(userID, loserID)
	require.NoError(t, err)
	require.Equal(t, 64, l.ProgressPct, "preview must not drain the loser")
	marks, err := database.AsBookmarkStore(store).ListBookmarks(userID, winnerSync)
	require.NoError(t, err)
	require.Empty(t, marks)
}

func TestRepairMergedUserState_ApplyMovesState(t *testing.T) {
	store := setupTestStore(t)
	userID, winnerID, loserID := strandState(t, store)
	winnerSync, _, err := database.AsSyncIdentityStore(store).GetSyncIDForBook(winnerID)
	require.NoError(t, err)

	rep, err := RepairMergedUserState(context.Background(), store, UserStateRepairOptions{Apply: true})
	require.NoError(t, err)
	require.Equal(t, 1, rep.StateMoved)
	require.Equal(t, 1, rep.BookmarksCopied)
	require.Zero(t, rep.Errors)

	w, err := store.GetUserBookState(userID, winnerID)
	require.NoError(t, err)
	require.NotNil(t, w)
	require.Equal(t, 64, w.ProgressPct)
	assertLoserProgressDrained(t, store, userID, loserID)
	marks, err := database.AsBookmarkStore(store).ListBookmarks(userID, winnerSync)
	require.NoError(t, err)
	require.Len(t, marks, 1)

	again, err := RepairMergedUserState(context.Background(), store, UserStateRepairOptions{})
	require.NoError(t, err)
	require.Zero(t, again.StateRows, "an applied repair leaves nothing to find")
	require.Zero(t, again.BookmarksOwed)
}

// --- survivor preference ---

func prefBooks() []*database.Book {
	organized := "organized"
	return []*database.Book{
		{ID: "a", Format: "m4b", FilePath: "/lib/a.m4b", LibraryState: &organized},
		{ID: "b", Format: "mp3", FilePath: "/lib/b.mp3", LibraryState: &organized},
	}
}

func TestPreferUserStateSurvivor(t *testing.T) {
	files := map[string][]database.BookFile{}
	t.Run("state on the would-be loser flips the survivor", func(t *testing.T) {
		books := prefBooks()
		elected := ElectPrimary(books, files)
		require.Equal(t, 0, elected, "m4b wins without state")
		require.Equal(t, 1, PreferUserStateSurvivor(books, files, map[string]bool{"b": true}, elected))
	})
	t.Run("several stateful candidates keep the current rule", func(t *testing.T) {
		books := prefBooks()
		require.Equal(t, 0, PreferUserStateSurvivor(books, files, map[string]bool{"a": true, "b": true}, 0))
	})
	t.Run("hard rule: an iTunes ghost never beats a library book", func(t *testing.T) {
		books := prefBooks()
		books[1].FilePath = "/Music/iTunes/iTunes Media/Audiobooks/b.mp3"
		require.Equal(t, 0, PreferUserStateSurvivor(books, files, map[string]bool{"b": true}, 0))
	})
	t.Run("hard rule: organized beats not organized", func(t *testing.T) {
		books := prefBooks()
		imported := "imported"
		books[1].LibraryState = &imported
		require.Equal(t, 0, PreferUserStateSurvivor(books, files, map[string]bool{"b": true}, 0))
	})
	t.Run("hard rule: an audio route beats none", func(t *testing.T) {
		books := prefBooks()
		books[1].FilePath = ""
		require.Equal(t, 0, PreferUserStateSurvivor(books, files, map[string]bool{"b": true}, 0))
	})
}

func TestMergeBooks_UserStateSurvivor(t *testing.T) {
	t.Run("automatic election keeps the book the user listens to", func(t *testing.T) {
		store := setupTestStore(t)
		user := seedSyncUser(t, store)
		winnerID, loserID := seedSyncBooks(t, store) // m4b vs mp3: m4b elected without state
		seedProgress(t, store, user.ID, loserID, 30)
		res, err := NewService(store).MergeBooks([]string{winnerID, loserID}, "")
		require.NoError(t, err)
		require.Equal(t, loserID, res.PrimaryID)
		require.Equal(t, winnerID, res.ElectedWithoutUserState, "the flip is visible in the output")
	})
	t.Run("an explicit keep id is respected", func(t *testing.T) {
		store := setupTestStore(t)
		user := seedSyncUser(t, store)
		winnerID, loserID := seedSyncBooks(t, store)
		seedProgress(t, store, user.ID, loserID, 30)
		res, err := NewService(store).MergeBooks([]string{winnerID, loserID}, winnerID)
		require.NoError(t, err)
		require.Equal(t, winnerID, res.PrimaryID)
		require.Empty(t, res.ElectedWithoutUserState)
		got, err := store.GetUserBookState(user.ID, winnerID)
		require.NoError(t, err)
		require.Equal(t, 30, got.ProgressPct, "the state move carries it instead")
	})
}
