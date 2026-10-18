// file: internal/merge/combine_undo_replay_test.go
// version: 1.0.0
// guid: 9e3a6d12-7c48-4b05-a1f9-2d8e5b70c364
// last-edited: 2026-10-18

package merge

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metastate"
)

// Round-4 S2 (#4064): an undo that finishes but whose final journal write
// fails leaves the journal undo_failed (UndoCombine marks it so before its
// first write), so it can be run again, and that re-run replays applyUndo.
// The replay must not change anything the user did after the first undo.
// Before the fix it removed a lock the user set after the undo, took the
// carried listened seconds off the survivor a second time, and reported
// put-backs it did not make.

// failUndoneJournalStore fails the combine-journal write that records the
// undo as done, so a COMPLETED undo leaves its journal as the pre-mark wrote
// it.
type failUndoneJournalStore struct {
	*database.PebbleStore
}

func (s *failUndoneJournalStore) SetRaw(key string, value []byte) error {
	if strings.HasPrefix(key, combineJournalPrefix) && strings.Contains(string(value), `"status":"undone"`) {
		return errors.New("injected undone-journal failure")
	}
	return s.PebbleStore.SetRaw(key, value)
}

// undoWithoutRecording runs an undo that completes but cannot record itself,
// and checks it left the journal as a cut-short undo.
func undoWithoutRecording(t *testing.T, store *database.PebbleStore, ms *Service, journalID string) {
	t.Helper()
	_, err := NewService(&failUndoneJournalStore{PebbleStore: store}).UndoCombine(journalID)
	require.NoError(t, err, "the undo itself completes")
	j, err := ms.GetCombineJournal(journalID)
	require.NoError(t, err)
	require.Equal(t, CombineJournalUndoFailed, j.Status, "the journal still reads as an undo in progress")
}

// fullProgress is every field of a user's progress on a book that the undo
// can write, positions included.
func fullProgress(t *testing.T, store *database.PebbleStore, userID, bookID string) string {
	t.Helper()
	st, err := store.GetUserBookState(userID, bookID)
	require.NoError(t, err)
	pos, err := store.ListUserPositionsForBook(userID, bookID)
	require.NoError(t, err)
	var segs []string
	for _, p := range pos {
		segs = append(segs, fmt.Sprintf("%s@%v/%d", p.SegmentID, p.PositionSeconds, p.UpdatedAt.UnixNano()))
	}
	slices.Sort(segs)
	if st == nil {
		return fmt.Sprintf("no state %v", segs)
	}
	return fmt.Sprintf("status=%s manual=%v pct=%d listened=%v last=%s activity=%d hide=%v seg=%s %v",
		st.Status, st.StatusManual, st.ProgressPct, st.TotalListenedSeconds, st.LastSegmentID,
		st.LastActivityAt.UnixNano(), st.HideFromContinueListening, st.LastSegmentID, segs)
}

func TestUndoCombine_ReplayAfterUnrecordedUndoChangesNothing(t *testing.T) {
	for _, mode := range []string{"no listening since", "listened to the survivor since", "listened to an absorbed book since"} {
		t.Run(mode, func(t *testing.T) {
			store := setupTestStore(t).(*database.PebbleStore)
			f := seedUndoFixture(t, store)
			at := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
			// Listened time on both sides, so a carried-seconds subtraction
			// shows.
			require.NoError(t, store.SetUserBookState(&database.UserBookState{UserID: f.user.ID, BookID: f.absA, Status: "in_progress", ProgressPct: 40, LastActivityAt: at, LastSegmentID: "seg-a", TotalListenedSeconds: 600}))
			require.NoError(t, store.SetUserBookState(&database.UserBookState{UserID: f.user.ID, BookID: f.survivor, Status: "in_progress", ProgressPct: 10, LastActivityAt: at.Add(-time.Hour), LastSegmentID: "seg-s", TotalListenedSeconds: 100}))
			ms := NewService(store)
			res, err := ms.CombineBooks([]string{f.survivor, f.absA, f.absB}, f.survivor, nil)
			require.NoError(t, err)
			undoWithoutRecording(t, store, ms, res.JournalID)

			now := time.Now().UTC().Truncate(time.Second)
			switch mode {
			case "listened to the survivor since":
				// More than the carried 600 seconds, so a second take-back
				// would land and remove real listening.
				require.NoError(t, store.SetUserPosition(f.user.ID, f.survivor, "seg-new", 4321))
				require.NoError(t, store.SetUserBookState(&database.UserBookState{UserID: f.user.ID, BookID: f.survivor, Status: "in_progress", ProgressPct: 77, LastActivityAt: now, LastSegmentID: "seg-new", TotalListenedSeconds: 2100}))
			case "listened to an absorbed book since":
				require.NoError(t, store.SetUserPosition(f.user.ID, f.absA, "seg-a", 5555))
				require.NoError(t, store.SetUserBookState(&database.UserBookState{UserID: f.user.ID, BookID: f.absA, Status: "in_progress", ProgressPct: 66, LastActivityAt: now, LastSegmentID: "seg-a", TotalListenedSeconds: 900}))
			}
			before := map[string]string{}
			for _, id := range []string{f.survivor, f.absA, f.absB} {
				before[id] = fullProgress(t, store, f.user.ID, id)
			}

			re, err := ms.UndoCombine(res.JournalID)
			require.NoError(t, err, "the re-run finishes the undo")
			require.Empty(t, re.Warnings, "the re-run reports no put-back it did not make")
			require.Empty(t, re.RestoredBooks)
			require.Zero(t, re.FilesMoved)
			for _, id := range []string{f.survivor, f.absA, f.absB} {
				require.Equal(t, before[id], fullProgress(t, store, f.user.ID, id), "progress on %s untouched by the re-run", id)
			}
			j, err := ms.GetCombineJournal(res.JournalID)
			require.NoError(t, err)
			require.Equal(t, CombineJournalUndone, j.Status)
		})
	}
}

// lockOf returns bookID's lock row for field, nil when there is none.
func lockOf(t *testing.T, store *database.PebbleStore, bookID, field string) *database.MetadataFieldState {
	t.Helper()
	states, err := store.GetMetadataFieldStates(bookID)
	require.NoError(t, err)
	for i := range states {
		if states[i].Field == field {
			return &states[i]
		}
	}
	return nil
}

func lockValue(t *testing.T, st *database.MetadataFieldState) string {
	t.Helper()
	require.NotNil(t, st)
	require.NotNil(t, st.OverrideValue)
	v, ok := metastate.Decode(st.OverrideValue).(string)
	require.True(t, ok, "a string lock value: %s", *st.OverrideValue)
	return v
}

// combineWithTitleOverride combines two books, setting the survivor's title.
func combineWithTitleOverride(t *testing.T, store *database.PebbleStore) (*Service, string, *CombineResult) {
	t.Helper()
	keep, gone := overrideBooks(t, store)
	ms := NewService(store)
	res, err := ms.CombineBooksWithOptions([]string{keep, gone}, keep, &CombineOverride{Title: "Override T"}, CombineOptions{})
	require.NoError(t, err)
	require.Equal(t, "Override T", lockValue(t, lockOf(t, store, keep, database.FieldKeyTitle)))
	return ms, keep, res
}

// The lock the user sets after a completed (unrecorded) undo survives the
// re-run, which says it left it. It used to be removed: step 4 restored the
// journaled lock rows unconditionally.
func TestUndoCombine_ReplayKeepsALockSetAfterTheUndo(t *testing.T) {
	store := setupTestStore(t).(*database.PebbleStore)
	ms, keep, res := combineWithTitleOverride(t, store)
	j, err := ms.GetCombineJournal(res.JournalID)
	require.NoError(t, err)
	require.NotNil(t, j.Override.FieldStatesAfter[database.FieldKeyTitle], "the journal records the lock the override wrote")
	undoWithoutRecording(t, store, ms, res.JournalID)
	require.Nil(t, lockOf(t, store, keep, database.FieldKeyTitle), "the first undo removed the combine's lock")

	b, err := store.GetBookByID(keep)
	require.NoError(t, err)
	require.NoError(t, database.RecordUserOverrides(store, keep, map[string]any{database.FieldKeyTitle: b.Title}))

	re, err := ms.UndoCombine(res.JournalID)
	require.NoError(t, err)
	require.Contains(t, strings.Join(re.Warnings, "\n"), "title lock on book "+keep+" was changed after the combine; left as it is")
	require.Equal(t, b.Title, lockValue(t, lockOf(t, store, keep, database.FieldKeyTitle)), "the user's later lock survives the re-run")
}

// A lock the user changed between the combine and the undo is kept, with a
// warning; the title column still goes back.
func TestUndoCombine_KeepsALockChangedSinceTheCombine(t *testing.T) {
	store := setupTestStore(t).(*database.PebbleStore)
	ms, keep, res := combineWithTitleOverride(t, store)
	require.NoError(t, database.RecordUserOverrides(store, keep, map[string]any{database.FieldKeyTitle: "My Own Pick"}))

	undo, err := ms.UndoCombine(res.JournalID)
	require.NoError(t, err)
	require.Contains(t, strings.Join(undo.Warnings, "\n"), "title lock on book "+keep+" was changed after the combine")
	require.Equal(t, "My Own Pick", lockValue(t, lockOf(t, store, keep, database.FieldKeyTitle)))
	b, err := store.GetBookByID(keep)
	require.NoError(t, err)
	require.Equal(t, "B", b.Title, "the override's title is still taken back")
}

// A metadata fetch since the combine wrote a fetched value onto the lock row
// without touching the lock: the lock is still the combine's and goes, the
// fetched value stays.
func TestUndoCombine_RemovesTheCombinesLockKeepingALaterFetchedValue(t *testing.T) {
	store := setupTestStore(t).(*database.PebbleStore)
	ms, keep, res := combineWithTitleOverride(t, store)
	st := lockOf(t, store, keep, database.FieldKeyTitle)
	fetched, err := metastate.Encode("Fetched Title")
	require.NoError(t, err)
	st.FetchedValue = fetched
	st.UpdatedAt = time.Now()
	require.NoError(t, store.UpsertMetadataFieldState(st))

	undo, err := ms.UndoCombine(res.JournalID)
	require.NoError(t, err)
	require.Empty(t, undo.Warnings)
	got := lockOf(t, store, keep, database.FieldKeyTitle)
	require.NotNil(t, got, "the row stays for its fetched value")
	require.False(t, got.OverrideLocked, "the combine's lock is gone")
	require.Nil(t, got.OverrideValue)
	require.NotNil(t, got.FetchedValue)
	require.Equal(t, *fetched, *got.FetchedValue, "the later fetch is kept")
}

// A journal written before FieldStatesAfter: a lock that still holds what the
// override wrote is removed; one holding anything else is kept.
func TestUndoCombine_JournalWithoutLockAfterState(t *testing.T) {
	stripAfter := func(t *testing.T, ms *Service, store *database.PebbleStore, id string) {
		t.Helper()
		j, err := ms.GetCombineJournal(id)
		require.NoError(t, err)
		j.Override.FieldStatesAfter = nil
		require.NoError(t, ms.putCombineJournal(j))
		raw, err := store.GetRaw(combineJournalPrefix + id)
		require.NoError(t, err)
		require.NotContains(t, string(raw), "field_states_after", "the stored journal reads as one written before the field")
	}
	t.Run("lock as the override wrote it is removed", func(t *testing.T) {
		store := setupTestStore(t).(*database.PebbleStore)
		ms, keep, res := combineWithTitleOverride(t, store)
		stripAfter(t, ms, store, res.JournalID)
		undo, err := ms.UndoCombine(res.JournalID)
		require.NoError(t, err)
		require.Empty(t, undo.Warnings)
		require.Nil(t, lockOf(t, store, keep, database.FieldKeyTitle))
	})
	t.Run("a different lock is kept", func(t *testing.T) {
		store := setupTestStore(t).(*database.PebbleStore)
		ms, keep, res := combineWithTitleOverride(t, store)
		stripAfter(t, ms, store, res.JournalID)
		require.NoError(t, database.RecordUserOverrides(store, keep, map[string]any{database.FieldKeyTitle: "My Own Pick"}))
		undo, err := ms.UndoCombine(res.JournalID)
		require.NoError(t, err)
		require.Contains(t, strings.Join(undo.Warnings, "\n"), "was changed after the combine")
		require.Equal(t, "My Own Pick", lockValue(t, lockOf(t, store, keep, database.FieldKeyTitle)))
	})
}
