// file: internal/maintenance/jobs/backfill_itunes_positions_test.go
// version: 1.0.0
// guid: 5577372a-b3ed-4283-9217-345d654e2a57
// last-edited: 2026-07-30

// Package jobs_test — coverage for the backfill-itunes-positions maintenance
// job. The owner is migrating off iTunes/Apple Books and their existing
// listening positions must carry over, so the load-bearing assertions here are
// the negative ones: a nil bookmark must not become a zero position, and a
// newer position from a real device must never be rewound by stale iTunes data.
package jobs_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance/jobs"
	"github.com/falkcorp/audiobook-organizer/internal/syncapi/progress"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newPositionUser(t *testing.T, store *database.PebbleStore, username string) *database.User {
	t.Helper()
	u, err := store.CreateUser(username, username+"@example.test", "argon2id", "hash", []string{"admin"}, "active")
	require.NoError(t, err)
	return u
}

// seedBookmarkedBook creates a book whose track durations sum to trackSecs
// (spec §5b's recommended authoritative duration) and whose ITunesBookmark is
// bookmarkMs. A nil bookmarkMs means "no saved position".
func seedBookmarkedBook(t *testing.T, store *database.PebbleStore, title string, bookmarkMs *int64, trackSecs []int, lastPlayed *time.Time) *database.Book {
	t.Helper()
	book, err := store.CreateBook(&database.Book{
		Title:            title,
		FilePath:         "/library/" + title,
		ITunesBookmark:   bookmarkMs,
		ITunesLastPlayed: lastPlayed,
	})
	require.NoError(t, err)
	for i, secs := range trackSecs {
		require.NoError(t, store.CreateBookFile(&database.BookFile{
			BookID:   book.ID,
			FilePath: fmt.Sprintf("/library/%s/track-%02d.mp3", title, i),
			Duration: secs,
		}))
	}
	return book
}

func runPositionBackfill(t *testing.T, store database.Store, dryRun bool) *noopReporter {
	t.Helper()
	j, err := maintenance.Get("backfill-itunes-positions")
	require.NoError(t, err)
	rep := &noopReporter{}
	require.NoError(t, j.Run(context.Background(), store, rep, dryRun))
	return rep
}

func int64p(v int64) *int64 { return &v }

// wholeBookPosition returns the migrated whole-book row, or nil.
func wholeBookPosition(t *testing.T, store *database.PebbleStore, userID, bookID string) *database.UserPosition {
	t.Helper()
	rows, err := store.ListUserPositionsForBook(userID, bookID)
	require.NoError(t, err)
	for i := range rows {
		if rows[i].SegmentID == jobs.ITunesPositionWholeBookSegmentID {
			return &rows[i]
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Registration
// ---------------------------------------------------------------------------

func TestBackfillITunesPositionsJob_Registered(t *testing.T) {
	assertJobRegistered(t, "backfill-itunes-positions")
	j, err := maintenance.Get("backfill-itunes-positions")
	require.NoError(t, err)
	require.NotEmpty(t, j.Name())
	require.NotEmpty(t, j.Description())
	require.NotEmpty(t, j.Category())
	require.NotNil(t, j.DefaultParams())
	require.False(t, j.CanResume())
}

// ---------------------------------------------------------------------------
// Conversion + derivation
// ---------------------------------------------------------------------------

// ITunesBookmark is MILLISECONDS; ABS positions are float SECONDS.
func TestBackfillITunesPositions_ConvertsMillisecondsToFloatSeconds(t *testing.T) {
	store := newSyncPebbleStore(t)
	user := newPositionUser(t, store, "owner")
	t.Setenv(jobs.ITunesPositionBackfillUserIDEnv, user.ID)

	book := seedBookmarkedBook(t, store, "half-listened", int64p(1234567), []int{1000, 1000}, nil)

	runPositionBackfill(t, store, false)

	pos := wholeBookPosition(t, store, user.ID, book.ID)
	require.NotNil(t, pos, "no whole-book position row was written")
	require.InDelta(t, 1234.567, pos.PositionSeconds, 1e-9)
}

// progress must be a 0.0-1.0 fraction, never a percentage.
func TestITunesPositionProgressFraction_IsAFractionNotAPercentage(t *testing.T) {
	require.InDelta(t, 0.5, jobs.ITunesPositionProgressFraction(50, 100), 1e-9)
	require.InDelta(t, 0.0, jobs.ITunesPositionProgressFraction(0, 100), 1e-9)
	require.InDelta(t, 1.0, jobs.ITunesPositionProgressFraction(100, 100), 1e-9)
	// Clamped: a position past the (int-seconds) duration sum must not report
	// 1.04, which a strict client decoder would reject.
	require.InDelta(t, 1.0, jobs.ITunesPositionProgressFraction(104, 100), 1e-9)
	// Unknown duration cannot yield a fraction.
	require.InDelta(t, 0.0, jobs.ITunesPositionProgressFraction(50, 0), 1e-9)
}

// §5b: a fully-listened book must auto-mark finished despite duration skew.
// The Odyssey fixture's three legitimate durations disagree by ~52 ms
// (container 9975.480544 / last-chapter-end 9975.428 / track-sum 9975.431111);
// BookFile.Duration is whole seconds, so the skew against a millisecond
// bookmark is up to ~1 s per track — which is exactly why the tolerance is 2 s
// and not an epsilon.
func TestBackfillITunesPositions_FinishedWithinToleranceOfDuration(t *testing.T) {
	store := newSyncPebbleStore(t)
	user := newPositionUser(t, store, "owner")
	t.Setenv(jobs.ITunesPositionBackfillUserIDEnv, user.ID)

	tracks := []int{1663, 1663, 1663, 1663, 1663, 1660} // sums to 9975
	// 9973.5 s — 1.5 s short of the track sum, inside FinishedToleranceSec.
	finished := seedBookmarkedBook(t, store, "odyssey-finished", int64p(9973500), tracks, nil)
	// 9972.0 s — 3 s short, outside the tolerance.
	unfinished := seedBookmarkedBook(t, store, "odyssey-unfinished", int64p(9972000), tracks, nil)

	require.Equal(t, 2.0, progress.FinishedToleranceSec, "test assumes the spec §5b 2s tolerance")

	runPositionBackfill(t, store, false)

	st, err := store.GetUserBookState(user.ID, finished.ID)
	require.NoError(t, err)
	require.NotNil(t, st)
	require.Equal(t, database.UserBookStatusFinished, st.Status)
	require.Equal(t, 100, st.ProgressPct)

	st2, err := store.GetUserBookState(user.ID, unfinished.ID)
	require.NoError(t, err)
	require.NotNil(t, st2)
	require.Equal(t, database.UserBookStatusInProgress, st2.Status)
	require.Less(t, st2.ProgressPct, 100)
}

// lastUpdate must be a real integer ms epoch, taken from ITunesLastPlayed.
func TestBackfillITunesPositions_StampsMillisecondEpochLastUpdate(t *testing.T) {
	store := newSyncPebbleStore(t)
	user := newPositionUser(t, store, "owner")
	t.Setenv(jobs.ITunesPositionBackfillUserIDEnv, user.ID)

	lastPlayed := time.UnixMilli(1_700_000_123_456).UTC()
	book := seedBookmarkedBook(t, store, "stamped", int64p(600000), []int{1200}, &lastPlayed)

	runPositionBackfill(t, store, false)

	st, err := store.GetUserBookState(user.ID, book.ID)
	require.NoError(t, err)
	require.NotNil(t, st)
	require.Equal(t, int64(1_700_000_123_456), st.LastActivityAt.UnixMilli(),
		"LastActivityAt must carry the exact ms epoch the ABS DTO reports as lastUpdate")
}

// ---------------------------------------------------------------------------
// The negative guarantees
// ---------------------------------------------------------------------------

// A nil bookmark means "no saved position" — writing a zero would fabricate
// progress the user never had.
func TestBackfillITunesPositions_NilBookmarkWritesNothing(t *testing.T) {
	store := newSyncPebbleStore(t)
	user := newPositionUser(t, store, "owner")
	t.Setenv(jobs.ITunesPositionBackfillUserIDEnv, user.ID)

	book := seedBookmarkedBook(t, store, "never-opened", nil, []int{1200}, nil)

	runPositionBackfill(t, store, false)

	require.Nil(t, wholeBookPosition(t, store, user.ID, book.ID))
	st, err := store.GetUserBookState(user.ID, book.ID)
	require.NoError(t, err)
	require.Nil(t, st, "a book with no iTunes bookmark must get no user-book state at all")
}

// A zero (or negative) bookmark is the same "not started" signal.
func TestBackfillITunesPositions_ZeroBookmarkWritesNothing(t *testing.T) {
	store := newSyncPebbleStore(t)
	user := newPositionUser(t, store, "owner")
	t.Setenv(jobs.ITunesPositionBackfillUserIDEnv, user.ID)

	zero := seedBookmarkedBook(t, store, "zero-bookmark", int64p(0), []int{1200}, nil)
	neg := seedBookmarkedBook(t, store, "negative-bookmark", int64p(-5), []int{1200}, nil)

	runPositionBackfill(t, store, false)

	require.Nil(t, wholeBookPosition(t, store, user.ID, zero.ID))
	require.Nil(t, wholeBookPosition(t, store, user.ID, neg.ID))
}

// THE data-loss guard: a user who already listened further on a device must not
// be rewound by stale iTunes data.
func TestBackfillITunesPositions_NeverRewindsANewerPosition(t *testing.T) {
	store := newSyncPebbleStore(t)
	user := newPositionUser(t, store, "owner")
	t.Setenv(jobs.ITunesPositionBackfillUserIDEnv, user.ID)

	book := seedBookmarkedBook(t, store, "already-ahead", int64p(1_000_000), []int{6000}, nil) // iTunes at 1000s
	require.NoError(t, store.SetUserPosition(user.ID, book.ID, "device-1", 5000))              // device at 5000s
	require.NoError(t, store.SetUserBookState(&database.UserBookState{
		UserID: user.ID, BookID: book.ID,
		Status: database.UserBookStatusInProgress, ProgressPct: 83,
		LastActivityAt: time.Now().UTC(),
	}))

	runPositionBackfill(t, store, false)

	require.Nil(t, wholeBookPosition(t, store, user.ID, book.ID),
		"stale iTunes data must not write a whole-book row behind the device's position")

	latest, err := store.GetUserPosition(user.ID, book.ID)
	require.NoError(t, err)
	require.NotNil(t, latest)
	require.InDelta(t, 5000, latest.PositionSeconds, 1e-9)

	st, err := store.GetUserBookState(user.ID, book.ID)
	require.NoError(t, err)
	require.NotNil(t, st)
	require.Equal(t, 83, st.ProgressPct, "existing state must be left untouched")
}

// Forward-only still advances: iTunes further along than the stored position is
// a legitimate carry-over.
func TestBackfillITunesPositions_AdvancesWhenITunesIsFurther(t *testing.T) {
	store := newSyncPebbleStore(t)
	user := newPositionUser(t, store, "owner")
	t.Setenv(jobs.ITunesPositionBackfillUserIDEnv, user.ID)

	book := seedBookmarkedBook(t, store, "itunes-ahead", int64p(5_000_000), []int{6000}, nil) // iTunes at 5000s
	require.NoError(t, store.SetUserPosition(user.ID, book.ID, "device-1", 100))

	runPositionBackfill(t, store, false)

	pos := wholeBookPosition(t, store, user.ID, book.ID)
	require.NotNil(t, pos)
	require.InDelta(t, 5000, pos.PositionSeconds, 1e-9)
}

// A manual status override is a user decision; the migration must not stomp it.
func TestBackfillITunesPositions_RespectsManualStatus(t *testing.T) {
	store := newSyncPebbleStore(t)
	user := newPositionUser(t, store, "owner")
	t.Setenv(jobs.ITunesPositionBackfillUserIDEnv, user.ID)

	book := seedBookmarkedBook(t, store, "abandoned-on-purpose", int64p(600_000), []int{6000}, nil)
	require.NoError(t, store.SetUserBookState(&database.UserBookState{
		UserID: user.ID, BookID: book.ID,
		Status: database.UserBookStatusAbandoned, StatusManual: true,
	}))

	runPositionBackfill(t, store, false)

	st, err := store.GetUserBookState(user.ID, book.ID)
	require.NoError(t, err)
	require.NotNil(t, st)
	require.Equal(t, database.UserBookStatusAbandoned, st.Status)
	require.True(t, st.StatusManual)
	// The position itself still carries over.
	pos := wholeBookPosition(t, store, user.ID, book.ID)
	require.NotNil(t, pos)
	require.InDelta(t, 600, pos.PositionSeconds, 1e-9)
}

// ---------------------------------------------------------------------------
// Dry run + idempotency
// ---------------------------------------------------------------------------

func TestBackfillITunesPositions_DryRunWritesNothing(t *testing.T) {
	store := newSyncPebbleStore(t)
	user := newPositionUser(t, store, "owner")
	t.Setenv(jobs.ITunesPositionBackfillUserIDEnv, user.ID)

	book := seedBookmarkedBook(t, store, "dry", int64p(600_000), []int{6000}, nil)

	runPositionBackfill(t, store, true)

	require.Nil(t, wholeBookPosition(t, store, user.ID, book.ID))
	st, err := store.GetUserBookState(user.ID, book.ID)
	require.NoError(t, err)
	require.Nil(t, st)
}

// Re-running must be a true no-op. UserPosition.UpdatedAt is stamped
// time.Now() by the store on every write, so an unchanged UpdatedAt is proof
// the second run did not write.
func TestBackfillITunesPositions_RerunIsANoOp(t *testing.T) {
	store := newSyncPebbleStore(t)
	user := newPositionUser(t, store, "owner")
	t.Setenv(jobs.ITunesPositionBackfillUserIDEnv, user.ID)

	book := seedBookmarkedBook(t, store, "twice", int64p(600_000), []int{6000}, nil)

	runPositionBackfill(t, store, false)
	pos1 := wholeBookPosition(t, store, user.ID, book.ID)
	require.NotNil(t, pos1)
	st1, err := store.GetUserBookState(user.ID, book.ID)
	require.NoError(t, err)
	require.NotNil(t, st1)

	time.Sleep(2 * time.Millisecond) // so a second write would be visibly newer
	rep := runPositionBackfill(t, store, false)

	pos2 := wholeBookPosition(t, store, user.ID, book.ID)
	require.NotNil(t, pos2)
	require.Equal(t, *pos1, *pos2, "re-run rewrote the position row")
	st2, err := store.GetUserBookState(user.ID, book.ID)
	require.NoError(t, err)
	require.Equal(t, *st1, *st2, "re-run rewrote the user-book state")

	require.True(t, summaryContains(rep, "migrated=0"),
		"re-run should report zero migrations, got logs: %v", rep.logs)
}

// A state row lost or corrupted after the position was written must be repaired
// by a re-run rather than left permanently stale.
func TestBackfillITunesPositions_RepairsMissingStateOnRerun(t *testing.T) {
	store := newSyncPebbleStore(t)
	user := newPositionUser(t, store, "owner")
	t.Setenv(jobs.ITunesPositionBackfillUserIDEnv, user.ID)

	book := seedBookmarkedBook(t, store, "state-drift", int64p(600_000), []int{6000}, nil)
	runPositionBackfill(t, store, false)

	// Simulate a half-applied write: neutralize the state row, keep the position.
	require.NoError(t, store.SetUserBookState(&database.UserBookState{
		UserID: user.ID, BookID: book.ID, Status: "", ProgressPct: 0,
	}))

	runPositionBackfill(t, store, false)

	st, err := store.GetUserBookState(user.ID, book.ID)
	require.NoError(t, err)
	require.NotNil(t, st)
	require.Equal(t, database.UserBookStatusInProgress, st.Status)
	require.Equal(t, 10, st.ProgressPct)
}

// ---------------------------------------------------------------------------
// Target-user resolution
// ---------------------------------------------------------------------------

func TestResolveITunesPositionBackfillUser_EnvOverrideWins(t *testing.T) {
	store := newSyncPebbleStore(t)
	newPositionUser(t, store, "first")
	second := newPositionUser(t, store, "second")

	t.Setenv(jobs.ITunesPositionBackfillUserIDEnv, second.ID)
	got, err := jobs.ResolveITunesPositionBackfillUser(store)
	require.NoError(t, err)
	require.Equal(t, second.ID, got)
}

func TestResolveITunesPositionBackfillUser_RejectsUnknownOverride(t *testing.T) {
	store := newSyncPebbleStore(t)
	newPositionUser(t, store, "first")

	t.Setenv(jobs.ITunesPositionBackfillUserIDEnv, "no-such-user")
	_, err := jobs.ResolveITunesPositionBackfillUser(store)
	require.Error(t, err, "an unknown override must fail loudly, not silently fall back")
}

func TestResolveITunesPositionBackfillUser_SingleUser(t *testing.T) {
	store := newSyncPebbleStore(t)
	only := newPositionUser(t, store, "solo")

	t.Setenv(jobs.ITunesPositionBackfillUserIDEnv, "")
	got, err := jobs.ResolveITunesPositionBackfillUser(store)
	require.NoError(t, err)
	require.Equal(t, only.ID, got)
}

func TestResolveITunesPositionBackfillUser_MultiUserIsDeterministic(t *testing.T) {
	store := newSyncPebbleStore(t)
	a := newPositionUser(t, store, "alpha")
	b := newPositionUser(t, store, "bravo")

	t.Setenv(jobs.ITunesPositionBackfillUserIDEnv, "")
	first, err := jobs.ResolveITunesPositionBackfillUser(store)
	require.NoError(t, err)
	require.Contains(t, []string{a.ID, b.ID}, first)
	for i := 0; i < 3; i++ {
		again, err := jobs.ResolveITunesPositionBackfillUser(store)
		require.NoError(t, err)
		require.Equal(t, first, again, "ambiguous user choice must be stable across runs")
	}
}

func TestResolveITunesPositionBackfillUser_NoUsersIsAnError(t *testing.T) {
	store := newSyncPebbleStore(t)
	t.Setenv(jobs.ITunesPositionBackfillUserIDEnv, "")
	_, err := jobs.ResolveITunesPositionBackfillUser(store)
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// Unknown duration + reporting
// ---------------------------------------------------------------------------

// Without a duration, isFinished cannot be derived (§1.8.7's null-duration
// trap) — but the position itself is still the user's data and must carry over.
func TestBackfillITunesPositions_MigratesPositionWithoutDuration(t *testing.T) {
	store := newSyncPebbleStore(t)
	user := newPositionUser(t, store, "owner")
	t.Setenv(jobs.ITunesPositionBackfillUserIDEnv, user.ID)

	book := seedBookmarkedBook(t, store, "no-duration", int64p(600_000), nil, nil)

	rep := runPositionBackfill(t, store, false)

	pos := wholeBookPosition(t, store, user.ID, book.ID)
	require.NotNil(t, pos)
	require.InDelta(t, 600, pos.PositionSeconds, 1e-9)

	st, err := store.GetUserBookState(user.ID, book.ID)
	require.NoError(t, err)
	require.NotNil(t, st)
	require.Equal(t, database.UserBookStatusInProgress, st.Status)
	require.Equal(t, 0, st.ProgressPct, "no duration means no computable fraction")
	require.True(t, summaryContains(rep, "no_duration=1"), "logs: %v", rep.logs)
}

// The repo's status-reporting convention: exact counts, never "all done".
func TestBackfillITunesPositions_ReportsExactCounts(t *testing.T) {
	store := newSyncPebbleStore(t)
	user := newPositionUser(t, store, "owner")
	t.Setenv(jobs.ITunesPositionBackfillUserIDEnv, user.ID)

	seedBookmarkedBook(t, store, "m1", int64p(600_000), []int{6000}, nil)
	seedBookmarkedBook(t, store, "m2", int64p(700_000), []int{6000}, nil)
	seedBookmarkedBook(t, store, "skipped", nil, []int{6000}, nil)

	rep := runPositionBackfill(t, store, false)

	require.True(t, summaryContains(rep, "migrated=2"), "logs: %v", rep.logs)
	require.True(t, summaryContains(rep, "no_bookmark=1"), "logs: %v", rep.logs)
	require.True(t, summaryContains(rep, "failed=0"), "logs: %v", rep.logs)
}

// ---------------------------------------------------------------------------
// The single courtesy bookmark
// ---------------------------------------------------------------------------

// A migrated position also drops ONE named bookmark at the same offset so the
// owner can see and jump back to where Apple Books left them. Exactly one —
// the iTunes value is a single resume scalar, not a bookmark collection, and
// the ABS bookmarks feature is otherwise an independent system clients populate
// themselves.
func TestBackfillITunesPositions_DropsOneNamedBookmarkAtTheSameOffset(t *testing.T) {
	store := newSyncPebbleStore(t)
	user := newPositionUser(t, store, "owner")
	t.Setenv(jobs.ITunesPositionBackfillUserIDEnv, user.ID)

	book := seedBookmarkedBook(t, store, "with-marker", int64p(1_234_567), []int{6000}, nil)

	rep := runPositionBackfill(t, store, false)

	marks, err := store.ListBookmarks(user.ID, book.ID)
	require.NoError(t, err)
	require.Len(t, marks, 1, "exactly one bookmark, never a synthesized list")
	require.InDelta(t, 1234.567, marks[0].TimeSec, 1e-3)
	require.Equal(t, jobs.ITunesPositionBookmarkTitle, marks[0].Title)
	require.Greater(t, marks[0].CreatedAt, int64(0), "CreatedAt must be a real ms epoch")
	require.True(t, summaryContains(rep, "bookmarks=1"), "logs: %v", rep.logs)
}

func TestBackfillITunesPositions_NoBookmarkWhenNothingMigrated(t *testing.T) {
	store := newSyncPebbleStore(t)
	user := newPositionUser(t, store, "owner")
	t.Setenv(jobs.ITunesPositionBackfillUserIDEnv, user.ID)

	// nil bookmark, and a book whose stored position is already further along.
	skipped := seedBookmarkedBook(t, store, "no-marker", nil, []int{6000}, nil)
	ahead := seedBookmarkedBook(t, store, "already-ahead-marker", int64p(1_000), []int{6000}, nil)
	require.NoError(t, store.SetUserPosition(user.ID, ahead.ID, "device-1", 5000))

	runPositionBackfill(t, store, false)

	for _, id := range []string{skipped.ID, ahead.ID} {
		marks, err := store.ListBookmarks(user.ID, id)
		require.NoError(t, err)
		require.Empty(t, marks, "book %s should have no courtesy bookmark", id)
	}
}

func TestBackfillITunesPositions_DryRunCreatesNoBookmark(t *testing.T) {
	store := newSyncPebbleStore(t)
	user := newPositionUser(t, store, "owner")
	t.Setenv(jobs.ITunesPositionBackfillUserIDEnv, user.ID)

	book := seedBookmarkedBook(t, store, "dry-marker", int64p(600_000), []int{6000}, nil)

	runPositionBackfill(t, store, true)

	marks, err := store.ListBookmarks(user.ID, book.ID)
	require.NoError(t, err)
	require.Empty(t, marks)
}

func summaryContains(rep *noopReporter, want string) bool {
	for _, l := range rep.logs {
		if strings.Contains(l, want) {
			return true
		}
	}
	return false
}
