// file: internal/merge/combine_undo_rev5_test.go
// version: 1.0.1
// guid: 5c1e8f47-2a9d-4b63-9e07-d84f3a6b21c9
// last-edited: 2026-10-18

package merge

import (
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
