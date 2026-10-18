// file: internal/merge/combine_undo_rev6_test.go
// version: 1.1.0
// guid: 5e0dd46d-1455-44fc-94b7-899dc9c35055
// last-edited: 2026-10-18

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
	// landed state (recordPutBackLanded).
	failLandedMarker bool
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

// S1 fallback: when the marker cannot record the landed write, the put-back
// still succeeds (it landed), and a re-run falls back to reading the
// written-since state as landed, with the warning. The data is right either
// way.
func TestRev6_LandedMarkerWriteFailureFallsBackToTheWarning(t *testing.T) {
	store, keep, gone, user, res := rev6Seed(t)
	_, err := NewService(&rev6Store{PebbleStore: store, failLockDel: 1, failLandedMarker: true}).UndoCombine(res.JournalID)
	require.ErrorContains(t, err, "injected lock delete failure")
	require.NotContains(t, err.Error(), "landed-marker", "a landed put-back is not failed for its marker")

	re, err := NewService(store).UndoCombine(res.JournalID)
	require.NoError(t, err)
	require.Contains(t, strings.Join(re.Warnings, "\n"), "was put back by an earlier run of this undo and listened to since; left as is")
	require.Equal(t, float64(600), rev5Listened(t, store, user, gone))
	require.Equal(t, float64(0), rev5Listened(t, store, user, keep))
}
