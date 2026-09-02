// file: internal/metafetch/field_locks_test.go
// version: 1.0.0
// guid: 552d8ccc-ffc7-41f3-8cef-f00ba6d70608
// last-edited: 2026-09-02

package metafetch

import (
	"errors"
	"reflect"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The conformance suite for the metafetch side of the lock guard. It iterates
// database.UserLockableFields -- the SAME list the writers are checked against
// in internal/audiobooks -- and for every key proves three things through the
// two public apply functions:
//
//  1. the fixture can observe the bug: with nothing locked, applying a
//     different value CHANGES the key's Book column (a fixture whose value
//     matched the book's could never fail);
//  2. with that one key locked, the column is preserved AND the key is
//     reported in the skipped-locked list (silently dropping it would leave
//     op summaries claiming a full apply);
//  3. the lock is per-field: another column still changes, so the guard is
//     not a blanket refusal.
//
// Mutation check: reverting guardedApply to call applyMetadataUnguarded
// directly fails every "locked" subtest; deleting any one case from
// StripLockedFields panics TestStripLockedFieldsCoversVocabulary and fails
// that key's subtest here; returning nil instead of the skipped list fails
// the SkippedLockedFields assertions.

const (
	curatedAuthorID = 7
	fetchedAuthorID = 8
	curatedSeriesID = 21
	fetchedSeriesID = 22
)

// curatedBook is a book the user has fully curated: every lockable column
// holds a real, non-garbage value, so IsBetterValue/IsBetterStringPtr will
// happily replace each one with the fetched value when nothing is locked.
func curatedBook() *database.Book {
	return &database.Book{
		ID:                   "b-locks",
		Title:                "Curated Title",
		AuthorID:             new(curatedAuthorID),
		SeriesID:             new(curatedSeriesID),
		SeriesSequence:       new(1),
		Narrator:             new("Curated Narrator"),
		Publisher:            new("Curated Publisher"),
		Language:             new("en"),
		AudiobookReleaseYear: new(2001),
		ISBN10:               new("1111111111"),
		ISBN13:               new("9781111111111"),
		ASIN:                 new("B000CURATED"),
		Genre:                new("Curated Genre"),
		Description:          new("Curated description."),
	}
}

// fetchedMetaFor returns a provider result that differs from curatedBook on
// EVERY lockable column. meta.ISBN is one string routed by length, so the
// isbn10 key gets a 10-digit ISBN and every other key a 13-digit one.
func fetchedMetaFor(key string) metadata.BookMetadata {
	meta := metadata.BookMetadata{
		Title:                         "Fetched Title",
		Author:                        "Fetched Author",
		Series:                        "Fetched Series",
		SeriesPosition:                "5",
		Narrator:                      "Fetched Narrator",
		Publisher:                     "Fetched Publisher",
		Language:                      "de",
		PublishYear:                   2020,
		PublishYearIsAudiobookRelease: true,
		ISBN:                          "9782222222222",
		ASIN:                          "B000FETCHED",
		Genre:                         "Fetched Genre",
		Description:                   "Fetched description.",
	}
	if key == database.FieldKeyISBN10 {
		meta.ISBN = "2222222222"
	}
	return meta
}

// candidateFor is fetchedMetaFor expressed as the manual-match candidate shape.
// The Audible client's own Name() makes ApplyMetadataCandidate route Year to
// AudiobookReleaseYear, the column the audiobook_release_year key protects
// (metadata.SourceProducesAudiobookReleaseYear compares against exactly that).
func candidateFor(key string) MetadataCandidate {
	m := fetchedMetaFor(key)
	return MetadataCandidate{
		Title:          m.Title,
		Author:         m.Author,
		Narrator:       m.Narrator,
		Series:         m.Series,
		SeriesPosition: m.SeriesPosition,
		Year:           m.PublishYear,
		Publisher:      m.Publisher,
		ISBN:           m.ISBN,
		Description:    m.Description,
		Language:       m.Language,
		Source:         (&metadata.AudibleClient{}).Name(),
	}
}

// candidateCarriesKey reports whether ApplyMetadataCandidate can carry a value
// for the key at all. MetadataCandidate has no Genre, and its ASIN is not
// mapped into the applied metadata, so those two keys can only be exercised
// through ApplyMetadataToBook.
func candidateCarriesKey(key string) bool {
	return key != database.FieldKeyASIN && key != database.FieldKeyGenre
}

// lockStore is a MockStore whose author/series lookups resolve the fetched
// names to DIFFERENT ids than the curated book holds, so AuthorID/SeriesID
// provably move when unlocked. locked is the set of lock rows it reports.
func lockStore(t *testing.T, locked ...string) *database.MockStore {
	t.Helper()
	rows := make([]database.MetadataFieldState, 0, len(locked))
	for _, k := range locked {
		rows = append(rows, database.MetadataFieldState{BookID: "b-locks", Field: k, OverrideLocked: true})
	}
	return &database.MockStore{
		GetMetadataFieldStatesFunc: func(string) ([]database.MetadataFieldState, error) { return rows, nil },
		GetAuthorByNameFunc: func(name string) (*database.Author, error) {
			if name == "Fetched Author" {
				return &database.Author{ID: fetchedAuthorID, Name: name}, nil
			}
			t.Fatalf("unexpected author lookup %q", name)
			return nil, nil
		},
		GetSeriesByNameFunc: func(name string, _ *int) (*database.Series, error) {
			if name == "Fetched Series" {
				return &database.Series{ID: fetchedSeriesID, Name: name}, nil
			}
			t.Fatalf("unexpected series lookup %q", name)
			return nil, nil
		},
	}
}

// columnValue reads Book.<column> by name, dereferencing pointer columns so
// the comparison is on values. A missing column is a test bug, not a pass.
func columnValue(t *testing.T, book *database.Book, column string) any {
	t.Helper()
	v := reflect.ValueOf(book).Elem().FieldByName(column)
	require.Truef(t, v.IsValid(), "Book has no column %q", column)
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		return v.Elem().Interface()
	}
	return v.Interface()
}

// otherColumn names a column that must still change when key is locked, so a
// guard that refuses everything cannot pass as a guard that refuses one field.
func otherColumn(key string) string {
	if key == database.FieldKeyTitle {
		return "Narrator"
	}
	return "Title"
}

func TestStripLockedFieldsCoversVocabulary(t *testing.T) {
	for _, f := range database.UserLockableFields {
		t.Run(f.Key, func(t *testing.T) {
			// Must not reach the default: panic branch.
			stripped, skipped := StripLockedFields(fetchedMetaFor(f.Key), map[string]bool{f.Key: true})
			assert.Equal(t, []string{f.Key}, skipped, "one locked key with a value present is exactly one skip")
			assert.NotEqual(t, fetchedMetaFor(f.Key), stripped, "stripping %s must change the metadata", f.Key)
		})
	}

	t.Run("nothing locked strips nothing", func(t *testing.T) {
		meta := fetchedMetaFor("")
		stripped, skipped := StripLockedFields(meta, nil)
		assert.Equal(t, meta, stripped)
		assert.Empty(t, skipped)
	})

	t.Run("locked key without a value is not a skip", func(t *testing.T) {
		_, skipped := StripLockedFields(metadata.BookMetadata{Title: "Only a title"}, database.AllUserLockableFieldsLocked())
		assert.Equal(t, []string{database.FieldKeyTitle}, skipped, "an absent value was never going to be written; reporting it as skipped would inflate op summaries")
	})

	t.Run("series_name lock also drops the position", func(t *testing.T) {
		stripped, _ := StripLockedFields(fetchedMetaFor(""), map[string]bool{database.FieldKeySeriesName: true})
		assert.Empty(t, stripped.Series)
		assert.Empty(t, stripped.SeriesPosition, "a position belongs to its series; attaching it to the locked series would be wrong data")
	})

	t.Run("print year is not the audiobook release year", func(t *testing.T) {
		meta := fetchedMetaFor("")
		meta.PublishYearIsAudiobookRelease = false
		stripped, skipped := StripLockedFields(meta, map[string]bool{database.FieldKeyAudiobookReleaseYear: true})
		assert.Equal(t, 2020, stripped.PublishYear, "a print-kind year routes to PrintYear, which the lock does not cover")
		assert.Empty(t, skipped)
	})
}

func TestApplyMetadataToBook_HonorsEveryLockKey(t *testing.T) {
	for _, f := range database.UserLockableFields {
		curated := columnValue(t, curatedBook(), f.Column)

		t.Run(f.Key+"/unlocked applies", func(t *testing.T) {
			book := curatedBook()
			skipped, err := NewService(lockStore(t)).ApplyMetadataToBook(book, fetchedMetaFor(f.Key))
			require.NoError(t, err)
			assert.Empty(t, skipped)
			assert.NotEqual(t, curated, columnValue(t, book, f.Column),
				"fixture cannot observe the bug: Book.%s did not change with nothing locked", f.Column)
		})

		t.Run(f.Key+"/locked preserves", func(t *testing.T) {
			book := curatedBook()
			other := columnValue(t, book, otherColumn(f.Key))
			skipped, err := NewService(lockStore(t, f.Key)).ApplyMetadataToBook(book, fetchedMetaFor(f.Key))
			require.NoError(t, err)
			assert.Equal(t, curated, columnValue(t, book, f.Column),
				"lock %s did not protect Book.%s", f.Key, f.Column)
			assert.Equal(t, []string{f.Key}, skipped, "the skipped field must be reported, not silently dropped")
			assert.NotEqual(t, other, columnValue(t, book, otherColumn(f.Key)),
				"locking %s must not block Book.%s", f.Key, otherColumn(f.Key))
		})
	}
}

func TestApplyMetadataToBook_OverrideValueAloneLocks(t *testing.T) {
	// HasUserOverride is OverrideLocked OR OverrideValue != nil: an explicit
	// user value is the user's word even if they never toggled the padlock.
	store := lockStore(t)
	store.GetMetadataFieldStatesFunc = func(string) ([]database.MetadataFieldState, error) {
		return []database.MetadataFieldState{{Field: database.FieldKeyNarrator, OverrideValue: new(`"Curated Narrator"`)}}, nil
	}
	book := curatedBook()
	skipped, err := NewService(store).ApplyMetadataToBook(book, fetchedMetaFor(""))
	require.NoError(t, err)
	assert.Equal(t, "Curated Narrator", *book.Narrator)
	assert.Equal(t, []string{database.FieldKeyNarrator}, skipped)
}

func TestApplyMetadataToBook_LegacyBlobLockIsHonored(t *testing.T) {
	// A book nobody has opened since the per-field rows were introduced still
	// has its lock in the user-preference blob; a background apply must see it.
	store := lockStore(t)
	store.GetUserPreferenceFunc = func(string) (*database.UserPreference, error) {
		return &database.UserPreference{Value: new(`{"author_name":{"override_locked":true}}`)}, nil
	}
	book := curatedBook()
	skipped, err := NewService(store).ApplyMetadataToBook(book, fetchedMetaFor(""))
	require.NoError(t, err)
	assert.Equal(t, curatedAuthorID, *book.AuthorID)
	assert.Equal(t, []string{database.FieldKeyAuthorName}, skipped)
}

func TestApplyMetadataToBook_LockReadErrorRefusesToApply(t *testing.T) {
	store := lockStore(t)
	store.GetMetadataFieldStatesFunc = func(string) ([]database.MetadataFieldState, error) {
		return nil, errors.New("pebble: iterator closed")
	}
	book := curatedBook()
	skipped, err := NewService(store).ApplyMetadataToBook(book, fetchedMetaFor(""))
	require.Error(t, err)
	assert.True(t, errors.Is(err, database.ErrFieldLocksUnavailable), "error %v must wrap ErrFieldLocksUnavailable", err)
	assert.Nil(t, skipped)
	assert.Equal(t, curatedBook(), book, "a failed lock read must leave the book untouched -- 'unknown' is not 'unlocked'")
}

func TestApplyMetadataToBook_NilServiceFailsClosed(t *testing.T) {
	var mfs *Service
	book := curatedBook()
	_, err := mfs.ApplyMetadataToBook(book, fetchedMetaFor(""))
	require.Error(t, err)
	assert.True(t, errors.Is(err, database.ErrFieldLocksUnavailable))
	assert.Equal(t, curatedBook(), book)
}

// candidateStore extends lockStore with the book lookup and a capturing
// UpdateBook so the persisted row -- not the in-memory one -- is what the
// assertions read. That is the row every list view and write-back reads.
func candidateStore(t *testing.T, locked ...string) (*database.MockStore, func() *database.Book) {
	t.Helper()
	store := lockStore(t, locked...)
	var updated *database.Book
	store.GetBookByIDFunc = func(string) (*database.Book, error) { return curatedBook(), nil }
	store.UpdateBookFunc = func(_ string, b *database.Book) (*database.Book, error) {
		cp := *b
		updated = &cp
		return &cp, nil
	}
	return store, func() *database.Book { return updated }
}

func TestApplyMetadataCandidate_HonorsEveryLockKey(t *testing.T) {
	for _, f := range database.UserLockableFields {
		if !candidateCarriesKey(f.Key) {
			continue
		}
		curated := columnValue(t, curatedBook(), f.Column)

		t.Run(f.Key+"/unlocked applies", func(t *testing.T) {
			store, updated := candidateStore(t)
			resp, err := NewService(store).ApplyMetadataCandidate("b-locks", candidateFor(f.Key), nil)
			require.NoError(t, err)
			require.NotNil(t, updated(), "UpdateBook was not called")
			assert.Empty(t, resp.SkippedLockedFields)
			assert.NotEqual(t, curated, columnValue(t, updated(), f.Column),
				"fixture cannot observe the bug: Book.%s did not change with nothing locked", f.Column)
		})

		t.Run(f.Key+"/locked preserves", func(t *testing.T) {
			store, updated := candidateStore(t, f.Key)
			resp, err := NewService(store).ApplyMetadataCandidate("b-locks", candidateFor(f.Key), nil)
			require.NoError(t, err)
			require.NotNil(t, updated(), "UpdateBook was not called")
			assert.Equal(t, curated, columnValue(t, updated(), f.Column),
				"lock %s did not protect the persisted Book.%s", f.Key, f.Column)
			assert.Equal(t, []string{f.Key}, resp.SkippedLockedFields,
				"the response must name the skipped field so callers and op summaries can show it")
			other := columnValue(t, curatedBook(), otherColumn(f.Key))
			assert.NotEqual(t, other, columnValue(t, updated(), otherColumn(f.Key)),
				"locking %s must not block Book.%s", f.Key, otherColumn(f.Key))
		})
	}
}

func TestApplyMetadataCandidate_LockReadErrorRefusesToApply(t *testing.T) {
	store, updated := candidateStore(t)
	store.GetMetadataFieldStatesFunc = func(string) ([]database.MetadataFieldState, error) {
		return nil, errors.New("pebble: iterator closed")
	}
	resp, err := NewService(store).ApplyMetadataCandidate("b-locks", candidateFor(""), nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, database.ErrFieldLocksUnavailable), "error %v must wrap ErrFieldLocksUnavailable", err)
	assert.Nil(t, resp)
	assert.Nil(t, updated(), "UpdateBook must not be called when the locks could not be read")
}

func TestApplyMetadataCandidate_ProvenanceKeepsTheFetchedValue(t *testing.T) {
	// The state row's fetched_value is "what the provider said" -- the UI shows
	// it beside the user's override. Stripping it would blank that panel.
	store, _ := candidateStore(t, database.FieldKeyTitle)
	var upserted []database.MetadataFieldState
	store.UpsertMetadataFieldStateFunc = func(st *database.MetadataFieldState) error {
		upserted = append(upserted, *st)
		return nil
	}
	_, err := NewService(store).ApplyMetadataCandidate("b-locks", candidateFor(""), nil)
	require.NoError(t, err)
	var titleFetched *string
	for _, st := range upserted {
		if st.Field == database.FieldKeyTitle {
			titleFetched = st.FetchedValue
		}
	}
	require.NotNil(t, titleFetched, "no fetched_value recorded for the locked title")
	assert.Contains(t, *titleFetched, "Fetched Title")
}
