// file: internal/merge/combine_undo_rev5_test.go
// version: 1.0.2
// guid: 5c1e8f47-2a9d-4b63-9e07-d84f3a6b21c9
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

// Round-5 review of #4064: regression tests for the survivor put-back's
// locking and markers, and for re-running an undo that could not record
// itself.

// rev5Store injects failures and client writes into an undo.
type rev5Store struct {
	*database.PebbleStore
	survivor string
	// onSurvivorPositions runs once, on the first read of the survivor's
	// positions: a client write that lands after the put-back read the
	// survivor's state and before it takes the user-state stripe.
	onSurvivorPositions func()
	// failSurvState fails that many survivor state writes.
	failSurvState int
	// failMarkerDel fails that many reconcile-marker deletes.
	failMarkerDel int
	// failNotLanded fails the overwrite that withdraws a marker.
	failNotLanded bool
}

func (s *rev5Store) SetUserBookState(st *database.UserBookState) error {
	if st.BookID == s.survivor && s.failSurvState > 0 {
		s.failSurvState--
		return errors.New("injected survivor state write failure")
	}
	return s.PebbleStore.SetUserBookState(st)
}

func (s *rev5Store) DeleteRaw(key string) error {
	if strings.HasPrefix(key, survivorReconcilePrefix) && s.failMarkerDel > 0 {
		s.failMarkerDel--
		return errors.New("injected marker delete failure")
	}
	return s.PebbleStore.DeleteRaw(key)
}

func (s *rev5Store) SetRaw(key string, value []byte) error {
	if s.failNotLanded && strings.HasPrefix(key, survivorReconcilePrefix) && strings.Contains(string(value), `"not_landed":true`) {
		return errors.New("injected marker withdraw failure")
	}
	return s.PebbleStore.SetRaw(key, value)
}

func (s *rev5Store) ListUserPositionsForBook(userID, bookID string) ([]database.UserPosition, error) {
	if bookID == s.survivor && s.onSurvivorPositions != nil {
		f := s.onSurvivorPositions
		s.onSurvivorPositions = nil
		f()
	}
	return s.PebbleStore.ListUserPositionsForBook(userID, bookID)
}

// rev5Seed combines the undo fixture with listened time on both sides:
// 600 seconds on the absorbed book (carried), 100 on the survivor.
func rev5Seed(t *testing.T) (*database.PebbleStore, undoFixture, *CombineResult) {
	t.Helper()
	store := setupTestStore(t).(*database.PebbleStore)
	f := seedUndoFixture(t, store)
	at := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	require.NoError(t, store.SetUserBookState(&database.UserBookState{UserID: f.user.ID, BookID: f.absA, Status: "in_progress", ProgressPct: 40, LastActivityAt: at, LastSegmentID: "seg-a", TotalListenedSeconds: 600}))
	require.NoError(t, store.SetUserBookState(&database.UserBookState{UserID: f.user.ID, BookID: f.survivor, Status: "in_progress", ProgressPct: 10, LastActivityAt: at.Add(-time.Hour), LastSegmentID: "seg-s", TotalListenedSeconds: 100}))
	res, err := NewService(store).CombineBooks([]string{f.survivor, f.absA, f.absB}, f.survivor, nil)
	require.NoError(t, err)
	require.Equal(t, float64(600), rev5Listened(t, store, f.user.ID, f.survivor), "the follow carried the absorbed book's listened time")
	return store, f, res
}

func rev5Listened(t *testing.T, store *database.PebbleStore, user, book string) float64 {
	t.Helper()
	st, err := store.GetUserBookState(user, book)
	require.NoError(t, err)
	require.NotNil(t, st)
	return float64(st.TotalListenedSeconds)
}

// B1: a device sync that lands while the plain survivor put-back is deciding
// is kept. The client takes the user-state stripe, as the ABS handlers do.
// Before the fix the put-back took no stripe and did not re-read, so it
// wrote the before-snapshot (100 seconds) over the sync.
func TestRev5_PlainPutBackKeepsAClientWriteThatLandsBeforeTheStripe(t *testing.T) {
	store, f, res := rev5Seed(t)
	w := &rev5Store{PebbleStore: store, survivor: f.survivor}
	fired := false
	w.onSurvivorPositions = func() {
		fired = true
		defer database.LockUserBookState(f.user.ID, f.survivor)()
		cur, err := store.GetUserBookState(f.user.ID, f.survivor)
		require.NoError(t, err)
		c := *cur
		c.TotalListenedSeconds += 900
		c.LastActivityAt = time.Now().UTC()
		c.ProgressPct = 88
		require.NoError(t, store.SetUserBookState(&c))
	}
	_, err := NewService(w).UndoCombine(res.JournalID)
	require.NoError(t, err)
	require.True(t, fired, "the client write ran inside the survivor put-back")
	// The carried 600 seconds go back to the absorbed book; the 900 the
	// client added since the follow stay on the survivor.
	require.Equal(t, float64(1000), rev5Listened(t, store, f.user.ID, f.survivor), "the client's 900 seconds are kept")
	require.Equal(t, float64(600), rev5Listened(t, store, f.user.ID, f.absA))
}

// S3: the user re-picks the combine's own title (column and lock) after an
// undo that completed but could not record itself. The re-run finds the
// undo's applied record and only records the journal; it used to replay,
// read both as the combine's, and take them away again.
func TestRev5_RerunKeepsAUserRepickOfTheOverrideValue(t *testing.T) {
	store := setupTestStore(t).(*database.PebbleStore)
	ms, keep, res := combineWithTitleOverride(t, store)
	undoWithoutRecording(t, store, ms, res.JournalID)
	_, err := store.ModifyBook(keep, func(b *database.Book) error { b.Title = "Override T"; return nil })
	require.NoError(t, err)
	require.NoError(t, database.RecordUserOverrides(store, keep, map[string]any{database.FieldKeyTitle: "Override T"}))

	re, err := ms.UndoCombine(res.JournalID)
	require.NoError(t, err)
	require.Empty(t, re.Warnings)
	b, err := store.GetBookByID(keep)
	require.NoError(t, err)
	require.Equal(t, "Override T", b.Title, "the title the user picked after the undo stays")
	require.Equal(t, "Override T", lockValue(t, lockOf(t, store, keep, database.FieldKeyTitle)), "and so does their lock")
	j, err := ms.GetCombineJournal(res.JournalID)
	require.NoError(t, err)
	require.Equal(t, CombineJournalUndone, j.Status)
}

// S3: the user credits the override's author again after an unrecorded undo;
// the re-run leaves the link.
func TestRev5_RerunKeepsAReaddedOverrideAuthorLink(t *testing.T) {
	store := setupTestStore(t).(*database.PebbleStore)
	keep, gone := overrideBooks(t, store)
	prior, err := store.CreateAuthor("Prior Writer")
	require.NoError(t, err)
	require.NoError(t, store.SetBookAuthors(keep, []database.BookAuthor{{BookID: keep, AuthorID: prior.ID, Role: "author"}}))
	ms := NewService(store)
	res, err := ms.CombineBooksWithOptions([]string{keep, gone}, keep, &CombineOverride{Author: "Override Writer"}, CombineOptions{})
	require.NoError(t, err)
	j, err := ms.GetCombineJournal(res.JournalID)
	require.NoError(t, err)
	require.NotNil(t, j.Override.AuthorIDAfter)
	a := *j.Override.AuthorIDAfter
	undoWithoutRecording(t, store, ms, res.JournalID)
	require.NoError(t, store.SetBookAuthors(keep, []database.BookAuthor{
		{BookID: keep, AuthorID: prior.ID, Role: "author", Position: 0},
		{BookID: keep, AuthorID: a, Role: "author", Position: 1},
	}))

	re, err := ms.UndoCombine(res.JournalID)
	require.NoError(t, err)
	require.Empty(t, re.Warnings)
	cur, err := store.GetBookAuthors(keep)
	require.NoError(t, err)
	found := false
	for _, l := range cur {
		if l.AuthorID == a && l.Role == "author" {
			found = true
		}
	}
	require.True(t, found, "the link the user added after the undo survives the re-run: %+v", cur)
}

// rev5AddListening is the user listening to the survivor: more listened time
// and a newer last-played, written as a client does.
func rev5AddListening(t *testing.T, store *database.PebbleStore, user, book string, seconds int) {
	t.Helper()
	cur, err := store.GetUserBookState(user, book)
	require.NoError(t, err)
	c := *cur
	c.TotalListenedSeconds += float64(seconds)
	c.LastActivityAt = time.Now().UTC()
	require.NoError(t, store.SetUserBookState(&c))
}

// S2: the plain put-back's state write fails and so does the delete of its
// marker. The marker is withdrawn as not landed, so the re-run after the
// user listened reconciles instead of skipping: the carried 600 seconds are
// not left on the survivor as well (it used to read 600 + 300 = 900).
func TestRev5_FailedPutBackWithAStuckMarkerIsNotReadAsLanded(t *testing.T) {
	store, f, res := rev5Seed(t)
	w := &rev5Store{PebbleStore: store, survivor: f.survivor, failSurvState: 1, failMarkerDel: 1}
	_, err := NewService(w).UndoCombine(res.JournalID)
	require.ErrorContains(t, err, "injected survivor state write failure")
	require.NotContains(t, err.Error(), "neither dropped", "the marker was withdrawn")
	rev5AddListening(t, store, f.user.ID, f.survivor, 300)

	_, err = NewService(store).UndoCombine(res.JournalID)
	require.NoError(t, err)
	require.Equal(t, float64(400), rev5Listened(t, store, f.user.ID, f.survivor), "before 100 + the 300 listened since")
	require.Equal(t, float64(600), rev5Listened(t, store, f.user.ID, f.absA))
}

// S2, reconcile path: the user listened to the survivor before the undo, so
// it reconciles; that state write fails and the marker delete fails. The
// re-run after more listening reconciles again from the after-snapshot.
func TestRev5_FailedReconcileWithAStuckMarkerIsNotReadAsLanded(t *testing.T) {
	store, f, res := rev5Seed(t)
	rev5AddListening(t, store, f.user.ID, f.survivor, 200)
	w := &rev5Store{PebbleStore: store, survivor: f.survivor, failSurvState: 1, failMarkerDel: 1}
	_, err := NewService(w).UndoCombine(res.JournalID)
	require.ErrorContains(t, err, "injected survivor state write failure")
	rev5AddListening(t, store, f.user.ID, f.survivor, 300)

	_, err = NewService(store).UndoCombine(res.JournalID)
	require.NoError(t, err)
	require.Equal(t, float64(600), rev5Listened(t, store, f.user.ID, f.survivor), "before 100 + 200 + 300 listened since the follow")
	require.Equal(t, float64(600), rev5Listened(t, store, f.user.ID, f.absA))
}

// S2: when the marker can be neither deleted nor withdrawn, the error says
// what a re-run will do, and that re-run reports its skip.
func TestRev5_StuckMarkerIsNamedInTheErrorAndTheSkipIsReported(t *testing.T) {
	store, f, res := rev5Seed(t)
	w := &rev5Store{PebbleStore: store, survivor: f.survivor, failSurvState: 1, failMarkerDel: 1, failNotLanded: true}
	_, err := NewService(w).UndoCombine(res.JournalID)
	require.ErrorContains(t, err, "could be neither dropped")
	j, err := NewService(store).GetCombineJournal(res.JournalID)
	require.NoError(t, err)
	require.Contains(t, j.LastError, "could be neither dropped", "the journal names it")
	rev5AddListening(t, store, f.user.ID, f.survivor, 300)

	re, err := NewService(store).UndoCombine(res.JournalID)
	require.NoError(t, err)
	require.Contains(t, strings.Join(re.Warnings, "\n"), "was put back by an earlier run of this undo and listened to since; left as is")
}

// A put-back whose state write fails (the marker delete works) is finished
// by the re-run with nothing listened in between.
func TestRev5_PartialPutBackThenRerun(t *testing.T) {
	store, f, res := rev5Seed(t)
	w := &rev5Store{PebbleStore: store, survivor: f.survivor, failSurvState: 1}
	_, err := NewService(w).UndoCombine(res.JournalID)
	require.Error(t, err)
	_, err = NewService(store).UndoCombine(res.JournalID)
	require.NoError(t, err)
	require.Equal(t, float64(100), rev5Listened(t, store, f.user.ID, f.survivor))
	require.Equal(t, float64(600), rev5Listened(t, store, f.user.ID, f.absA))
}
