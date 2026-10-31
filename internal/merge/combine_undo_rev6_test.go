// file: internal/merge/combine_undo_rev6_test.go
// version: 1.3.1
// guid: 5e0dd46d-1455-44fc-94b7-899dc9c35055
// last-edited: 2026-10-30

package merge

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Round-6 review of #4064: the survivor put-back decides under the
// user-state stripe it writes under.

// stripedClientWrite runs write as a client that takes the (user, book)
// user-state stripe first, as the ABS handlers do. It is started from inside
// a store read of the restore under test and waits up to 200ms for the write
// to land. A restore that holds the stripe across that read keeps the client
// waiting until it is done, so the write lands on top of the restore; one
// that does not lets it land at once, between the read and the restore's
// write. The returned wait joins the client and returns its error; call it
// before asserting.
func stripedClientWrite(user, book string, write func() error) (wait func() error) {
	done := make(chan error, 1)
	go func() {
		unlock := database.LockUserBookState(user, book)
		defer unlock()
		done <- write()
	}()
	var err error
	landed := false
	select {
	case err = <-done:
		landed = true
	case <-time.After(200 * time.Millisecond):
	}
	return func() error {
		if !landed {
			err = <-done
		}
		return err
	}
}

// rev6Store injects failures into an undo.
type rev6Store struct {
	*database.PebbleStore
	// failLockDel fails that many lock-row deletes: the override's lock
	// restore, a step the undo takes after the progress put-back.
	failLockDel int
	// failLandedMarker fails the marker write that records a put-back's
	// landed state (recordPutBackLanded); landedMarkerFails counts them.
	failLandedMarker  bool
	landedMarkerFails int
}

func (s *rev6Store) DeleteMetadataFieldState(bookID, field string) error {
	if s.failLockDel > 0 {
		s.failLockDel--
		return errors.New("injected lock delete failure")
	}
	return s.PebbleStore.DeleteMetadataFieldState(bookID, field)
}

func (s *rev6Store) SetRaw(key string, value []byte) error {
	if s.failLandedMarker && strings.HasPrefix(key, survivorReconcilePrefix) && strings.Contains(string(value), `"post_updated_at"`) {
		s.landedMarkerFails++
		return errors.New("injected landed-marker write failure")
	}
	return s.PebbleStore.SetRaw(key, value)
}

// rev6Seed combines two books with a title override (so the undo has a
// lock-row step after the progress put-back). The user listened to the
// absorbed book only: 600 seconds and a position, carried onto the
// survivor, which had no state for them before.
func rev6Seed(t *testing.T) (store *database.PebbleStore, keep, gone, user string, res *CombineResult) {
	t.Helper()
	store = setupTestStore(t).(*database.PebbleStore)
	keep, gone = overrideBooks(t, store)
	u := seedSyncUser(t, store)
	at := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	require.NoError(t, store.SetUserPosition(u.ID, gone, "seg-g", 777))
	require.NoError(t, store.SetUserBookState(&database.UserBookState{UserID: u.ID, BookID: gone, Status: "in_progress", ProgressPct: 40, LastActivityAt: at, LastSegmentID: "seg-g", TotalListenedSeconds: 600}))
	res, err := NewService(store).CombineBooksWithOptions([]string{keep, gone}, keep, &CombineOverride{Title: "Override T"}, CombineOptions{})
	require.NoError(t, err)
	require.Equal(t, float64(600), rev5Listened(t, store, u.ID, keep), "the follow carried the listened time")
	return store, keep, gone, u.ID, res
}

// S1: the survivor had no state for the user before the combine, so the
// plain put-back leaves a drained row there, which never equals the (nil)
// before-snapshot. A later step of the undo fails; the re-run, with nothing
// listened in between, used to read the drained row as "put back and
// listened to since" and report a complete undo as done in part.
func TestRev6_RerunAfterALandedPutBackOnAStatelessSurvivorIsSilent(t *testing.T) {
	store, keep, gone, user, res := rev6Seed(t)
	_, err := NewService(&rev6Store{PebbleStore: store, failLockDel: 1}).UndoCombine(res.JournalID)
	require.ErrorContains(t, err, "injected lock delete failure")
	require.Equal(t, float64(0), rev5Listened(t, store, user, keep), "the put-back landed: the survivor holds the drained row")

	re, err := NewService(store).UndoCombine(res.JournalID)
	require.NoError(t, err)
	require.Empty(t, re.Warnings, "nothing was listened to between the runs")
	require.Equal(t, float64(600), rev5Listened(t, store, user, gone), "the absorbed book has its 600 seconds back")
	require.Equal(t, float64(0), rev5Listened(t, store, user, keep), "the survivor holds only the drained row")
}

// S1, reconcile path: the user listened to the survivor after the combine,
// so the undo reconciles (the carried 600 seconds go back, the 300 listened
// since stay). A later step fails; the re-run with nothing listened in
// between is silent.
func TestRev6_RerunAfterALandedReconcileIsSilent(t *testing.T) {
	store, keep, gone, user, res := rev6Seed(t)
	rev5AddListening(t, store, user, keep, 300)
	_, err := NewService(&rev6Store{PebbleStore: store, failLockDel: 1}).UndoCombine(res.JournalID)
	require.ErrorContains(t, err, "injected lock delete failure")
	require.Equal(t, float64(300), rev5Listened(t, store, user, keep), "the reconcile landed: before 0 + the 300 listened since")

	re, err := NewService(store).UndoCombine(res.JournalID)
	require.NoError(t, err)
	require.Empty(t, re.Warnings, "nothing was listened to between the runs")
	require.Equal(t, float64(300), rev5Listened(t, store, user, keep), "the carried seconds are not taken off again")
	require.Equal(t, float64(600), rev5Listened(t, store, user, gone))
}

// S1: a put-back (plain, or the reconcile) landed and recorded so in its
// marker, a later step failed, and the user then listened to the survivor.
// The re-run leaves the survivor alone and says nothing: the put-back was
// complete and the new listening is the user's own. It is kept, and the
// carried 600 seconds are counted once, on the absorbed book.
func TestRev6_RerunAfterALandedPutBackAndNewListeningIsSilent(t *testing.T) {
	for _, tc := range []struct {
		name       string
		listenedBy float64 // listened on the survivor before the undo
		want       float64 // survivor after the re-run
	}{
		{name: "put-back", listenedBy: 0, want: 0 + 250},
		{name: "reconcile", listenedBy: 300, want: 300 + 250},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, keep, gone, user, res := rev6Seed(t)
			if tc.listenedBy > 0 {
				rev5AddListening(t, store, user, keep, int(tc.listenedBy))
			}
			_, err := NewService(&rev6Store{PebbleStore: store, failLockDel: 1}).UndoCombine(res.JournalID)
			require.ErrorContains(t, err, "injected lock delete failure")
			require.Equal(t, tc.listenedBy, rev5Listened(t, store, user, keep), "the put-back landed")
			rev5AddListening(t, store, user, keep, 250)

			re, err := NewService(store).UndoCombine(res.JournalID)
			require.NoError(t, err)
			require.Empty(t, re.Warnings, "the put-back was complete; the listening since is the user's own")
			require.Equal(t, tc.want, rev5Listened(t, store, user, keep), "the 250 seconds listened since are kept, nothing taken off again")
			require.Equal(t, float64(600), rev5Listened(t, store, user, gone), "the carried seconds are on the absorbed book once")
		})
	}
}

// S1 fallback: when the marker cannot record the landed write, the put-back
// still succeeds (it landed), and a re-run falls back to reading the
// written-since state as landed, with the warning. The data is right either
// way.
func TestRev6_LandedMarkerWriteFailureFallsBackToTheWarning(t *testing.T) {
	store, keep, gone, user, res := rev6Seed(t)
	w := &rev6Store{PebbleStore: store, failLockDel: 1, failLandedMarker: true}
	_, err := NewService(w).UndoCombine(res.JournalID)
	require.ErrorContains(t, err, "injected lock delete failure", "the undo went on past the put-back")
	require.NotContains(t, err.Error(), "landed-marker", "a landed put-back is not failed for its marker")
	require.Positive(t, w.landedMarkerFails, "the put-back tried to record its landed write")

	re, err := NewService(store).UndoCombine(res.JournalID)
	require.NoError(t, err)
	require.Contains(t, strings.Join(WarningTexts(re.Warnings), "\n"), "was put back by an earlier run of this undo and listened to since; left as is")
	require.Equal(t, float64(600), rev5Listened(t, store, user, gone))
	require.Equal(t, float64(0), rev5Listened(t, store, user, keep))
}

// lockBetweenPlanAndWrite sets the user's own title lock on book right after
// the override's title column write (ModifyBook) lands: after the combine
// planned the override and before it writes the lock rows.
type lockBetweenPlanAndWrite struct {
	*database.PebbleStore
	book  string
	value string
	fired bool
	err   error
}

func (s *lockBetweenPlanAndWrite) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	b, err := s.PebbleStore.ModifyBook(id, fn)
	if err == nil && id == s.book && !s.fired && b != nil && b.Title == "Override T" {
		s.fired = true
		s.err = database.RecordUserOverrides(s.PebbleStore, s.book, map[string]any{database.FieldKeyTitle: s.value})
	}
	return b, err
}

// N2: a person locks the survivor's title between the override's plan and
// its lock write. The override replaces that lock, and its undo puts the
// person's lock back. The before-snapshot used to be the plan's, read ahead
// of the write without the metadata-state lock, so the undo put back the
// older lock ("Prior T") instead.
func TestRev6_OverrideUndoRestoresALockSetBetweenPlanAndWrite(t *testing.T) {
	store := setupTestStore(t).(*database.PebbleStore)
	keep, gone := overrideBooks(t, store)
	require.NoError(t, database.RecordUserOverrides(store, keep, map[string]any{database.FieldKeyTitle: "Prior T"}))
	w := &lockBetweenPlanAndWrite{PebbleStore: store, book: keep, value: "User T"}
	res, err := NewService(w).CombineBooksWithOptions([]string{keep, gone}, keep, &CombineOverride{Title: "Override T"}, CombineOptions{})
	require.NoError(t, err)
	require.True(t, w.fired, "the person's lock landed between the plan and the lock write")
	require.NoError(t, w.err)
	require.Equal(t, "Override T", lockValue(t, lockOf(t, store, keep, database.FieldKeyTitle)), "the override replaced it")

	_, err = NewService(store).UndoCombine(res.JournalID)
	require.NoError(t, err)
	require.Equal(t, "User T", lockValue(t, lockOf(t, store, keep, database.FieldKeyTitle)), "the undo puts back the lock the override replaced")
}
