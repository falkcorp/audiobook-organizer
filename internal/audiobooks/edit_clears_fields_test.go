// file: internal/audiobooks/edit_clears_fields_test.go
// version: 1.1.0
// guid: 451212a4-52da-4236-9a22-658fce80859a
// last-edited: 2026-10-03

package audiobooks_test

import (
	"context"
	"errors"
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
		bookDetailSave("T"))
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
