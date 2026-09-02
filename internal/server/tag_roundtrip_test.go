// file: internal/server/tag_roundtrip_test.go
// version: 1.0.1
// guid: b1c2d3e4-f5a6-7b8c-9d0e-1f2a3b4c5d6e
// last-edited: 2026-09-02

package server

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// TestBuildFullTagMap_AllFields verifies that buildFullTagMap includes every
// DB field we want written to audio files.
func TestBuildFullTagMap_AllFields(t *testing.T) {
	mockStore := &mocks.MockStore{}
	mockStore.On("GetSeriesByID", 42).Return(&database.Series{ID: 42, Name: "Omega Force"}, nil)

	mfs := metafetch.NewService(mockStore)

	book := &database.Book{
		ID:             "BOOK123",
		Title:          "Return of the Archon",
		SeriesID:       new(42),
		SeriesSequence: new(5),
		Language:       new("english"),
		Publisher:      new("Audible Studios"),
		Description:    new("A great book"),
		ISBN10:         new("1234567890"),
		ISBN13:         new("9781234567890"),
		ASIN:           new("B01635BIDS"),
		OpenLibraryID:  new("OL12345M"),
		HardcoverID:    new("HC789"),
		GoogleBooksID:  new("GB456"),
		Edition:        new("First"),
		PrintYear:      new(2015),
		CoverURL:       new("https://example.com/cover.jpg"),
	}

	tagMap := mfs.BuildFullTagMap(book, "Return of the Archon", "Return of the Archon", "Joshua Dalzelle", "Paul Heitsch", 2015, "")

	// Standard tags
	assert.Equal(t, "Return of the Archon", tagMap["title"])
	assert.Equal(t, "Return of the Archon", tagMap["album"])
	assert.Equal(t, "Joshua Dalzelle", tagMap["artist"])
	assert.Equal(t, "Paul Heitsch", tagMap["narrator"])
	assert.Equal(t, 2015, tagMap["year"])
	assert.Equal(t, "Audiobook", tagMap["genre"])

	// Extended metadata
	assert.Equal(t, "english", tagMap["language"])
	assert.Equal(t, "Audible Studios", tagMap["publisher"])
	assert.Equal(t, "A great book", tagMap["description"])
	assert.Equal(t, "1234567890", tagMap["isbn10"])
	assert.Equal(t, "9781234567890", tagMap["isbn13"])
	assert.Equal(t, "B01635BIDS", tagMap["asin"])

	// Series
	assert.Equal(t, "Omega Force", tagMap["series"])
	assert.Equal(t, 5, tagMap["series_index"])

	// External IDs (written as AUDIOBOOK_ORGANIZER_* custom tags)
	assert.Equal(t, "BOOK123", tagMap["book_id"])
	assert.Equal(t, "OL12345M", tagMap["open_library_id"])
	assert.Equal(t, "HC789", tagMap["hardcover_id"])
	assert.Equal(t, "GB456", tagMap["google_books_id"])

	// Edition and print year
	assert.Equal(t, "First", tagMap["edition"])
	assert.Equal(t, "2015", tagMap["print_year"]) // string because custom tag writers expect string

	// Verify all keys are present
	expectedKeys := []string{
		"title", "album", "artist", "narrator", "year", "genre",
		"language", "publisher", "description", "isbn10", "isbn13", "asin",
		"series", "series_index", "book_id", "open_library_id",
		"hardcover_id", "google_books_id", "edition", "print_year",
	}
	for _, key := range expectedKeys {
		assert.Contains(t, tagMap, key, "tagMap missing key: %s", key)
	}
}

// TestBuildFullTagMap_EmptyOptionalFields verifies that nil/empty optional
// fields are NOT included in the tag map (avoids writing empty strings).
func TestBuildFullTagMap_EmptyOptionalFields(t *testing.T) {
	mockStore := &mocks.MockStore{}
	mockStore.On("GetSeriesByID", mock.Anything).Return(nil, nil)

	mfs := metafetch.NewService(mockStore)

	book := &database.Book{
		ID:    "BOOK456",
		Title: "Simple Book",
	}

	tagMap := mfs.BuildFullTagMap(book, "Simple Book", "Simple Book", "", "", 0, "")

	// Should always have title, album, genre, book_id
	assert.Equal(t, "Simple Book", tagMap["title"])
	assert.Equal(t, "Simple Book", tagMap["album"])
	assert.Equal(t, "Audiobook", tagMap["genre"])
	assert.Equal(t, "BOOK456", tagMap["book_id"])

	// Should NOT have empty optional fields
	assert.NotContains(t, tagMap, "narrator")
	assert.NotContains(t, tagMap, "artist")
	assert.NotContains(t, tagMap, "language")
	assert.NotContains(t, tagMap, "publisher")
	assert.NotContains(t, tagMap, "description")
	assert.NotContains(t, tagMap, "isbn10")
	assert.NotContains(t, tagMap, "isbn13")
	assert.NotContains(t, tagMap, "asin")
	assert.NotContains(t, tagMap, "open_library_id")
	assert.NotContains(t, tagMap, "hardcover_id")
	assert.NotContains(t, tagMap, "google_books_id")
	assert.NotContains(t, tagMap, "edition")
	assert.NotContains(t, tagMap, "print_year")
	assert.NotContains(t, tagMap, "year")
}

// TestBuildMetadataProvenance_FileValues verifies that file_value in
// provenance entries is populated from extracted metadata, not nil.
func TestBuildMetadataProvenance_FileValues(t *testing.T) {
	book := &database.Book{
		ID:    "BOOK789",
		Title: "Test Book",
		ASIN:  new("B01635BIDS"),
	}

	meta := metadata.Metadata{
		Title:     "Test Book",
		Artist:    "Author Name",
		Narrator:  "Narrator Name",
		Series:    "Series Name",
		Publisher: "Publisher Name",
		Language:  "english",
		Year:      2020,
		ISBN10:    "1234567890",
		ISBN13:    "9781234567890",
		Genre:     "Audiobook",
		Album:     "Test Book",
		ASIN:      "B01635BIDS",
		Edition:   "Second",
		PrintYear: "2019",
	}

	provenance := metafetch.BuildMetadataProvenance(book, nil, meta, "Author Name", "Series Name", nil)

	// Verify file_value is populated (not nil) for fields we read from file
	fieldsWithFileValues := []string{
		"title", "author_name", "narrator", "series_name",
		"publisher", "language", "isbn10", "isbn13", "genre", "album",
		"asin", "edition", "print_year",
	}
	for _, field := range fieldsWithFileValues {
		entry, ok := provenance[field]
		assert.True(t, ok, "provenance missing field: %s", field)
		assert.NotNil(t, entry.FileValue, "file_value is nil for field: %s", field)
	}

	// Year is special — it's an int, so check explicitly
	yearEntry := provenance["audiobook_release_year"]
	assert.Equal(t, 2020, yearEntry.FileValue)

	// Series index with 0 should result in nil file_value
	siEntry := provenance["series_index"]
	assert.Nil(t, siEntry.FileValue)
}
