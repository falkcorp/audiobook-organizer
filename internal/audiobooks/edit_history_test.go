// file: internal/audiobooks/edit_history_test.go
// version: 1.4.0
// guid: 6f1a8c34-2d9b-4e70-a5c3-0b7e4d2f9a51
// last-edited: 2026-10-04

package audiobooks_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// The single-book edit (PUT /audiobooks/:id) records a "manual" history row
// for EVERY column it changes. Until 2026-09-30 the handler recorded six
// fields and only non-empty values, so a PUT of format (or a clear) left no
// history and a queued metadata apply overwrote it silently. Fields the
// override bookkeeping already recorded (isbn13 here: "override",
// "user_edit") are not listed twice.
func TestUpdateAudiobook_ChangeOfAnUnlistedFieldRefusesAQueuedApply(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	book, err := store.CreateBook(&database.Book{Title: "T", FilePath: "/library/u.m4b", Format: "m4b"})
	require.NoError(t, err)

	mfs := metafetch.NewService(store)
	mark, err := mfs.ApplyEditMark(book.ID)
	require.NoError(t, err)

	_, err = audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID,
		map[string]any{"format": "mp3", "isbn13": "9780000000002"})
	require.NoError(t, err)

	edits, err := mfs.ApplyEditsSince(book.ID, mark, "apply-queued-own")
	require.NoError(t, err)
	require.Contains(t, edits.Others, "format (manual, manual)")
	require.Contains(t, edits.Others, "isbn13 (override, user_edit)")
	require.NotContains(t, edits.Others, "isbn13 (manual, manual)", "isbn13 was recorded twice")

	// A PUT that sets the value the book already has records nothing more.
	before, err := store.GetBookChangeHistory(book.ID, 1000)
	require.NoError(t, err)
	_, err = audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID,
		map[string]any{"format": "mp3"})
	require.NoError(t, err)
	after, err := store.GetBookChangeHistory(book.ID, 1000)
	require.NoError(t, err)
	require.Len(t, after, len(before), "a no-op PUT recorded history")
}

// historyFailStore fails RecordMetadataChange for rows fail picks.
type historyFailStore struct {
	*database.PebbleStore
	fail func(*database.MetadataChangeRecord) bool
}

func (s historyFailStore) RecordMetadataChange(r *database.MetadataChangeRecord) error {
	if s.fail(r) {
		return errors.New("history store down")
	}
	return s.PebbleStore.RecordMetadataChange(r)
}

func editFixture(t *testing.T) (*database.PebbleStore, *database.Book) {
	t.Helper()
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	book, err := store.CreateBook(&database.Book{Title: "T", FilePath: "/library/e.m4b", Format: "m4b"})
	require.NoError(t, err)
	return store, book
}

// The column rows are stamped AFTER the write commits, so they are never
// older than the override rows the same edit wrote before it (a queued
// apply's mark taken between the two must not miss the edit).
func TestUpdateAudiobook_ManualRowsAreStampedAfterOverrideRows(t *testing.T) {
	store, book := editFixture(t)
	_, err := audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID,
		map[string]any{"format": "mp3", "isbn13": "9780000000002"})
	require.NoError(t, err)
	history, err := store.GetBookChangeHistory(book.ID, 100)
	require.NoError(t, err)
	var overrideAt, manualAt int64
	for _, r := range history {
		switch r.ChangeType {
		case "override":
			overrideAt = max(overrideAt, r.ChangedAt.UnixNano())
		case database.ChangeTypeManual:
			manualAt = r.ChangedAt.UnixNano()
		}
	}
	require.NotZero(t, overrideAt)
	require.NotZero(t, manualAt)
	require.GreaterOrEqual(t, manualAt, overrideAt, "a manual row is older than the edit's override rows")
}

// An override row that fails to record must not make the column diff skip
// the field: it then gets its manual row.
func TestUpdateAudiobook_FailedOverrideRowStillGetsAColumnRow(t *testing.T) {
	store, book := editFixture(t)
	fs := historyFailStore{store, func(r *database.MetadataChangeRecord) bool { return r.ChangeType == "override" }}
	_, err := audiobooks.NewAudiobookUpdateService(fs).UpdateAudiobook(context.Background(), book.ID,
		map[string]any{"isbn13": "9780000000002"})
	require.NoError(t, err)
	history, err := store.GetBookChangeHistory(book.ID, 100)
	require.NoError(t, err)
	found := false
	for _, r := range history {
		if r.Field == "isbn13" && r.ChangeType == database.ChangeTypeManual {
			found = true
		}
	}
	require.True(t, found, "isbn13 has no history row at all: %+v", history)
}

// A history failure after the row committed does not fail the edit: the
// request succeeds (the edit landed) and the rest of the save still runs.
func TestUpdateAudiobook_HistoryFailureDoesNotFailALandedEdit(t *testing.T) {
	store, book := editFixture(t)
	fs := historyFailStore{store, func(*database.MetadataChangeRecord) bool { return true }}
	got, err := audiobooks.NewAudiobookUpdateService(fs).UpdateAudiobook(context.Background(), book.ID,
		map[string]any{"format": "mp3"})
	require.NoError(t, err, "a landed edit was reported as failed")
	require.Equal(t, "mp3", got.Format)
	row, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.Equal(t, "mp3", row.Format)
}

// stateFailStore fails every field-state write.
type stateFailStore struct{ *database.PebbleStore }

func (s stateFailStore) UpsertMetadataFieldState(*database.MetadataFieldState) error {
	return errors.New("field state store down")
}

// The field-state save runs before the history that describes it. When it
// fails, the edit (which committed) still succeeds, and no "override" row
// claims a lock that was never written: the column diff records the field as
// a "manual" row instead, so a queued metadata apply still sees the edit.
// Until 2026-10-03 the override rows were written first and the request then
// returned 500 for a landed edit.
func TestUpdateAudiobook_FailedStateSaveWritesNoOverrideHistory(t *testing.T) {
	store, book := editFixture(t)
	mfs := metafetch.NewService(store)
	mark, err := mfs.ApplyEditMark(book.ID)
	require.NoError(t, err)

	got, err := audiobooks.NewAudiobookUpdateService(stateFailStore{store}).UpdateAudiobook(context.Background(), book.ID,
		map[string]any{"isbn13": "9780000000002"})
	require.NoError(t, err, "a landed edit was reported as failed")
	require.NotNil(t, got.ISBN13)
	require.Equal(t, "9780000000002", *got.ISBN13)

	edits, err := mfs.ApplyEditsSince(book.ID, mark, "apply-queued-own")
	require.NoError(t, err)
	require.NotContains(t, edits.Others, "isbn13 (override, user_edit)", "an override row claims a lock that was never written")
	require.Contains(t, edits.Others, "isbn13 (manual, manual)", "the edit left no history a queued apply can see")
	states, err := store.GetMetadataFieldStates(book.ID)
	require.NoError(t, err)
	require.Empty(t, states)
}

// A field-state save failure is a 200 for the landed edit, but the caller is
// told: UpdateAudiobookWithWarnings returns one warning saying the locks and
// overrides were not saved (the PUT handler returns it to the client).
func TestUpdateAudiobookWithWarnings_FailedStateSaveIsReported(t *testing.T) {
	store, book := editFixture(t)
	got, warnings, err := audiobooks.NewAudiobookUpdateService(stateFailStore{store}).UpdateAudiobookWithWarnings(
		context.Background(), book.ID, map[string]any{"isbn13": "9780000000002"})
	require.NoError(t, err, "a landed edit was reported as failed")
	require.NotNil(t, got)
	require.Len(t, warnings, 1, "%v", warnings)
	require.Contains(t, warnings[0], "the edit was saved but its field locks and overrides were not")
	require.Contains(t, warnings[0], "field state store down")
}

// A history failure is reported the same way.
func TestUpdateAudiobookWithWarnings_HistoryFailureIsReported(t *testing.T) {
	store, book := editFixture(t)
	fs := historyFailStore{store, func(*database.MetadataChangeRecord) bool { return true }}
	_, warnings, err := audiobooks.NewAudiobookUpdateService(fs).UpdateAudiobookWithWarnings(
		context.Background(), book.ID, map[string]any{"format": "mp3"})
	require.NoError(t, err)
	require.Len(t, warnings, 1, "%v", warnings)
	require.Contains(t, warnings[0], "the edit was saved but its change history was not fully recorded")
}

// A clean edit has no warnings.
func TestUpdateAudiobookWithWarnings_CleanEditHasNone(t *testing.T) {
	store, book := editFixture(t)
	_, warnings, err := audiobooks.NewAudiobookUpdateService(store).UpdateAudiobookWithWarnings(
		context.Background(), book.ID, map[string]any{"isbn13": "9780000000002"})
	require.NoError(t, err)
	require.Empty(t, warnings)
}

// A case-only series rename whose history row cannot be recorded is
// reported (the rename landed; a queued apply would not see it).
func TestUpdateAudiobookWithWarnings_SeriesRenameHistoryFailureIsReported(t *testing.T) {
	store, book, _, _ := caseFixture(t, false)
	// Both rows that can carry the rename fail: the series_name override row
	// (then the rename falls back to its own "series" row) and that row.
	fs := historyFailStore{store, func(r *database.MetadataChangeRecord) bool {
		return r.Field == database.HistoryFieldSeries || r.Field == database.FieldKeySeriesName
	}}
	_, warnings, err := audiobooks.NewAudiobookUpdateService(fs).UpdateAudiobookWithWarnings(
		context.Background(), book.ID, map[string]any{"series_name": "The Saga"})
	require.NoError(t, err)
	found := false
	for _, w := range warnings {
		found = found || strings.Contains(w, "series rename was not recorded")
	}
	require.True(t, found, "%v", warnings)
}

// narratorFailStore fails the book_narrators write.
type narratorFailStore struct{ *database.PebbleStore }

func (s narratorFailStore) SetBookNarrators(string, []database.BookNarrator) error {
	return errors.New("junction store down")
}

// A narrator edit whose junction write fails is reported.
func TestUpdateAudiobookWithWarnings_NarratorJunctionFailureIsReported(t *testing.T) {
	store, book := editFixture(t)
	_, warnings, err := audiobooks.NewAudiobookUpdateService(narratorFailStore{store}).UpdateAudiobookWithWarnings(
		context.Background(), book.ID, map[string]any{"narrator": "Jane Reader"})
	require.NoError(t, err)
	require.NotEmpty(t, warnings)
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "narrator list (book_narrators) was not updated") && strings.Contains(w, "junction store down") {
			found = true
		}
	}
	require.True(t, found, "%v", warnings)
}
