// file: internal/audiobooks/edit_clears_fields_test.go
// version: 1.0.0
// guid: 451212a4-52da-4236-9a22-658fce80859a
// last-edited: 2026-10-03

package audiobooks_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// seriesFixture is a book linked to a series, with a position (both the int
// and the raw string), as a scan or a metadata apply leaves it.
func seriesFixture(t *testing.T) (*database.PebbleStore, *database.Book, *database.Series) {
	t.Helper()
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	series, err := store.CreateSeries("Redshirts", nil)
	require.NoError(t, err)
	seq, raw := 1, "1"
	book, err := store.CreateBook(&database.Book{
		Title: "Redshirts", FilePath: "/library/redshirts.m4b", Format: "m4b",
		SeriesID: &series.ID, Series: series, SeriesSequence: &seq, SeriesPositionRaw: &raw,
	})
	require.NoError(t, err)
	return store, book, series
}

func fieldStates(t *testing.T, store *database.PebbleStore, bookID string) map[string]database.MetadataFieldState {
	t.Helper()
	rows, err := store.GetMetadataFieldStates(bookID)
	require.NoError(t, err)
	out := map[string]database.MetadataFieldState{}
	for _, r := range rows {
		out[r.Field] = r
	}
	return out
}

func historyByField(t *testing.T, store *database.PebbleStore, bookID string) map[string][]database.MetadataChangeRecord {
	t.Helper()
	rows, err := store.GetBookChangeHistory(bookID, 1000)
	require.NoError(t, err)
	out := map[string][]database.MetadataChangeRecord{}
	for _, r := range rows {
		out[r.Field] = append(out[r.Field], r)
	}
	return out
}

func requireSeriesGone(t *testing.T, store *database.PebbleStore, got *database.Book) {
	t.Helper()
	require.Nil(t, got.SeriesID, "response still links a series")
	require.Nil(t, got.Series, "response still carries the series object")
	require.Nil(t, got.SeriesSequence, "response still has a series position")

	row, err := store.GetBookByID(got.ID)
	require.NoError(t, err)
	require.Nil(t, row.SeriesID, "stored row still links a series")
	require.Nil(t, row.Series, "stored row still carries the series object (reads prefer it)")
	require.Nil(t, row.SeriesSequence, "stored row still has a series position")
	require.Nil(t, row.SeriesPositionRaw, "stored row still has a raw series position")

	// The read path a GET serves: no series name may come back.
	view, err := audiobooks.NewAudiobookService(store).GetAudiobook(context.Background(), got.ID)
	require.NoError(t, err)
	require.Nil(t, view.Series, "GET still shows the series")
}

// Observed on prod 2026-10-03: PUT {"series_name": ""} returned 200 and the
// book kept its series. SeriesID did go nil, but the embedded Series object
// survived the save and the store's preserve-on-nil guard, and every read
// prefers that object.
func TestUpdateAudiobook_EmptySeriesNameClearsTheSeries(t *testing.T) {
	store, book, _ := seriesFixture(t)

	got, err := audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID,
		map[string]any{"series_name": ""})
	require.NoError(t, err)
	requireSeriesGone(t, store, got)

	// Exactly one history row for the series (old name -> ""), plus the
	// rows for the two position columns the clear also emptied, and nothing
	// else.
	h := historyByField(t, store, book.ID)
	require.Len(t, h[database.HistoryFieldSeries], 1, "series history: %+v", h[database.HistoryFieldSeries])
	r := h[database.HistoryFieldSeries][0]
	require.Equal(t, database.ChangeTypeManual, r.ChangeType)
	require.NotNil(t, r.PreviousValue)
	require.Equal(t, `"Redshirts"`, *r.PreviousValue)
	require.Equal(t, `""`, *r.NewValue)
	require.Len(t, h[database.HistoryFieldSeriesNo], 1, "series_position history: %+v", h[database.HistoryFieldSeriesNo])
	require.Len(t, h["series_position_raw"], 1)
	require.Len(t, h, 3, "unexpected history fields: %+v", h)

	// The clear is locked like a set, so a rescan or apply cannot bring the
	// series (or a stale position) back.
	locks, err := database.LockedUserFields(store, book.ID)
	require.NoError(t, err)
	require.True(t, locks[database.FieldKeySeriesName], "series_name not locked after the clear")
	require.True(t, locks[database.FieldKeySeriesPosition], "series_position not locked after the clear")
}

// `"series_id": null` is the same clear. It used to be dropped entirely:
// ExtractIntField reports a JSON null as "absent".
func TestUpdateAudiobook_NullSeriesIDClearsTheSeries(t *testing.T) {
	store, book, _ := seriesFixture(t)

	got, err := audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID,
		map[string]any{"series_id": nil})
	require.NoError(t, err)
	requireSeriesGone(t, store, got)
	require.Len(t, historyByField(t, store, book.ID)[database.HistoryFieldSeries], 1)
	locks, err := database.LockedUserFields(store, book.ID)
	require.NoError(t, err)
	require.True(t, locks[database.FieldKeySeriesName])
}

// The web editor clears a series by sending series_name "" at top level AND
// an override {"value": "", "locked": true} for the dirty field. That path
// records the override row itself; the column diff must not add a second.
func TestUpdateAudiobook_EditorSeriesClearRecordsOneRow(t *testing.T) {
	store, book, _ := seriesFixture(t)

	got, err := audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID,
		map[string]any{
			"series_name":     "",
			"series_position": 1, // the editor still sends the old number
			"overrides":       map[string]any{"series_name": map[string]any{"value": "", "locked": true}},
		})
	require.NoError(t, err)
	requireSeriesGone(t, store, got)
	// The override bookkeeping records the clear under the lock key
	// (series_name, "override"/"user_edit") and suppresses the column row.
	h := historyByField(t, store, book.ID)
	require.Len(t, h[database.FieldKeySeriesName], 1, "override row: %+v", h)
	require.Empty(t, h[database.HistoryFieldSeries], "series recorded twice: %+v", h)
	st := fieldStates(t, store, book.ID)
	require.True(t, st[database.FieldKeySeriesName].OverrideLocked)
	require.True(t, st[database.FieldKeySeriesPosition].OverrideLocked)
}

// A clear replaces a previously locked series override, so the old locked
// value cannot be re-projected onto the book.
func TestUpdateAudiobook_SeriesClearReplacesALockedSeriesOverride(t *testing.T) {
	store, book, _ := seriesFixture(t)
	svc := audiobooks.NewAudiobookUpdateService(store)
	_, err := svc.UpdateAudiobook(context.Background(), book.ID, map[string]any{"series_name": "Redshirts", "series_position": 1})
	require.NoError(t, err)
	before := fieldStates(t, store, book.ID)
	require.True(t, before[database.FieldKeySeriesName].OverrideLocked, "fixture: set did not lock")

	got, err := svc.UpdateAudiobook(context.Background(), book.ID, map[string]any{"series_name": "  "})
	require.NoError(t, err)
	requireSeriesGone(t, store, got)
	after := fieldStates(t, store, book.ID)
	require.True(t, after[database.FieldKeySeriesName].OverrideLocked)
	require.NotNil(t, after[database.FieldKeySeriesName].OverrideValue)
	require.Equal(t, `""`, *after[database.FieldKeySeriesName].OverrideValue)
	require.Nil(t, after[database.FieldKeySeriesPosition].OverrideValue, "stale locked position survived the clear")
}

// A book already hit by the bug (SeriesID nil, stale Series object) is
// cleaned up by a repeat clear.
func TestUpdateAudiobook_SeriesClearRepairsAStaleSeriesObject(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	series, err := store.CreateSeries("Redshirts", nil)
	require.NoError(t, err)
	book, err := store.CreateBook(&database.Book{Title: "Redshirts", FilePath: "/library/r.m4b", Format: "m4b", Series: series})
	require.NoError(t, err)
	row, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.NotNil(t, row.Series, "fixture: stale object not stored")

	got, err := audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID,
		map[string]any{"series_name": ""})
	require.NoError(t, err)
	requireSeriesGone(t, store, got)
}

// The editor sends series_name "" on every save. For a book with no series
// that is not an edit: no history, and no lock that would stop a later fetch
// from ever adding a series.
func TestUpdateAudiobook_EmptySeriesNameOnASeriesLessBookChangesNothing(t *testing.T) {
	store, book := editFixture(t)

	_, err := audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID,
		map[string]any{"series_name": ""})
	require.NoError(t, err)
	require.Empty(t, historyByField(t, store, book.ID))
	locks, err := database.LockedUserFields(store, book.ID)
	require.NoError(t, err)
	require.False(t, locks[database.FieldKeySeriesName])
	require.False(t, locks[database.FieldKeySeriesPosition])
}

// description, genre, asin and series_position were parsed from the
// top-level payload and then never applied: a set or a clear did nothing,
// and the field was locked at its OLD value.
func TestUpdateAudiobook_TopLevelDescriptionGenreASINPositionApply(t *testing.T) {
	store, book := editFixture(t)
	svc := audiobooks.NewAudiobookUpdateService(store)

	_, err := svc.UpdateAudiobook(context.Background(), book.ID, map[string]any{
		"description": "A desc", "genre": "SF", "asin": "B000000001", "series_position": 3,
	})
	require.NoError(t, err)
	row, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.NotNil(t, row.Description)
	require.Equal(t, "A desc", *row.Description)
	require.NotNil(t, row.Genre)
	require.Equal(t, "SF", *row.Genre)
	require.NotNil(t, row.ASIN)
	require.Equal(t, "B000000001", *row.ASIN)
	require.NotNil(t, row.SeriesSequence)
	require.Equal(t, 3, *row.SeriesSequence)
	st := fieldStates(t, store, book.ID)
	require.Equal(t, `"A desc"`, *st[database.FieldKeyDescription].OverrideValue, "lock holds the old value")

	_, err = svc.UpdateAudiobook(context.Background(), book.ID, map[string]any{
		"description": "", "genre": "", "asin": "",
	})
	require.NoError(t, err)
	row, err = store.GetBookByID(book.ID)
	require.NoError(t, err)
	for name, v := range map[string]*string{"description": row.Description, "genre": row.Genre, "asin": row.ASIN} {
		require.True(t, v == nil || *v == "", "%s not cleared: %v", name, v)
	}
	h := historyByField(t, store, book.ID)
	for _, f := range []string{"description", "genre", "asin"} {
		require.NotEmpty(t, h[f], "%s change left no history", f)
	}
}

// author_name "" is ignored: the editor sends it on every save, and it used
// to half-clear (author_id nil, join and embedded Author kept).
func TestUpdateAudiobook_EmptyAuthorNameIsIgnored(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	a, err := store.CreateAuthor("John Scalzi")
	require.NoError(t, err)
	book, err := store.CreateBook(&database.Book{Title: "T", FilePath: "/library/a.m4b", Format: "m4b", AuthorID: &a.ID, Author: a})
	require.NoError(t, err)
	require.NoError(t, store.SetBookAuthors(book.ID, []database.BookAuthor{{BookID: book.ID, AuthorID: a.ID, Role: "author"}}))

	_, err = audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID,
		map[string]any{"author_name": ""})
	require.NoError(t, err)
	row, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.NotNil(t, row.AuthorID, "author_name \"\" cleared author_id")
	require.Equal(t, a.ID, *row.AuthorID)
	join, err := store.GetBookAuthors(book.ID)
	require.NoError(t, err)
	require.Len(t, join, 1)
	require.Empty(t, historyByField(t, store, book.ID))
	locks, err := database.LockedUserFields(store, book.ID)
	require.NoError(t, err)
	require.False(t, locks[database.FieldKeyAuthorName], "an ignored author_name locked the field")
}

// A narrator clear empties the book_narrators junction too: the store's sync
// runs only for a non-empty credit, and ABS prefers the junction.
func TestUpdateAudiobook_EmptyNarratorClearsTheJunction(t *testing.T) {
	store, book := editFixture(t)
	svc := audiobooks.NewAudiobookUpdateService(store)
	_, err := svc.UpdateAudiobook(context.Background(), book.ID, map[string]any{"narrator": "Wil Wheaton"})
	require.NoError(t, err)
	bn, err := store.GetBookNarrators(book.ID)
	require.NoError(t, err)
	require.Len(t, bn, 1, "fixture: narrator junction not written")

	_, err = svc.UpdateAudiobook(context.Background(), book.ID, map[string]any{"narrator": ""})
	require.NoError(t, err)
	row, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.True(t, row.Narrator == nil || *row.Narrator == "")
	bn, err = store.GetBookNarrators(book.ID)
	require.NoError(t, err)
	require.Empty(t, bn, "narrator junction kept the cleared cast")
	require.NotEmpty(t, historyByField(t, store, book.ID)["narrator"])
}
