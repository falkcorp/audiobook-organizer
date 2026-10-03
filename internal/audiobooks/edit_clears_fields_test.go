// file: internal/audiobooks/edit_clears_fields_test.go
// version: 1.8.0
// guid: 451212a4-52da-4236-9a22-658fce80859a
// last-edited: 2026-10-03

package audiobooks_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

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
	// series back. The position gets no lock of its own: the series_name lock
	// already blanks it, and a position lock would outlive a later re-set.
	locks, err := database.LockedUserFields(store, book.ID)
	require.NoError(t, err)
	require.True(t, locks[database.FieldKeySeriesName], "series_name not locked after the clear")
	require.False(t, locks[database.FieldKeySeriesPosition], "series_position locked by the clear")
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
	require.False(t, st[database.FieldKeySeriesPosition].OverrideLocked, "series_position locked by the clear")
}

// A clear replaces a previously locked series override, so the old locked
// value cannot be re-projected onto the book.
func TestUpdateAudiobook_SeriesClearReplacesALockedSeriesOverride(t *testing.T) {
	store, book, _ := seriesFixture(t)
	svc := audiobooks.NewAudiobookUpdateService(store)
	// Lock a series and position through overrides (re-sending an unchanged
	// value no longer locks it, so the fixture changes both).
	_, err := svc.UpdateAudiobook(context.Background(), book.ID, map[string]any{"overrides": map[string]any{
		"series_name":     map[string]any{"value": "Old Man's War", "locked": true},
		"series_position": map[string]any{"value": 2, "locked": true},
	}})
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
	require.False(t, after[database.FieldKeySeriesPosition].OverrideLocked, "stale position lock survived the clear")
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
	require.Equal(t, `"A desc"`, *st[database.FieldKeyDescription].OverrideValue, "lock does not hold the new value")

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

// lockedKeys is the set of lock keys the book has locked.
func lockedKeys(t *testing.T, store *database.PebbleStore, bookID string) []string {
	t.Helper()
	locks, err := database.LockedUserFields(store, bookID)
	require.NoError(t, err)
	var out []string
	for k, v := range locks {
		if v {
			out = append(out, k)
		}
	}
	return out
}

// bookDetailSave is the exact body web/src/pages/BookDetail.tsx sends for a
// title-only edit of a book with no description, publisher, language,
// narrator, ISBN, author, series, position or year: every text field goes as
// "" (undefined fields are dropped by JSON), and the dirty title also goes as
// a locked override.
func bookDetailSave(title string) map[string]any {
	return map[string]any{
		"title":       title,
		"description": "",
		"publisher":   "",
		"language":    "",
		"narrator":    "",
		"isbn":        "",
		"author_name": "",
		"series_name": "",
		"overrides": map[string]any{
			"title": map[string]any{"value": title, "locked": true},
		},
	}
}

// Review B1 (2026-10-03): a BookDetail title edit of a book with a nil
// description locked description (and publisher, language, narrator) at ""
// and recorded nil -> "" history for each, so no metadata fetch could ever
// fill them. A "" for an already-empty field is not an edit.
func TestUpdateAudiobook_BookDetailTitleSaveLocksOnlyTitle(t *testing.T) {
	store, book := editFixture(t)
	require.Nil(t, book.Description, "fixture: description must start nil")

	_, err := audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID,
		bookDetailSave("Renamed"))
	require.NoError(t, err)

	require.ElementsMatch(t, []string{database.FieldKeyTitle}, lockedKeys(t, store, book.ID))
	for field := range historyByField(t, store, book.ID) {
		require.Equal(t, database.FieldKeyTitle, field, "history row for a field the edit did not change")
	}
	row, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.Equal(t, "Renamed", row.Title)
	for name, v := range map[string]*string{
		"description": row.Description, "publisher": row.Publisher,
		"language": row.Language, "narrator": row.Narrator,
	} {
		require.Nil(t, v, "%s went from nil to a value", name)
	}
}

// The same no-op holds for a field stored as "" rather than nil, and for
// every top-level text field, not only the ones BookDetail sends.
func TestUpdateAudiobook_BlankForAnEmptyStringFieldChangesNothing(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	empty := func() *string { return new("") }
	book, err := store.CreateBook(&database.Book{
		Title: "T", FilePath: "/library/blank.m4b", Format: "m4b",
		Description: empty(), Publisher: empty(), Language: empty(), Narrator: empty(),
		Genre: empty(), ASIN: empty(), ISBN10: empty(), ISBN13: empty(),
	})
	require.NoError(t, err)

	_, err = audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID,
		map[string]any{
			"description": "", "publisher": "", "language": "", "narrator": " ",
			"genre": "", "asin": "", "isbn10": "", "isbn13": "",
		})
	require.NoError(t, err)
	require.Empty(t, lockedKeys(t, store, book.ID))
	require.Empty(t, historyByField(t, store, book.ID))
}

// Review S1: a SeriesID whose series row is gone shows no series on GET, so
// the editor sends series_name "" for it like for any series-less book. The
// dangling link is dropped, but that is not a user clearing a series they
// could see: no lock, and the position stays.
func TestUpdateAudiobook_DanglingSeriesIDIsDroppedWithoutLockOrPositionWipe(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	dangling, seq := 999999, 4
	book, err := store.CreateBook(&database.Book{Title: "T", FilePath: "/library/d.m4b", Format: "m4b",
		SeriesID: &dangling, SeriesSequence: &seq})
	require.NoError(t, err)

	_, err = audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID,
		bookDetailSave("Renamed"))
	require.NoError(t, err)
	row, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.Nil(t, row.SeriesID, "dangling series link kept")
	require.NotNil(t, row.SeriesSequence, "position wiped for a series the user could not see")
	require.Equal(t, 4, *row.SeriesSequence)
	require.ElementsMatch(t, []string{database.FieldKeyTitle}, lockedKeys(t, store, book.ID))
}

// Review S2: the editor's author clear is an author_name override of "".
// The author cannot be removed from this endpoint, so the request is refused
// (400 at the handler) before anything is written: it used to lock
// author_name at "" and record history while the author stayed.
func TestUpdateAudiobook_AuthorOverrideClearIsRejected(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	a, err := store.CreateAuthor("John Scalzi")
	require.NoError(t, err)
	book, err := store.CreateBook(&database.Book{Title: "T", FilePath: "/library/a.m4b", Format: "m4b", AuthorID: &a.ID, Author: a})
	require.NoError(t, err)

	for _, value := range []any{"", "  ", nil} {
		p := bookDetailSave("Renamed")
		p["overrides"] = map[string]any{"author_name": map[string]any{"value": value, "locked": true}}
		_, err = audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID, p)
		require.Error(t, err, "value %#v", value)
		require.True(t, errors.Is(err, audiobooks.ErrInvalidAudiobookUpdate), "value %#v: %v", value, err)
		require.Contains(t, err.Error(), "the author cannot be cleared; set a different author")
	}

	row, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.NotNil(t, row.AuthorID)
	require.Equal(t, a.ID, *row.AuthorID)
	require.Equal(t, "T", row.Title, "a refused request still wrote the title")
	require.Empty(t, lockedKeys(t, store, book.ID))
	require.Empty(t, historyByField(t, store, book.ID))
	require.Empty(t, fieldStates(t, store, book.ID))
}

// Review S3: the clear used to lock series_position too. That lock outlived
// a later re-set of the series, so a fetch could never fill the new number.
func TestUpdateAudiobook_SeriesClearThenReSetLeavesPositionUnlocked(t *testing.T) {
	store, book, _ := seriesFixture(t)
	svc := audiobooks.NewAudiobookUpdateService(store)
	_, err := svc.UpdateAudiobook(context.Background(), book.ID, map[string]any{"series_name": ""})
	require.NoError(t, err)
	_, err = svc.UpdateAudiobook(context.Background(), book.ID, map[string]any{"series_name": "Old Man's War"})
	require.NoError(t, err)
	locks, err := database.LockedUserFields(store, book.ID)
	require.NoError(t, err)
	require.True(t, locks[database.FieldKeySeriesName])
	require.False(t, locks[database.FieldKeySeriesPosition], "series_position lock outlived the re-set")
}

// Review S4: GET fills the narrator from book_narrators when the column is
// empty, so a user can see -- and clear -- a narrator that lives only in the
// junction. That clear used to be skipped and the cast came back.
func TestUpdateAudiobook_JunctionOnlyNarratorClearEmptiesTheJunction(t *testing.T) {
	store, book := editFixture(t)
	n, err := store.CreateNarrator("Wil Wheaton")
	require.NoError(t, err)
	require.NoError(t, store.SetBookNarrators(book.ID, []database.BookNarrator{{BookID: book.ID, NarratorID: n.ID, Role: "narrator"}}))

	_, err = audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID,
		map[string]any{"narrator": ""})
	require.NoError(t, err)
	bn, err := store.GetBookNarrators(book.ID)
	require.NoError(t, err)
	require.Empty(t, bn, "junction-only narrator survived the clear")
	locks, err := database.LockedUserFields(store, book.ID)
	require.NoError(t, err)
	require.True(t, locks[database.FieldKeyNarrator], "a real narrator clear was not locked")
}

// The junction is now written after the book commits (review N1). For a
// changed credit the store's sync, which runs inside that commit, writes the
// CLEANED cast ("Narrated by" stripped, the book's own author dropped), and
// that cast must survive: before the move it overwrote the service's raw
// split, and the service's later write must not now overwrite it back.
func TestUpdateAudiobook_NarratorSetKeepsTheStoresCleanedCast(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	a, err := store.CreateAuthor("John Scalzi")
	require.NoError(t, err)
	book, err := store.CreateBook(&database.Book{Title: "T", FilePath: "/library/n.m4b", Format: "m4b", AuthorID: &a.ID, Author: a})
	require.NoError(t, err)
	require.NoError(t, store.SetBookAuthors(book.ID, []database.BookAuthor{{BookID: book.ID, AuthorID: a.ID, Role: "author"}}))

	_, err = audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID,
		map[string]any{"narrator": "Narrated by Wil Wheaton & John Scalzi"})
	require.NoError(t, err)

	bn, err := store.GetBookNarrators(book.ID)
	require.NoError(t, err)
	var names []string
	for _, r := range bn {
		n, err := store.GetNarratorByID(r.NarratorID)
		require.NoError(t, err)
		require.NotNil(t, n)
		names = append(names, n.Name)
	}
	require.Equal(t, []string{"Wil Wheaton"}, names, "junction is not the store's cleaned cast")
}

// requireSeriesPosition checks the position the stored row and a GET both
// report: the int, and the raw position exactly as wantRaw (the value as the
// client sent it, decimal included).
func requireSeriesPosition(t *testing.T, store *database.PebbleStore, bookID string, want int, wantRaw string) {
	t.Helper()
	row, err := store.GetBookByID(bookID)
	require.NoError(t, err)
	require.NotNil(t, row.SeriesSequence, "stored series_sequence is nil")
	require.Equal(t, want, *row.SeriesSequence, "stored series_sequence")
	require.NotNil(t, row.SeriesPositionRaw, "stored raw position is nil")
	require.Equal(t, wantRaw, *row.SeriesPositionRaw, "stored raw position")
	view, err := audiobooks.NewAudiobookService(store).GetAudiobook(context.Background(), bookID)
	require.NoError(t, err)
	require.NotNil(t, view.SeriesSequence, "GET series_sequence is nil")
	require.Equal(t, want, *view.SeriesSequence, "GET series_sequence")
	st := fieldStates(t, store, bookID)[database.FieldKeySeriesPosition]
	if st.OverrideLocked {
		require.NotNil(t, st.OverrideValue)
		require.Contains(t, []string{strconv.Itoa(want), wantRaw, strconv.Quote(wantRaw)}, *st.OverrideValue, "series_position locked at a different value")
	}
}

// Observed on prod 2026-10-03 (main d2a4a290e): PUT {"series_name":
// "Amaranthe", "series_position": 8}, creating the series in the same
// request, stored series_sequence 1, the book's old position. A follow-up
// PUT {"series_position": 8} alone also left it at 1. series_position was
// parsed but never applied, and the extractor then locked the OLD value.
func TestUpdateAudiobook_NewSeriesAndPositionTogetherStoresThePosition(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	seq, raw := 1, "1"
	book, err := store.CreateBook(&database.Book{Title: "Amaranthe 8", FilePath: "/library/am8.m4b", Format: "m4b",
		SeriesSequence: &seq, SeriesPositionRaw: &raw})
	require.NoError(t, err)

	_, err = audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID,
		map[string]any{"series_name": "Amaranthe", "series_position": float64(8)})
	require.NoError(t, err)
	row, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.NotNil(t, row.SeriesID, "series not linked")
	requireSeriesPosition(t, store, book.ID, 8, "8")
}

func TestUpdateAudiobook_PositionAloneOnABookWithASeriesStoresThePosition(t *testing.T) {
	store, book, _ := seriesFixture(t) // position 1, raw "1"

	_, err := audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID,
		map[string]any{"series_position": float64(8)})
	require.NoError(t, err)
	requireSeriesPosition(t, store, book.ID, 8, "8")
}

// --- 2026-10-03 re-review: "sent" means the client named the field ---------

// richFixture is a book with something in every place a stray write could
// reach: a cleaned narrator cast in the junction, two authors in the join,
// a decimal raw series position, and text fields.
func richFixture(t *testing.T) (*database.PebbleStore, *database.Book) {
	t.Helper()
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	a, err := store.CreateAuthor("Terry Pratchett")
	require.NoError(t, err)
	co, err := store.CreateAuthor("Neil Gaiman")
	require.NoError(t, err)
	s, err := store.CreateSeries("Discworld", nil)
	require.NoError(t, err)
	seq, raw, desc, credit := 1, "1.5", "A description", "Narrated by Stephen Briggs & Terry Pratchett"
	book, err := store.CreateBook(&database.Book{Title: "Good Omens", FilePath: "/library/g.m4b", Format: "m4b",
		AuthorID: &a.ID, Author: a, SeriesID: &s.ID, Series: s, SeriesSequence: &seq, SeriesPositionRaw: &raw,
		Description: &desc})
	require.NoError(t, err)
	require.NoError(t, store.SetBookAuthors(book.ID, []database.BookAuthor{
		{BookID: book.ID, AuthorID: a.ID, Role: "author", Position: 0},
		{BookID: book.ID, AuthorID: co.ID, Role: "co-author", Position: 1}}))
	// The narrator written by a store write, so the store's own sync puts
	// its cleaned cast in the junction (as a scan or an apply leaves it).
	_, err = store.ModifyBook(book.ID, func(b *database.Book) error { b.Narrator = &credit; return nil })
	require.NoError(t, err)
	require.Equal(t, []string{"Stephen Briggs"}, narratorNames(t, store, book.ID), "fixture: cleaned cast")
	require.Empty(t, historyByField(t, store, book.ID), "fixture: history")
	require.Empty(t, fieldStates(t, store, book.ID), "fixture: field state")
	row, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	return store, row
}

func narratorNames(t *testing.T, store *database.PebbleStore, id string) []string {
	t.Helper()
	bn, err := store.GetBookNarrators(id)
	require.NoError(t, err)
	var names []string
	for _, r := range bn {
		n, err := store.GetNarratorByID(r.NarratorID)
		require.NoError(t, err)
		require.NotNil(t, n)
		names = append(names, n.Name)
	}
	return names
}

// The root cause: the update service pre-filled the request from the stored
// row, so every field the book had looked sent. A PUT {"title": "X"} must
// change the title and nothing else -- not the junction, not the co-authors,
// not the raw position -- and record and lock nothing else.
func TestUpdateAudiobook_TitleOnlyPutTouchesNothingButTitle(t *testing.T) {
	store, before := richFixture(t)
	beforeAuthors, err := store.GetBookAuthors(before.ID)
	require.NoError(t, err)

	_, err = audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), before.ID,
		map[string]any{"title": "Good Omens!"})
	require.NoError(t, err)

	after, err := store.GetBookByID(before.ID)
	require.NoError(t, err)
	require.Equal(t, "Good Omens!", after.Title)
	after.Title, after.UpdatedAt = before.Title, before.UpdatedAt
	wantJSON, err := json.Marshal(before)
	require.NoError(t, err)
	gotJSON, err := json.Marshal(after)
	require.NoError(t, err)
	require.JSONEq(t, string(wantJSON), string(gotJSON), "a title-only PUT changed another column")
	authors, err := store.GetBookAuthors(before.ID)
	require.NoError(t, err)
	require.Equal(t, beforeAuthors, authors, "a title-only PUT rewrote book_authors (co-authors collapsed)")
	require.Equal(t, []string{"Stephen Briggs"}, narratorNames(t, store, before.ID), "a title-only PUT rewrote book_narrators")
	for field := range historyByField(t, store, before.ID) {
		require.Equal(t, database.FieldKeyTitle, field, "history row for a field the PUT did not send")
	}
	require.ElementsMatch(t, []string{database.FieldKeyTitle}, lockedKeys(t, store, before.ID))
}

// Review B1: every save rewrote SeriesPositionRaw from the int, turning
// "1.5" into "1" and recording a history row. Re-sending the same series
// name is not a position edit either.
func TestUpdateAudiobook_UnsentPositionKeepsTheRawPosition(t *testing.T) {
	for name, body := range map[string]map[string]any{
		"title only":       {"title": "Only title"},
		"same series name": {"series_name": "Discworld", "title": "Good Omens"},
	} {
		t.Run(name, func(t *testing.T) {
			store, before := richFixture(t)
			_, err := audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), before.ID, body)
			require.NoError(t, err)
			requireSeriesPosition(t, store, before.ID, 1, "1.5")
			h := historyByField(t, store, before.ID)
			require.Empty(t, h["series_position_raw"], "raw position history for an unsent position")
			require.Empty(t, h[database.HistoryFieldSeriesNo])
			require.NotContains(t, lockedKeys(t, store, before.ID), database.FieldKeySeriesName,
				"re-sending the unchanged series name locked it")
		})
	}
}

// A sent position is stored as sent: "2.5" or 2.5 keeps the decimal in the
// raw position (the int gets 2), top level or override; null clears both.
// An override of "2.5" used to fail Atoi and change nothing.
func TestUpdateAudiobook_SentPositionIsStoredAsSent(t *testing.T) {
	for name, body := range map[string]map[string]any{
		"override string":  {"overrides": map[string]any{"series_position": map[string]any{"value": "2.5", "locked": true}}},
		"override number":  {"overrides": map[string]any{"series_position": map[string]any{"value": 2.5, "locked": true}}},
		"top-level":        {"series_position": 2.5},
		"top-level string": {"series_position": "2.5"},
	} {
		t.Run(name, func(t *testing.T) {
			store, before := richFixture(t)
			_, err := audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), before.ID, body)
			require.NoError(t, err)
			requireSeriesPosition(t, store, before.ID, 2, "2.5")
		})
	}
	for name, body := range map[string]map[string]any{
		"override null":  {"overrides": map[string]any{"series_position": map[string]any{"value": nil, "locked": true}}},
		"top-level null": {"series_position": nil},
	} {
		t.Run(name, func(t *testing.T) {
			store, before := richFixture(t)
			_, err := audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), before.ID, body)
			require.NoError(t, err)
			row, err := store.GetBookByID(before.ID)
			require.NoError(t, err)
			require.Nil(t, row.SeriesSequence, "a null position did not clear the int")
			require.Nil(t, row.SeriesPositionRaw, "a null position did not clear the raw position")
			require.NotNil(t, row.SeriesID, "clearing the position unlinked the series")
		})
	}
}

// Review B2: a title-only PUT emptied book_narrators when the column held
// "" (the narrator looked sent as "", and "" with a junction is a clear).
func TestUpdateAudiobook_TitleOnlyPutKeepsAJunctionOnlyNarrator(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	empty := ""
	book, err := store.CreateBook(&database.Book{Title: "T", FilePath: "/library/j.m4b", Format: "m4b", Narrator: &empty})
	require.NoError(t, err)
	n, err := store.CreateNarrator("Wil Wheaton")
	require.NoError(t, err)
	require.NoError(t, store.SetBookNarrators(book.ID, []database.BookNarrator{{BookID: book.ID, NarratorID: n.ID, Role: "narrator"}}))

	_, err = audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID,
		map[string]any{"title": "Only title"})
	require.NoError(t, err)
	require.Equal(t, []string{"Wil Wheaton"}, narratorNames(t, store, book.ID))
}

// Review S-a: BookDetail re-sends the narrator unchanged on every save. That
// used to rewrite the junction from a raw split, undoing the store's cleaned
// cast and creating a junk "Narrated by ..." narrator.
func TestUpdateAudiobook_UnchangedNarratorKeepsTheCleanedCast(t *testing.T) {
	store, before := richFixture(t)
	_, err := audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), before.ID, map[string]any{
		"title": "Renamed", "narrator": *before.Narrator, "author_name": "Terry Pratchett",
		"overrides": map[string]any{"title": map[string]any{"value": "Renamed", "locked": true}},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"Stephen Briggs"}, narratorNames(t, store, before.ID))
	junk, err := store.GetNarratorByName("Narrated by Stephen Briggs")
	require.NoError(t, err)
	require.Nil(t, junk, "a junk narrator entity was created from the raw credit")
	require.NotContains(t, lockedKeys(t, store, before.ID), database.FieldKeyNarrator, "an unchanged narrator was locked")
}

// A credit naming only the book's own author is a self-read: the author
// goes into the junction as the narrator.
func TestUpdateAudiobook_SelfReadNarratorCreditGoesInTheJunction(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	a, err := store.CreateAuthor("John Scalzi")
	require.NoError(t, err)
	book, err := store.CreateBook(&database.Book{Title: "T", FilePath: "/library/n.m4b", Format: "m4b", AuthorID: &a.ID, Author: a})
	require.NoError(t, err)
	require.NoError(t, store.SetBookAuthors(book.ID, []database.BookAuthor{{BookID: book.ID, AuthorID: a.ID, Role: "author"}}))
	_, err = audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID,
		map[string]any{"narrator": "John Scalzi"})
	require.NoError(t, err)
	require.Equal(t, []string{"John Scalzi"}, narratorNames(t, store, book.ID))

	// With a prefix: still the author, and no junk "Narrated by ..." entity.
	_, err = audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID,
		map[string]any{"narrator": "Narrated by John Scalzi"})
	require.NoError(t, err)
	require.Equal(t, []string{"John Scalzi"}, narratorNames(t, store, book.ID))
	junk, err := store.GetNarratorByName("Narrated by John Scalzi")
	require.NoError(t, err)
	require.Nil(t, junk, "a junk narrator entity was created from a prefixed self-read credit")
}

// Review S-c: a refused edit writes nothing. The override history rows used
// to be written before the author gate refused the request.
func TestUpdateAudiobook_RefusedEditLeavesNoHistory(t *testing.T) {
	store, book := editFixture(t)
	_, err := audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID, map[string]any{
		"title":       "X",
		"author_name": "Unknown",
		"overrides": map[string]any{
			"title":       map[string]any{"value": "X", "locked": true},
			"author_name": map[string]any{"value": "Unknown", "locked": true},
		},
	})
	require.Error(t, err)
	row, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.Equal(t, "T", row.Title)
	require.Empty(t, historyByField(t, store, book.ID), "a refused edit left history")
	require.Empty(t, fieldStates(t, store, book.ID), "a refused edit left field state")
}

// An author-less book whose author box was typed in and emptied again: the
// override "" has no author to clear, so it is a no-op -- the rest of the
// save goes through, and nothing is locked or recorded for the author.
func TestUpdateAudiobook_AuthorOverrideClearOnAnAuthorlessBookIsANoop(t *testing.T) {
	for name, value := range map[string]any{"empty string": "", "null": nil} {
		t.Run(name, func(t *testing.T) {
			store, book := editFixture(t)
			_, err := audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID, map[string]any{
				"title": "Renamed", "author_name": "", "series_name": "", "narrator": "", "description": "",
				"overrides": map[string]any{
					"title":       map[string]any{"value": "Renamed", "locked": true},
					"author_name": map[string]any{"value": value, "locked": true},
				},
			})
			require.NoError(t, err)
			row, err := store.GetBookByID(book.ID)
			require.NoError(t, err)
			require.Equal(t, "Renamed", row.Title)
			require.ElementsMatch(t, []string{database.FieldKeyTitle}, lockedKeys(t, store, book.ID))
			for field := range historyByField(t, store, book.ID) {
				require.Equal(t, database.FieldKeyTitle, field)
			}
			_, hasAuthorState := fieldStates(t, store, book.ID)[database.FieldKeyAuthorName]
			require.False(t, hasAuthorState, "an author override with nothing to clear left field state")
		})
	}
}

// A BookDetail save of a book that HAS a narrator, description and author:
// the editor sends every field, but only the dirty one (the title, also
// sent as an override) is locked. Locking every non-empty field present in
// the payload froze the whole book against metadata fetches after any edit.
func TestUpdateAudiobook_BookDetailSaveLocksOnlyTheDirtyField(t *testing.T) {
	store, before := richFixture(t)
	_, err := audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), before.ID, map[string]any{
		"title":       "Good Omens (Unabridged)",
		"description": *before.Description,
		"publisher":   "",
		"language":    "",
		"narrator":    *before.Narrator,
		"isbn":        "",
		"author_name": "Terry Pratchett",
		"series_name": "Discworld",
		"overrides": map[string]any{
			"title": map[string]any{"value": "Good Omens (Unabridged)", "locked": true},
		},
	})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{database.FieldKeyTitle}, lockedKeys(t, store, before.ID))
	for field := range historyByField(t, store, before.ID) {
		require.Equal(t, database.FieldKeyTitle, field, "history row for a field the save did not change")
	}
	requireSeriesPosition(t, store, before.ID, 1, "1.5")
	// The editor sends the primary author's name (what GET shows); the
	// co-author must survive it.
	authors, err := store.GetBookAuthors(before.ID)
	require.NoError(t, err)
	require.Len(t, authors, 2, "the co-author was dropped by a save that re-sent the shown author")
}

// A field the save did change, sent at top level without an override, is
// still locked (and recorded).
func TestUpdateAudiobook_ChangedTopLevelFieldIsLocked(t *testing.T) {
	store, before := richFixture(t)
	_, err := audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), before.ID, map[string]any{
		"description": "A new description", "narrator": *before.Narrator,
	})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{database.FieldKeyDescription}, lockedKeys(t, store, before.ID))
	require.NotEmpty(t, historyByField(t, store, before.ID)[database.FieldKeyDescription])
}

// GET shows a junction-only cast as the narrator (junction names joined with
// " & "), and BookDetail re-sends it on every save. That is not a narrator
// edit: the column stays empty, nothing is locked or recorded, and the
// junction is left as it is.
func TestUpdateAudiobook_ResentJunctionNarratorIsNotAnEdit(t *testing.T) {
	store, book := editFixture(t)
	a, err := store.CreateNarrator("Kate Reading")
	require.NoError(t, err)
	b, err := store.CreateNarrator("Michael Kramer")
	require.NoError(t, err)
	require.NoError(t, store.SetBookNarrators(book.ID, []database.BookNarrator{
		{BookID: book.ID, NarratorID: a.ID, Role: "narrator", Position: 0},
		{BookID: book.ID, NarratorID: b.ID, Role: "co-narrator", Position: 1}}))

	body := bookDetailSave("Renamed")
	body["narrator"] = "Kate Reading & Michael Kramer"
	_, err = audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID, body)
	require.NoError(t, err)

	row, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.True(t, row.Narrator == nil || *row.Narrator == "", "the shown junction text was written to the column")
	require.ElementsMatch(t, []string{database.FieldKeyTitle}, lockedKeys(t, store, book.ID))
	for field := range historyByField(t, store, book.ID) {
		require.Equal(t, database.FieldKeyTitle, field)
	}
	require.Equal(t, []string{"Kate Reading", "Michael Kramer"}, narratorNames(t, store, book.ID))
}

// A book hit by the old clear bug (nil SeriesID, stale embedded Series)
// shows the stale name, and BookDetail re-sends it. The save must relink the
// book to that series, not skip the lookup and let the store drop the
// object (which would erase the series on an ordinary save).
func TestUpdateAudiobook_ResentStaleSeriesNameRelinksTheBook(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	series, err := store.CreateSeries("Redshirts", nil)
	require.NoError(t, err)
	book, err := store.CreateBook(&database.Book{Title: "Redshirts", FilePath: "/library/stale.m4b", Format: "m4b", Series: series})
	require.NoError(t, err)

	body := bookDetailSave("Redshirts (Unabridged)")
	body["series_name"] = "Redshirts"
	_, err = audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID, body)
	require.NoError(t, err)

	row, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.NotNil(t, row.SeriesID, "the save did not relink the book to its shown series")
	view, err := audiobooks.NewAudiobookService(store).GetAudiobook(context.Background(), book.ID)
	require.NoError(t, err)
	require.NotNil(t, view.Series, "GET lost the series after an ordinary save")
	require.Equal(t, "Redshirts", view.Series.Name)
}

// authorNamesOf is the book's book_authors names, in order.
func authorNamesOf(t *testing.T, store *database.PebbleStore, id string) []string {
	t.Helper()
	rows, err := store.GetBookAuthors(id)
	require.NoError(t, err)
	var names []string
	for _, r := range rows {
		a, err := store.GetAuthorByID(r.AuthorID)
		require.NoError(t, err)
		require.NotNil(t, a)
		names = append(names, a.Name)
	}
	return names
}

// requireOnlyTitleEdited: the save locked and recorded the title and nothing
// else.
func requireOnlyTitleEdited(t *testing.T, store *database.PebbleStore, id string) {
	t.Helper()
	require.ElementsMatch(t, []string{database.FieldKeyTitle}, lockedKeys(t, store, id))
	for field := range historyByField(t, store, id) {
		require.Equal(t, database.FieldKeyTitle, field, "history row for a field the save did not change")
	}
}

// GET shows a join-only author (no AuthorID) as the join names joined with
// " & ", and BookDetail re-sends that. It used to be read as an author edit:
// the book was relinked to a new author named "Alice Able & Bob Baker", the
// field locked and history written.
func TestUpdateAudiobook_ResentJoinOnlyAuthorIsNotAnEdit(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	a, err := store.CreateAuthor("Alice Able")
	require.NoError(t, err)
	b, err := store.CreateAuthor("Bob Baker")
	require.NoError(t, err)
	book, err := store.CreateBook(&database.Book{Title: "T", FilePath: "/library/j.m4b", Format: "m4b"})
	require.NoError(t, err)
	require.NoError(t, store.SetBookAuthors(book.ID, []database.BookAuthor{
		{BookID: book.ID, AuthorID: a.ID, Role: "author"},
		{BookID: book.ID, AuthorID: b.ID, Role: "co-author", Position: 1}}))

	body := bookDetailSave("Renamed")
	body["author_name"] = "Alice Able & Bob Baker"
	_, err = audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID, body)
	require.NoError(t, err)

	row, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.Nil(t, row.AuthorID, "the book was relinked")
	require.Nil(t, row.Author, "an embedded author was written")
	require.Equal(t, []string{"Alice Able", "Bob Baker"}, authorNamesOf(t, store, book.ID))
	combined, err := store.GetAuthorByName("Alice Able & Bob Baker")
	require.NoError(t, err)
	require.Nil(t, combined, "an author named after the joined names was created")
	requireOnlyTitleEdited(t, store, book.ID)
}

// A dangling AuthorID (no author row) with a join: GET shows the join names.
// Re-sending them is not an edit either.
func TestUpdateAudiobook_ResentAuthorOfADanglingAuthorIDIsNotAnEdit(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	a, err := store.CreateAuthor("Alice Able")
	require.NoError(t, err)
	dangling := 99999
	book, err := store.CreateBook(&database.Book{Title: "T", FilePath: "/library/d.m4b", Format: "m4b", AuthorID: &dangling})
	require.NoError(t, err)
	require.NoError(t, store.SetBookAuthors(book.ID, []database.BookAuthor{{BookID: book.ID, AuthorID: a.ID, Role: "author"}}))

	_, err = audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID, map[string]any{
		"title": "Renamed", "author_name": "Alice Able",
	})
	require.NoError(t, err)
	row, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.NotNil(t, row.AuthorID)
	require.Equal(t, dangling, *row.AuthorID, "the author link was rewritten by a re-sent name")
	require.Nil(t, row.Author, "an embedded author was written for a dangling id")
	requireOnlyTitleEdited(t, store, book.ID)
}

// Review S4b: an override-only author_name / series_name (no top-level key)
// was locked but never applied: resolution read only the top-level key.
func TestUpdateAudiobook_OverrideOnlyAuthorAndSeriesAreApplied(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	a, err := store.CreateAuthor("Alice Able")
	require.NoError(t, err)
	book, err := store.CreateBook(&database.Book{Title: "T", FilePath: "/library/o.m4b", Format: "m4b", AuthorID: &a.ID, Author: a})
	require.NoError(t, err)

	_, err = audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID, map[string]any{
		"overrides": map[string]any{
			"author_name": map[string]any{"value": "Carol Cole", "locked": true},
			"series_name": map[string]any{"value": "New Series", "locked": true},
		},
	})
	require.NoError(t, err)
	view, err := audiobooks.NewAudiobookService(store).GetAudiobook(context.Background(), book.ID)
	require.NoError(t, err)
	row, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	carol, err := store.GetAuthorByName("Carol Cole")
	require.NoError(t, err)
	require.NotNil(t, carol, "the override author was not created")
	require.NotNil(t, row.AuthorID)
	require.Equal(t, carol.ID, *row.AuthorID, "the override author was not applied")
	require.Equal(t, []string{"Carol Cole"}, authorNamesOf(t, store, book.ID))
	require.NotNil(t, row.SeriesID, "the override series was not applied")
	require.NotNil(t, view.Series)
	require.Equal(t, "New Series", view.Series.Name)
	locks := lockedKeys(t, store, book.ID)
	require.Contains(t, locks, database.FieldKeyAuthorName)
	require.Contains(t, locks, database.FieldKeySeriesName)
}

// Whitespace around a re-sent author is still the same author, and a
// whitespace-only narrator for a narrator-less book is a blank no-op.
func TestUpdateAudiobook_WhitespaceOnlyDifferencesAreNotEdits(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	a, err := store.CreateAuthor("Alice Able")
	require.NoError(t, err)
	book, err := store.CreateBook(&database.Book{Title: "T", FilePath: "/library/w.m4b", Format: "m4b", AuthorID: &a.ID, Author: a})
	require.NoError(t, err)
	_, err = audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID, map[string]any{
		"author_name": "  Alice Able ", "narrator": " ",
	})
	require.NoError(t, err)
	require.Empty(t, lockedKeys(t, store, book.ID))
	require.Empty(t, historyByField(t, store, book.ID))
}

// A case-only author edit keeps the book on the same author row (the lookup
// is case-insensitive) instead of creating a new one. Case-only series edits
// are covered below.
func TestUpdateAudiobook_CaseOnlyAuthorEditKeepsTheSameRow(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	a, err := store.CreateAuthor("alice able")
	require.NoError(t, err)
	book, err := store.CreateBook(&database.Book{Title: "T", FilePath: "/library/c.m4b", Format: "m4b", AuthorID: &a.ID, Author: a})
	require.NoError(t, err)
	_, err = audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID, map[string]any{
		"author_name": "Alice Able",
	})
	require.NoError(t, err)
	row, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.Equal(t, a.ID, *row.AuthorID, "a case-only author edit moved the book to another author row")
}

// --- case-only series edits --------------------------------------------------

// caseFixture: a series "the saga" stored with no author, and a book linked
// to it whose author is set (withAuthor) or not.
func caseFixture(t *testing.T, withAuthor bool) (*database.PebbleStore, *database.Book, *database.Series, *int) {
	t.Helper()
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	s, err := store.CreateSeries("the saga", nil)
	require.NoError(t, err)
	b := &database.Book{Title: "T", FilePath: "/library/case.m4b", Format: "m4b", SeriesID: &s.ID, Series: s}
	var authorID *int
	if withAuthor {
		a, err := store.CreateAuthor("Alice Able")
		require.NoError(t, err)
		b.AuthorID, b.Author, authorID = &a.ID, a, &a.ID
	}
	book, err := store.CreateBook(b)
	require.NoError(t, err)
	return store, book, s, authorID
}

func requireSeriesRow(t *testing.T, store *database.PebbleStore, bookID string, wantID int, wantName string) {
	t.Helper()
	row, err := store.GetBookByID(bookID)
	require.NoError(t, err)
	require.NotNil(t, row.SeriesID)
	require.Equal(t, wantID, *row.SeriesID, "the book was moved to another series row")
	s, err := store.GetSeriesByID(wantID)
	require.NoError(t, err)
	require.Equal(t, wantName, s.Name, "series row name")
	view, err := audiobooks.NewAudiobookService(store).GetAudiobook(context.Background(), bookID)
	require.NoError(t, err)
	require.NotNil(t, view.Series)
	require.Equal(t, wantName, view.Series.Name, "GET series name")
}

// A case-only edit on a book that has an author: the author-scoped lookup
// used to miss the author-less row, create a second "The Saga" under the
// book's author and move the book onto it. The link is kept, no row is
// created, and as the only member the row takes the new casing (recorded
// in the book's history).
func TestUpdateAudiobook_CaseOnlySeriesEditOnAnAuthoredBookKeepsTheRow(t *testing.T) {
	store, book, s, authorID := caseFixture(t, true)
	_, err := audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID,
		map[string]any{"series_name": "The Saga"})
	require.NoError(t, err)
	requireSeriesRow(t, store, book.ID, s.ID, "The Saga")
	dup, err := store.GetSeriesByName("The Saga", authorID)
	require.NoError(t, err)
	require.Nil(t, dup, "a duplicate series row was created under the book's author")
	requireOneSeriesRenameRow(t, store, book.ID)
}

// The same on an author-less book: the row (sole member) takes the casing.
func TestUpdateAudiobook_CaseOnlySeriesEditOnAnAuthorlessBookRenamesTheSoleRow(t *testing.T) {
	store, book, s, _ := caseFixture(t, false)
	_, err := audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID,
		map[string]any{"series_name": "The Saga"})
	require.NoError(t, err)
	requireSeriesRow(t, store, book.ID, s.ID, "The Saga")
	requireOneSeriesRenameRow(t, store, book.ID)
}

// seriesWebSave is the editor's shape for a series edit: the top-level key
// and a locked override with the same value.
func seriesWebSave(name string) map[string]any {
	return map[string]any{
		"series_name": name,
		"overrides":   map[string]any{"series_name": map[string]any{"value": name, "locked": true}},
	}
}

// requireOneSeriesRenameRow: a rename leaves exactly one history row (the
// series_name override row, or the manual series row when there is none).
func requireOneSeriesRenameRow(t *testing.T, store *database.PebbleStore, bookID string) {
	t.Helper()
	h := historyByField(t, store, bookID)
	rows := append(append([]database.MetadataChangeRecord{}, h[database.FieldKeySeriesName]...), h[database.HistoryFieldSeries]...)
	require.Len(t, rows, 1, "series rename history rows: %+v", rows)
	require.NotNil(t, rows[0].NewValue)
	require.Equal(t, `"The Saga"`, *rows[0].NewValue)
}

// The editor's shape (top-level + override) on a sole-member series: one
// rename, one history row, locked at the new name.
func TestUpdateAudiobook_CaseOnlySeriesEditViaTheEditorRecordsOneRow(t *testing.T) {
	store, book, s, _ := caseFixture(t, true)
	_, err := audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID, seriesWebSave("The Saga"))
	require.NoError(t, err)
	requireSeriesRow(t, store, book.ID, s.ID, "The Saga")
	requireOneSeriesRenameRow(t, store, book.ID)
	require.Contains(t, lockedKeys(t, store, book.ID), database.FieldKeySeriesName)
}

// A series other books share is not renamed by one book's edit: the link is
// kept and the row keeps its name (and nothing is locked, since nothing
// changed).
func TestUpdateAudiobook_CaseOnlySeriesEditOfASharedSeriesKeepsItsName(t *testing.T) {
	store, book, s, _ := caseFixture(t, true)
	_, err := store.CreateBook(&database.Book{Title: "Other", FilePath: "/library/other.m4b", Format: "m4b", SeriesID: &s.ID, Series: s})
	require.NoError(t, err)
	// The editor's real shape: the override used to be locked and recorded
	// at "The Saga" while the book kept showing "the saga".
	got, err := audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID, seriesWebSave("The Saga"))
	require.NoError(t, err)
	requireSeriesRow(t, store, book.ID, s.ID, "the saga")
	require.NotNil(t, got.Series)
	require.Equal(t, "the saga", got.Series.Name, "response series name")
	require.NotContains(t, lockedKeys(t, store, book.ID), database.FieldKeySeriesName)
	_, hasState := fieldStates(t, store, book.ID)[database.FieldKeySeriesName]
	require.False(t, hasState, "a series_name override the book does not show left field state")
	h := historyByField(t, store, book.ID)
	require.Empty(t, h[database.FieldKeySeriesName])
	require.Empty(t, h[database.HistoryFieldSeries])
}

// A series name matching an existing series of the same author, in other
// casing, resolves to that series instead of creating a duplicate.
func TestUpdateAudiobook_SeriesNameInOtherCasingResolvesToTheAuthorsSeries(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	a, err := store.CreateAuthor("Alice Able")
	require.NoError(t, err)
	s, err := store.CreateSeries("The Saga", &a.ID)
	require.NoError(t, err)
	book, err := store.CreateBook(&database.Book{Title: "T", FilePath: "/library/n.m4b", Format: "m4b", AuthorID: &a.ID, Author: a})
	require.NoError(t, err)
	_, err = audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID,
		map[string]any{"series_name": "the  saga"})
	require.NoError(t, err)
	row, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.NotNil(t, row.SeriesID)
	require.Equal(t, s.ID, *row.SeriesID, "a duplicate series was created instead of resolving the author's series")
}

// A trashed book still counts as a member: the series is not renamed.
func TestUpdateAudiobook_CaseOnlySeriesEditWithATrashedSiblingKeepsItsName(t *testing.T) {
	store, book, s, _ := caseFixture(t, false)
	other, err := store.CreateBook(&database.Book{Title: "Other", FilePath: "/library/other.m4b", Format: "m4b", SeriesID: &s.ID, Series: s})
	require.NoError(t, err)
	trashed := true
	other.MarkedForDeletion = &trashed
	_, err = store.UpdateBook(other.ID, other)
	require.NoError(t, err)
	_, err = audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID, seriesWebSave("The Saga"))
	require.NoError(t, err)
	requireSeriesRow(t, store, book.ID, s.ID, "the saga")
}

// A SeriesID whose row is gone is not "the current series": the edit goes
// through the normal lookup and relinks the book, instead of stamping the
// sent name on a dangling link.
func TestUpdateAudiobook_CaseOnlySeriesEditOnADanglingSeriesIDRelinks(t *testing.T) {
	store, book, s, _ := caseFixture(t, false)
	require.NoError(t, store.DeleteSeries(s.ID))
	_, err := audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID,
		map[string]any{"series_name": "The Saga"})
	require.NoError(t, err)
	row, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.NotNil(t, row.SeriesID)
	require.NotEqual(t, s.ID, *row.SeriesID, "the dangling link was kept")
	linked, err := store.GetSeriesByID(*row.SeriesID)
	require.NoError(t, err)
	require.NotNil(t, linked)
	require.Equal(t, "The Saga", linked.Name)
}

// faultStore forces a ModifyBook or RenameSeriesIf failure.
type faultStore struct {
	*database.PebbleStore
	failModify, failRename bool
}

func (f *faultStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	if f.failModify {
		return nil, errors.New("forced commit failure")
	}
	return f.PebbleStore.ModifyBook(id, fn)
}

func (f *faultStore) RenameSeriesIf(id int, expectCurrent, newName string) error {
	if f.failRename {
		return errors.New("forced rename failure")
	}
	return f.PebbleStore.RenameSeriesIf(id, expectCurrent, newName)
}

// The rename runs only after the book commits: a failed commit leaves the
// series untouched.
func TestUpdateAudiobook_FailedCommitDoesNotRenameTheSeries(t *testing.T) {
	store, book, s, _ := caseFixture(t, false)
	_, err := audiobooks.NewAudiobookUpdateService(&faultStore{PebbleStore: store, failModify: true}).
		UpdateAudiobook(context.Background(), book.ID, seriesWebSave("The Saga"))
	require.Error(t, err)
	got, err := store.GetSeriesByID(s.ID)
	require.NoError(t, err)
	require.Equal(t, "the saga", got.Name, "a failed edit renamed the series")
	require.Empty(t, historyByField(t, store, book.ID))
	require.Empty(t, fieldStates(t, store, book.ID))
}

// A rename that fails after the commit leaves the book showing the row's
// actual name, with no lock or history claiming the new one.
func TestUpdateAudiobook_FailedRenameKeepsTheBookOnTheRowsName(t *testing.T) {
	store, book, s, _ := caseFixture(t, false)
	got, err := audiobooks.NewAudiobookUpdateService(&faultStore{PebbleStore: store, failRename: true}).
		UpdateAudiobook(context.Background(), book.ID, seriesWebSave("The Saga"))
	require.NoError(t, err)
	requireSeriesRow(t, store, book.ID, s.ID, "the saga")
	row, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.NotNil(t, row.Series)
	require.Equal(t, "the saga", row.Series.Name, "the book's embedded series name diverged from the row")
	require.NotNil(t, got.Series)
	require.Equal(t, "the saga", got.Series.Name, "response series name")
	require.NotContains(t, lockedKeys(t, store, book.ID), database.FieldKeySeriesName)
	h := historyByField(t, store, book.ID)
	require.Empty(t, h[database.FieldKeySeriesName])
	require.Empty(t, h[database.HistoryFieldSeries])
}

// An author "" override on a book whose AuthorID has no author row (GET
// shows no author) is a no-op, not a 400.
func TestUpdateAudiobook_AuthorOverrideClearOnADanglingAuthorIDIsANoop(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	dangling := 99999
	book, err := store.CreateBook(&database.Book{Title: "T", FilePath: "/library/d.m4b", Format: "m4b", AuthorID: &dangling})
	require.NoError(t, err)
	_, err = audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID, map[string]any{
		"overrides": map[string]any{"author_name": map[string]any{"value": "", "locked": true}},
	})
	require.NoError(t, err)
	require.Empty(t, lockedKeys(t, store, book.ID))
	require.Empty(t, historyByField(t, store, book.ID))
}

// A case-only author edit keeps the embedded Author name equal to the
// author row's (the row is not renamed: todo.d EDIT-AUTHOR-CASE-RENAME).
func TestUpdateAudiobook_CaseOnlyAuthorEditKeepsTheEmbeddedNameOnTheRow(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	a, err := store.CreateAuthor("alice able")
	require.NoError(t, err)
	book, err := store.CreateBook(&database.Book{Title: "T", FilePath: "/library/c.m4b", Format: "m4b", AuthorID: &a.ID, Author: a})
	require.NoError(t, err)
	_, err = audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID,
		map[string]any{"author_name": "Alice Able"})
	require.NoError(t, err)
	row, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	authorRow, err := store.GetAuthorByID(a.ID)
	require.NoError(t, err)
	require.NotNil(t, row.Author)
	require.Equal(t, authorRow.Name, row.Author.Name, "embedded author name diverged from the author row")
}

// --- invariant: locks and history describe what the book shows -----------

// shownByGET is, per lock key (and per history field name), the text a GET
// of the book shows for that field: the author as enrichBookForResponse
// builds it (primary name, else the join names), the narrator column or the
// junction names, the series name, the position as entered.
func shownByGET(t *testing.T, store *database.PebbleStore, id string) map[string]string {
	t.Helper()
	view, err := audiobooks.NewAudiobookService(store).GetAudiobook(context.Background(), id)
	require.NoError(t, err)
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	out := map[string]string{
		database.FieldKeyTitle:       view.Title,
		database.FieldKeyDescription: str(view.Description),
		database.FieldKeyPublisher:   str(view.Publisher),
		database.FieldKeyLanguage:    str(view.Language),
		database.FieldKeyGenre:       str(view.Genre),
		database.FieldKeyASIN:        str(view.ASIN),
		database.FieldKeyISBN10:      str(view.ISBN10),
		database.FieldKeyISBN13:      str(view.ISBN13),
		"series_position_raw":        str(view.SeriesPositionRaw),
	}
	primary := ""
	if view.Author != nil {
		primary = view.Author.Name
	} else if view.AuthorID != nil {
		if a, aErr := store.GetAuthorByID(*view.AuthorID); aErr == nil && a != nil {
			primary = a.Name
		}
	}
	out[database.FieldKeyAuthorName] = database.ShownCreditName(primary, authorNamesOf(t, store, id))
	out[database.FieldKeyNarrator] = database.ShownCreditName(str(view.Narrator), narratorNames(t, store, id))
	out[database.FieldKeySeriesName] = ""
	if view.Series != nil {
		out[database.FieldKeySeriesName] = view.Series.Name
	}
	out[database.HistoryFieldSeries] = out[database.FieldKeySeriesName]
	if view.AudiobookReleaseYear != nil {
		out[database.FieldKeyAudiobookReleaseYear] = strconv.Itoa(*view.AudiobookReleaseYear)
	}
	seq := ""
	if view.SeriesSequence != nil {
		seq = strconv.Itoa(*view.SeriesSequence)
	}
	// The lock holds the position as entered (raw); the manual history row
	// for series_position holds the int.
	out[database.FieldKeySeriesPosition] = seq
	if raw := str(view.SeriesPositionRaw); raw != "" {
		out[database.FieldKeySeriesPosition] = raw
	}
	out["series_sequence_int"] = seq
	return out
}

// jsonText renders a stored JSON value the way shownValue compares values.
func jsonText(t *testing.T, raw *string) string {
	t.Helper()
	if raw == nil {
		return ""
	}
	var v any
	require.NoError(t, json.Unmarshal([]byte(*raw), &v), "stored value %q", *raw)
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

// requireLocksAndHistoryMatchGET: every locked field's lock value, and every
// history row's new value, is what GET shows for that field.
func requireLocksAndHistoryMatchGET(t *testing.T, store *database.PebbleStore, id, label string) {
	t.Helper()
	requireLocksAndHistoryMatchGETSince(t, store, id, label, time.Time{})
}

// requireLocksAndHistoryMatchGETSince is requireLocksAndHistoryMatchGET for
// the history rows written at or after since (the last edit of a sequence;
// earlier edits' rows describe earlier states).
func requireLocksAndHistoryMatchGETSince(t *testing.T, store *database.PebbleStore, id, label string, since time.Time) {
	t.Helper()
	shown := shownByGET(t, store, id)
	for field, st := range fieldStates(t, store, id) {
		// A stored override value counts as a user override for the lock
		// guards even with the lock flag clear, so it is checked either way.
		if !st.OverrideLocked && st.OverrideValue == nil {
			continue
		}
		want, mapped := shown[field]
		require.True(t, mapped, "%s: overridden field %q has no GET mapping", label, field)
		require.Equal(t, want, jsonText(t, st.OverrideValue), "%s: override on %q disagrees with GET", label, field)
	}
	for field, rows := range historyByField(t, store, id) {
		key := field
		if field == database.HistoryFieldSeriesNo {
			// manual rows carry the int; override rows (same name) the lock value
			key = ""
		}
		for _, r := range rows {
			if r.ChangedAt.Before(since) {
				continue
			}
			k := key
			if k == "" {
				if r.ChangeType == database.ChangeTypeManual {
					k = "series_sequence_int"
				} else {
					k = database.FieldKeySeriesPosition
				}
			}
			want, mapped := shown[k]
			require.True(t, mapped, "%s: history field %q has no GET mapping", label, field)
			require.Equal(t, want, jsonText(t, r.NewValue), "%s: history row %s (%s) disagrees with GET", label, field, r.ChangeType)
		}
	}
}

// invariantFixture: a book with a value in every editable field, its
// author and series rows, and other rows the matrix edits point at.
func invariantFixture(t *testing.T) (*database.PebbleStore, *database.Book) {
	t.Helper()
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	a, err := store.CreateAuthor("Alice Able")
	require.NoError(t, err)
	_, err = store.CreateAuthor("carol cole")
	require.NoError(t, err)
	s, err := store.CreateSeries("the saga", nil)
	require.NoError(t, err)
	_, err = store.CreateSeries("The Expanse", nil)
	require.NoError(t, err)
	str := func(v string) *string { return &v }
	seq, year := 1, 2001
	book, err := store.CreateBook(&database.Book{Title: "T", FilePath: "/library/inv.m4b", Format: "m4b",
		AuthorID: &a.ID, Author: a, SeriesID: &s.ID, Series: s, SeriesSequence: &seq, SeriesPositionRaw: str("1"),
		Narrator: str("Kate Reading"), Description: str("D"), Publisher: str("P"), Language: str("en"),
		Genre: str("G"), ASIN: str("B000000001"), AudiobookReleaseYear: &year})
	require.NoError(t, err)
	require.NoError(t, store.SetBookAuthors(book.ID, []database.BookAuthor{{BookID: book.ID, AuthorID: a.ID, Role: "author"}}))
	return store, book
}

// The editor sends a field as a top-level key AND a locked override. Across
// every editable field, and the resolutions that change what the book shows
// (author and series rows in other casing, a case-only rename, a shared
// series, a decimal position), the lock and the history must describe what
// the book shows -- the bug class that kept reappearing one field at a time.
func TestUpdateAudiobook_LocksAndHistoryAlwaysMatchWhatGETShows(t *testing.T) {
	editor := func(field string, value any) map[string]any {
		return map[string]any{field: value, "overrides": map[string]any{field: map[string]any{"value": value, "locked": true}}}
	}
	cases := map[string]map[string]any{
		"title":                      editor("title", "New Title"),
		"author new":                 editor("author_name", "Bob Baker"),
		"author existing other case": editor("author_name", "Carol Cole"),
		"author case-only":           editor("author_name", "alice able"),
		"narrator":                   editor("narrator", "Wil Wheaton"),
		"series case-only sole":      editor("series_name", "The Saga"),
		"series other":               editor("series_name", "Other Saga"),
		"series existing other case": editor("series_name", "the expanse"),
		"series_position decimal":    editor("series_position", 2.5),
		"series_position string":     editor("series_position", "3"),
		"description":                editor("description", "New D"),
		"publisher":                  editor("publisher", "New P"),
		"language":                   editor("language", "fr"),
		"genre":                      editor("genre", "New G"),
		"asin":                       editor("asin", "B000000002"),
		"year":                       editor("audiobook_release_year", 2005),
		"override disagrees with top-level": {
			"series_name": "Completely Different",
			"overrides":   map[string]any{"series_name": map[string]any{"value": "The Saga", "locked": true}},
		},
		"lock-only with a top-level change": {
			"title": "New Title", "overrides": map[string]any{"title": map[string]any{"locked": true}},
		},
		"lock-only unchanged":          {"overrides": map[string]any{"publisher": map[string]any{"locked": true}}},
		"current value + locked:true":  editor("genre", "G"),
		"changed value + locked:false": {"title": "New", "overrides": map[string]any{"title": map[string]any{"value": "New", "locked": false}}},
		"position equal as a number":   editor("series_position", "1.0"),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			store, book := invariantFixture(t)
			_, err := audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID, body)
			require.NoError(t, err)
			requireLocksAndHistoryMatchGET(t, store, book.ID, name)
		})
	}
	t.Run("series case-only shared", func(t *testing.T) {
		store, book := invariantFixture(t)
		_, err := store.CreateBook(&database.Book{Title: "Other", FilePath: "/library/inv2.m4b", Format: "m4b",
			SeriesID: book.SeriesID, Series: book.Series})
		require.NoError(t, err)
		_, err = audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID, editor("series_name", "The Saga"))
		require.NoError(t, err)
		requireLocksAndHistoryMatchGET(t, store, book.ID, "shared")
		require.Empty(t, lockedKeys(t, store, book.ID), "nothing the book shows changed")
	})
	t.Run("lock-only author, no author sent", func(t *testing.T) {
		store, book := invariantFixture(t)
		svc := audiobooks.NewAudiobookUpdateService(store)
		// After an earlier locked author edit, so the old lock value differs.
		_, err := svc.UpdateAudiobook(context.Background(), book.ID, editor("author_name", "Bob Baker"))
		require.NoError(t, err)
		before := historyByField(t, store, book.ID)
		since := time.Now()
		_, err = svc.UpdateAudiobook(context.Background(), book.ID,
			map[string]any{"overrides": map[string]any{"author_name": map[string]any{"locked": true}}})
		require.NoError(t, err)
		requireLocksAndHistoryMatchGETSince(t, store, book.ID, "lock-only author", since)
		st := fieldStates(t, store, book.ID)[database.FieldKeyAuthorName]
		require.True(t, st.OverrideLocked)
		require.Equal(t, `"Bob Baker"`, *st.OverrideValue)
		require.Equal(t, len(before[database.FieldKeyAuthorName]), len(historyByField(t, store, book.ID)[database.FieldKeyAuthorName]),
			"a lock-only change wrote a history row")
	})
	t.Run("lock-only narrator on a junction-only book", func(t *testing.T) {
		store, book := invariantFixture(t)
		_, err := store.ModifyBook(book.ID, func(b *database.Book) error { b.Narrator = nil; return nil })
		require.NoError(t, err)
		n, err := store.CreateNarrator("Ray Porter")
		require.NoError(t, err)
		require.NoError(t, store.SetBookNarrators(book.ID, []database.BookNarrator{{BookID: book.ID, NarratorID: n.ID, Role: "narrator"}}))
		_, err = audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID,
			map[string]any{"overrides": map[string]any{"narrator": map[string]any{"locked": true}}})
		require.NoError(t, err)
		requireLocksAndHistoryMatchGET(t, store, book.ID, "lock-only narrator")
		st := fieldStates(t, store, book.ID)[database.FieldKeyNarrator]
		require.True(t, st.OverrideLocked)
		require.Equal(t, `"Ray Porter"`, *st.OverrideValue)
	})
	t.Run("author_id + lock-only author_name", func(t *testing.T) {
		store, book := invariantFixture(t)
		carol, err := store.GetAuthorByName("carol cole")
		require.NoError(t, err)
		_, err = audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID, map[string]any{
			"author_id": carol.ID, "overrides": map[string]any{"author_name": map[string]any{"locked": true}}})
		require.NoError(t, err)
		requireLocksAndHistoryMatchGET(t, store, book.ID, "author_id + lock-only")
		st := fieldStates(t, store, book.ID)[database.FieldKeyAuthorName]
		require.True(t, st.OverrideLocked)
		require.Equal(t, `"carol cole"`, *st.OverrideValue)
	})
	t.Run("position cleared with locked:false", func(t *testing.T) {
		store, book := invariantFixture(t)
		svc := audiobooks.NewAudiobookUpdateService(store)
		_, err := svc.UpdateAudiobook(context.Background(), book.ID, editor("series_position", 3))
		require.NoError(t, err)
		since := time.Now()
		_, err = svc.UpdateAudiobook(context.Background(), book.ID,
			map[string]any{"overrides": map[string]any{"series_position": map[string]any{"value": nil, "locked": false}}})
		require.NoError(t, err)
		requireLocksAndHistoryMatchGETSince(t, store, book.ID, "position clear unlocked", since)
		st := fieldStates(t, store, book.ID)[database.FieldKeySeriesPosition]
		require.False(t, st.OverrideLocked)
		require.Nil(t, st.OverrideValue, "a cleared position kept the old override value")
	})
	t.Run("series cleared with locked:false", func(t *testing.T) {
		store, book := invariantFixture(t)
		_, err := audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID, map[string]any{
			"series_name": "", "overrides": map[string]any{"series_name": map[string]any{"value": "", "locked": false}}})
		require.NoError(t, err)
		requireLocksAndHistoryMatchGET(t, store, book.ID, "series clear unlocked")
		st := fieldStates(t, store, book.ID)[database.FieldKeySeriesName]
		require.False(t, st.OverrideLocked)
		require.Nil(t, st.OverrideValue, "a locked:false series clear stored an override value")
	})
	t.Run("dangling SeriesID relinked by name", func(t *testing.T) {
		store, book := invariantFixture(t)
		dangling := 999999
		_, err := store.ModifyBook(book.ID, func(b *database.Book) error { b.SeriesID, b.Series = &dangling, nil; return nil })
		require.NoError(t, err)
		_, err = audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID, editor("series_name", "the saga"))
		require.NoError(t, err)
		requireLocksAndHistoryMatchGET(t, store, book.ID, "dangling relink")
	})
	t.Run("series rename fails", func(t *testing.T) {
		store, book := invariantFixture(t)
		_, err := audiobooks.NewAudiobookUpdateService(&faultStore{PebbleStore: store, failRename: true}).
			UpdateAudiobook(context.Background(), book.ID, editor("series_name", "The Saga"))
		require.NoError(t, err)
		requireLocksAndHistoryMatchGET(t, store, book.ID, "rename fails")
	})
}

// --- probe13 cases ---------------------------------------------------------

// The editor's shape for a case-only author edit: the book keeps the row's
// spelling, so nothing is locked or recorded (the row is not renamed: todo.d
// EDIT-AUTHOR-CASE-RENAME).
func TestUpdateAudiobook_CaseOnlyAuthorEditViaTheEditorLocksNothing(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	a, err := store.CreateAuthor("alice able")
	require.NoError(t, err)
	book, err := store.CreateBook(&database.Book{Title: "T", FilePath: "/library/c.m4b", Format: "m4b", AuthorID: &a.ID, Author: a})
	require.NoError(t, err)
	_, err = audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID, map[string]any{
		"author_name": "Alice Able",
		"overrides":   map[string]any{"author_name": map[string]any{"value": "Alice Able", "locked": true}},
	})
	require.NoError(t, err)
	require.Empty(t, lockedKeys(t, store, book.ID), "a lock claims a spelling the book does not show")
	require.Empty(t, historyByField(t, store, book.ID))
}

// Moving the book to an existing author typed in other casing: the lock and
// history hold the row's spelling, which is what the book shows.
func TestUpdateAudiobook_AuthorChangeToAnExistingRowInOtherCasingLocksTheRowsName(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	b, err := store.CreateAuthor("Bob")
	require.NoError(t, err)
	alice, err := store.CreateAuthor("alice able")
	require.NoError(t, err)
	book, err := store.CreateBook(&database.Book{Title: "T", FilePath: "/library/c.m4b", Format: "m4b", AuthorID: &b.ID, Author: b})
	require.NoError(t, err)
	_, err = audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID, map[string]any{
		"author_name": "Alice Able",
		"overrides":   map[string]any{"author_name": map[string]any{"value": "Alice Able", "locked": true}},
	})
	require.NoError(t, err)
	row, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.Equal(t, alice.ID, *row.AuthorID)
	st := fieldStates(t, store, book.ID)[database.FieldKeyAuthorName]
	require.True(t, st.OverrideLocked)
	require.Equal(t, `"alice able"`, *st.OverrideValue)
	requireLocksAndHistoryMatchGET(t, store, book.ID, "author other case")
}

// A series change to an existing series typed in other casing resolves to it
// (no duplicate) when the lookup's author scope holds it, and locks its name.
// On a book with an author, an author-less series of that name is NOT
// linked: it may be another author's work (see
// AuthorlessSeriesOfTheSameNameIsNotLinked); the book gets its own row.
func TestUpdateAudiobook_SeriesChangeToAnExistingSeriesInOtherCasing(t *testing.T) {
	for _, withAuthor := range []bool{false, true} {
		store, book, _, authorID := caseFixture(t, withAuthor)
		expanse, err := store.CreateSeries("The Expanse", nil)
		require.NoError(t, err)
		_, err = audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID, seriesWebSave("the expanse"))
		require.NoError(t, err)
		row, err := store.GetBookByID(book.ID)
		require.NoError(t, err)
		if withAuthor {
			require.NotEqual(t, expanse.ID, *row.SeriesID, "linked into an author-less series of another scope")
			own, err := store.GetSeriesByName("the expanse", authorID)
			require.NoError(t, err)
			require.NotNil(t, own)
			require.Equal(t, own.ID, *row.SeriesID)
		} else {
			require.Equal(t, expanse.ID, *row.SeriesID, "linked to a new series instead of the existing one")
			st := fieldStates(t, store, book.ID)[database.FieldKeySeriesName]
			require.Equal(t, `"The Expanse"`, *st.OverrideValue)
		}
		requireLocksAndHistoryMatchGET(t, store, book.ID, "series other case")
	}
}

// Author B's book named into a series title that author A's book holds as
// an author-less series ("Collected Stories") gets B's own row: linking it
// into A's series is a wrong link, a second row is only a duplicate.
func TestUpdateAudiobook_AuthorlessSeriesOfTheSameNameIsNotLinked(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	a, err := store.CreateAuthor("Author A")
	require.NoError(t, err)
	b, err := store.CreateAuthor("Author B")
	require.NoError(t, err)
	cs, err := store.CreateSeries("Collected Stories", nil)
	require.NoError(t, err)
	_, err = store.CreateBook(&database.Book{Title: "A's", FilePath: "/library/a.m4b", Format: "m4b", AuthorID: &a.ID, Author: a, SeriesID: &cs.ID, Series: cs})
	require.NoError(t, err)
	bb, err := store.CreateBook(&database.Book{Title: "B's", FilePath: "/library/b.m4b", Format: "m4b", AuthorID: &b.ID, Author: b})
	require.NoError(t, err)
	_, err = audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), bb.ID, seriesWebSave("Collected Stories"))
	require.NoError(t, err)
	row, err := store.GetBookByID(bb.ID)
	require.NoError(t, err)
	require.NotNil(t, row.SeriesID)
	require.NotEqual(t, cs.ID, *row.SeriesID, "author B's book was linked into author A's series")
	own, err := store.GetSeriesByName("Collected Stories", &b.ID)
	require.NoError(t, err)
	require.NotNil(t, own)
	require.Equal(t, own.ID, *row.SeriesID)
}

// Any edit that moves the book to a different series without sending a
// position drops the old series' number.
func TestUpdateAudiobook_SeriesMoveWithoutAPositionClearsIt(t *testing.T) {
	store, book, _, _ := caseFixture(t, false)
	one, raw := 1, "1"
	book.SeriesSequence, book.SeriesPositionRaw = &one, &raw
	_, err := store.UpdateBook(book.ID, book)
	require.NoError(t, err)
	_, err = audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID, map[string]any{"series_name": "Other Saga"})
	require.NoError(t, err)
	row, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.Nil(t, row.SeriesSequence, "the old series' number was kept")
	require.Nil(t, row.SeriesPositionRaw)

	// With a position sent, it is used.
	_, err = audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID,
		map[string]any{"series_name": "Third Saga", "series_position": 4})
	require.NoError(t, err)
	requireSeriesPosition(t, store, book.ID, 4, "4")
}

// The override and the top-level key disagree: the override wins (as for
// every field), and the lock and history describe the result.
func TestUpdateAudiobook_OverrideWinsOverTheTopLevelKey(t *testing.T) {
	store, book, s, _ := caseFixture(t, false)
	_, err := audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID, map[string]any{
		"series_name": "Completely Different",
		"overrides":   map[string]any{"series_name": map[string]any{"value": "The Saga", "locked": true}},
	})
	require.NoError(t, err)
	requireSeriesRow(t, store, book.ID, s.ID, "The Saga")
	requireLocksAndHistoryMatchGET(t, store, book.ID, "override wins")
}

// The book's embedded series name is stale (the row was renamed since): a
// name matching the stale one keeps the link and shows the row's name, and
// the lock says so.
func TestUpdateAudiobook_NameMatchingAStaleShownSeriesShowsTheRowsName(t *testing.T) {
	store, book, s, _ := caseFixture(t, false)
	require.NoError(t, store.RenameSeriesIf(s.ID, "the saga", "New Name"))
	_, err := audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID, seriesWebSave("THE SAGA"))
	require.NoError(t, err)
	requireSeriesRow(t, store, book.ID, s.ID, "New Name")
	requireLocksAndHistoryMatchGET(t, store, book.ID, "stale shown")
}

// A same-request unlock survives a failed rename's rollback.
func TestUpdateAudiobook_FailedRenameKeepsASameRequestUnlock(t *testing.T) {
	store, book, _, _ := caseFixture(t, false)
	svc := audiobooks.NewAudiobookUpdateService(store)
	// Lock series_name first (a real change, then back).
	_, err := svc.UpdateAudiobook(context.Background(), book.ID, seriesWebSave("Other Saga"))
	require.NoError(t, err)
	_, err = svc.UpdateAudiobook(context.Background(), book.ID, seriesWebSave("the saga"))
	require.NoError(t, err)
	require.Contains(t, lockedKeys(t, store, book.ID), database.FieldKeySeriesName, "fixture: series_name locked")

	_, err = audiobooks.NewAudiobookUpdateService(&faultStore{PebbleStore: store, failRename: true}).UpdateAudiobook(
		context.Background(), book.ID, map[string]any{
			"series_name":      "THE SAGA",
			"overrides":        map[string]any{"series_name": map[string]any{"value": "THE SAGA", "locked": true}},
			"unlock_overrides": []any{"series_name"},
		})
	require.NoError(t, err)
	require.False(t, fieldStates(t, store, book.ID)[database.FieldKeySeriesName].OverrideLocked,
		"the same-request unlock was discarded")
}

// --- probe15 cases ---------------------------------------------------------

// A dangling SeriesID shows no series; re-typing the series name relinks the
// book by name, and that is not a move: the position stays.
func TestUpdateAudiobook_DanglingRelinkBySameNameKeepsThePosition(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	foo, err := store.CreateSeries("Foo", nil)
	require.NoError(t, err)
	dangling, seq, raw := 999999, 4, "4"
	book, err := store.CreateBook(&database.Book{Title: "T", FilePath: "/library/d.m4b", Format: "m4b",
		SeriesID: &dangling, SeriesSequence: &seq, SeriesPositionRaw: &raw})
	require.NoError(t, err)
	_, err = audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID, seriesWebSave("Foo"))
	require.NoError(t, err)
	row, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.Equal(t, foo.ID, *row.SeriesID)
	requireSeriesPosition(t, store, book.ID, 4, "4")
}

// A stale embedded Series (nil SeriesID) shows a series; moving to another
// one IS a move, and the old position goes.
func TestUpdateAudiobook_MoveFromAStaleEmbeddedSeriesClearsThePosition(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	alpha, err := store.CreateSeries("Alpha", nil)
	require.NoError(t, err)
	seq, raw := 3, "3"
	book, err := store.CreateBook(&database.Book{Title: "T", FilePath: "/library/s.m4b", Format: "m4b",
		Series: alpha, SeriesSequence: &seq, SeriesPositionRaw: &raw})
	require.NoError(t, err)
	_, err = audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID, seriesWebSave("Beta"))
	require.NoError(t, err)
	row, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.Nil(t, row.SeriesSequence, "the old series' number was kept")
	require.Nil(t, row.SeriesPositionRaw)
}

// Changing the author while re-sending the shown series name keeps the
// series link and its position (the name is the current row).
func TestUpdateAudiobook_AuthorChangeKeepsTheShownSeriesAndPosition(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	a, err := store.CreateAuthor("Author A")
	require.NoError(t, err)
	scoped, err := store.CreateSeries("Foo", &a.ID)
	require.NoError(t, err)
	_, err = store.CreateSeries("Foo", nil)
	require.NoError(t, err)
	seq, raw := 2, "2"
	book, err := store.CreateBook(&database.Book{Title: "T", FilePath: "/library/t.m4b", Format: "m4b", AuthorID: &a.ID, Author: a,
		SeriesID: &scoped.ID, Series: scoped, SeriesSequence: &seq, SeriesPositionRaw: &raw})
	require.NoError(t, err)
	_, err = audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID, map[string]any{
		"author_name": "Author B", "series_name": "Foo",
		"overrides": map[string]any{"author_name": map[string]any{"value": "Author B", "locked": true}}})
	require.NoError(t, err)
	row, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.Equal(t, scoped.ID, *row.SeriesID, "the series link moved")
	requireSeriesPosition(t, store, book.ID, 2, "2")
}

// A lock-only override with a top-level change stores the new (shown) value
// with the lock; a lock-only override, or the current value with
// locked:true, on an unchanged field locks it at the shown value.
func TestUpdateAudiobook_LockOnlyOverridesLockTheShownValue(t *testing.T) {
	store, book := invariantFixture(t)
	svc := audiobooks.NewAudiobookUpdateService(store)
	_, err := svc.UpdateAudiobook(context.Background(), book.ID, map[string]any{
		"title": "New", "overrides": map[string]any{"title": map[string]any{"locked": true}}})
	require.NoError(t, err)
	st := fieldStates(t, store, book.ID)[database.FieldKeyTitle]
	require.True(t, st.OverrideLocked)
	require.Equal(t, `"New"`, *st.OverrideValue)

	_, err = svc.UpdateAudiobook(context.Background(), book.ID, map[string]any{
		"overrides": map[string]any{"publisher": map[string]any{"locked": true}}})
	require.NoError(t, err)
	st = fieldStates(t, store, book.ID)[database.FieldKeyPublisher]
	require.True(t, st.OverrideLocked)
	require.Equal(t, `"P"`, *st.OverrideValue)

	_, err = svc.UpdateAudiobook(context.Background(), book.ID, map[string]any{
		"genre": "G", "overrides": map[string]any{"genre": map[string]any{"value": "G", "locked": true}}})
	require.NoError(t, err)
	require.True(t, fieldStates(t, store, book.ID)[database.FieldKeyGenre].OverrideLocked)
	requireLocksAndHistoryMatchGET(t, store, book.ID, "lock-only")
}

// A changed field sent with locked:false is recorded but stores no override
// value (a stored value would itself count as a user override).
func TestUpdateAudiobook_ChangedFieldWithLockedFalseStoresNoOverride(t *testing.T) {
	store, book := invariantFixture(t)
	_, err := audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID, map[string]any{
		"title": "New", "overrides": map[string]any{"title": map[string]any{"value": "New", "locked": false}}})
	require.NoError(t, err)
	row, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.Equal(t, "New", row.Title)
	st := fieldStates(t, store, book.ID)[database.FieldKeyTitle]
	require.False(t, st.OverrideLocked)
	require.Nil(t, st.OverrideValue, "a locked:false edit stored an override value")
	require.NotContains(t, lockedKeys(t, store, book.ID), database.FieldKeyTitle)
	require.NotEmpty(t, historyByField(t, store, book.ID)[database.FieldKeyTitle], "the edit left no history")
}

// A position re-sent as "2.0" for a stored 2 is not an edit.
func TestUpdateAudiobook_PositionEqualAsANumberIsNotAnEdit(t *testing.T) {
	store, book, _ := seriesFixture(t) // position 1, raw "1"
	_, err := audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID,
		map[string]any{"series_position": "1.0"})
	require.NoError(t, err)
	require.NotContains(t, lockedKeys(t, store, book.ID), database.FieldKeySeriesPosition)
}
