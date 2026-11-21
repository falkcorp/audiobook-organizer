// file: internal/itunes/audiobook_test.go
// version: 1.1.1
// guid: e5f6a7b8-9c0d-1e2f-3a4b-5c6d7e8f9a0b

package itunes

import (
	"encoding/json"
	"testing"
	"time"
)

// TestAuthorStruct tests the Author struct
func TestAuthorStruct(t *testing.T) {
	// Arrange
	author := Author{
		ID:   1,
		Name: "Bruce Wayne",
	}

	// Act & Assert
	if author.ID != 1 {
		t.Errorf("Expected ID to be 1, got %d", author.ID)
	}

	if author.Name != "Bruce Wayne" {
		t.Errorf("Expected Name to be 'Bruce Wayne', got '%s'", author.Name)
	}
}

// TestAuthorJSON tests Author JSON serialization
func TestAuthorJSON(t *testing.T) {
	// Arrange
	author := Author{
		ID:   1,
		Name: "Test Author",
	}

	// Act - Marshal to JSON
	jsonData, err := json.Marshal(author)
	if err != nil {
		t.Fatalf("Failed to marshal author: %v", err)
	}

	// Unmarshal back
	var decoded Author
	if err := json.Unmarshal(jsonData, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal author: %v", err)
	}

	// Assert
	if decoded.ID != author.ID {
		t.Errorf("Expected ID %d, got %d", author.ID, decoded.ID)
	}

	if decoded.Name != author.Name {
		t.Errorf("Expected Name '%s', got '%s'", author.Name, decoded.Name)
	}
}

// TestSeriesStruct tests the Series struct
func TestSeriesStruct(t *testing.T) {
	// Arrange
	authorID := 1
	series := Series{
		ID:       1,
		Name:     "Batman Year One",
		AuthorID: &authorID,
	}

	// Act & Assert
	if series.ID != 1 {
		t.Errorf("Expected ID to be 1, got %d", series.ID)
	}

	if series.Name != "Batman Year One" {
		t.Errorf("Expected Name to be 'Batman Year One', got '%s'", series.Name)
	}

	if series.AuthorID == nil || *series.AuthorID != authorID {
		t.Error("Expected AuthorID to be set to 1")
	}
}

// TestSeriesJSON tests Series JSON serialization
func TestSeriesJSON(t *testing.T) {
	// Arrange
	authorID := 1
	series := Series{
		ID:       1,
		Name:     "Test Series",
		AuthorID: &authorID,
	}

	// Act - Marshal to JSON
	jsonData, err := json.Marshal(series)
	if err != nil {
		t.Fatalf("Failed to marshal series: %v", err)
	}

	// Unmarshal back
	var decoded Series
	if err := json.Unmarshal(jsonData, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal series: %v", err)
	}

	// Assert
	if decoded.ID != series.ID {
		t.Errorf("Expected ID %d, got %d", series.ID, decoded.ID)
	}

	if decoded.Name != series.Name {
		t.Errorf("Expected Name '%s', got '%s'", series.Name, decoded.Name)
	}

	if decoded.AuthorID == nil || *decoded.AuthorID != *series.AuthorID {
		t.Error("AuthorID mismatch in decoded series")
	}
}

// TestAudiobookStruct tests the Audiobook struct
func TestAudiobookStruct(t *testing.T) {
	// Arrange
	authorID := 1
	seriesID := 1
	sequence := 1
	narrator := "Test Narrator"

	audiobook := Audiobook{
		ID:             1,
		Title:          "Test Audiobook",
		AuthorID:       &authorID,
		SeriesID:       &seriesID,
		SeriesSequence: &sequence,
		FilePath:       "/path/to/audiobook.mp3",
		Format:         "mp3",
		Narrator:       &narrator,
	}

	// Act & Assert
	if audiobook.ID != 1 {
		t.Errorf("Expected ID to be 1, got %d", audiobook.ID)
	}

	if audiobook.Title != "Test Audiobook" {
		t.Errorf("Expected Title to be 'Test Audiobook', got '%s'", audiobook.Title)
	}

	if audiobook.AuthorID == nil || *audiobook.AuthorID != authorID {
		t.Error("Expected AuthorID to be set")
	}

	if audiobook.Narrator == nil || *audiobook.Narrator != narrator {
		t.Error("Expected Narrator to be set")
	}
}

// TestAudiobookJSON tests Audiobook JSON serialization
func TestAudiobookJSON(t *testing.T) {
	// Arrange
	authorID := 1
	duration := 3600
	audiobook := Audiobook{
		ID:       1,
		Title:    "Test Book",
		AuthorID: &authorID,
		FilePath: "/test/book.mp3",
		Format:   "mp3",
		Duration: &duration,
	}

	// Act - Marshal to JSON
	jsonData, err := json.Marshal(audiobook)
	if err != nil {
		t.Fatalf("Failed to marshal audiobook: %v", err)
	}

	// Unmarshal back
	var decoded Audiobook
	if err := json.Unmarshal(jsonData, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal audiobook: %v", err)
	}

	// Assert
	if decoded.ID != audiobook.ID {
		t.Errorf("Expected ID %d, got %d", audiobook.ID, decoded.ID)
	}

	if decoded.Title != audiobook.Title {
		t.Errorf("Expected Title '%s', got '%s'", audiobook.Title, decoded.Title)
	}

	if decoded.Duration == nil || *decoded.Duration != duration {
		t.Error("Duration mismatch in decoded audiobook")
	}
}

// TestAudiobookVersionManagement tests version-related fields
func TestAudiobookVersionManagement(t *testing.T) {
	// Arrange
	isPrimary := true
	groupID := "version-group-123"
	notes := "Remastered 2024"

	audiobook := Audiobook{
		ID:               1,
		Title:            "Test Book",
		FilePath:         "/test/book.mp3",
		IsPrimaryVersion: &isPrimary,
		VersionGroupID:   &groupID,
		VersionNotes:     &notes,
	}

	// Act & Assert
	if audiobook.IsPrimaryVersion == nil || !*audiobook.IsPrimaryVersion {
		t.Error("Expected IsPrimaryVersion to be true")
	}

	if audiobook.VersionGroupID == nil || *audiobook.VersionGroupID != groupID {
		t.Error("Expected VersionGroupID to be set")
	}

	if audiobook.VersionNotes == nil || *audiobook.VersionNotes != notes {
		t.Error("Expected VersionNotes to be set")
	}
}

// TestAudiobookListRequest tests the request struct
// TestAudiobookListResponse tests the response struct
// TestAudiobookUpdateRequest tests the update request struct
// TestBatchUpdateRequest tests the batch update request struct
// TestFileSystemItem tests the filesystem item struct
// TestBrowseRequest tests the browse request struct
// TestExclusionRequest tests the exclusion request struct
// TestSystemStatus tests the system status struct
// TestLogEntry tests the log entry struct
// TestAudiobookMediaInfo tests media info fields
func TestAudiobookMediaInfo(t *testing.T) {
	// Arrange
	bitrate := 128
	codec := "AAC"
	sampleRate := 44100
	channels := 2
	quality := "Standard"

	audiobook := Audiobook{
		ID:         1,
		Title:      "Test Book",
		FilePath:   "/test/book.mp3",
		Bitrate:    &bitrate,
		Codec:      &codec,
		SampleRate: &sampleRate,
		Channels:   &channels,
		Quality:    &quality,
	}

	// Act & Assert
	if audiobook.Bitrate == nil || *audiobook.Bitrate != bitrate {
		t.Error("Expected Bitrate to be set")
	}

	if audiobook.Codec == nil || *audiobook.Codec != codec {
		t.Error("Expected Codec to be set")
	}

	if audiobook.SampleRate == nil || *audiobook.SampleRate != sampleRate {
		t.Error("Expected SampleRate to be set")
	}

	if audiobook.Channels == nil || *audiobook.Channels != channels {
		t.Error("Expected Channels to be set")
	}

	if audiobook.Quality == nil || *audiobook.Quality != quality {
		t.Error("Expected Quality to be set")
	}
}

// goldenAudiobookJSON is the byte-exact JSON of goldenAudiobook, captured
// BEFORE Audiobook, Author and Series moved from internal/models to
// internal/itunes (R3-01b). The moved test file keeps this constant untouched.
const goldenAudiobookJSON = `{"id":1,"title":"The Long Halloween","author_id":2,"series_id":3,"series_sequence":4,"file_path":"/library/batman/long-halloween.m4b","original_filename":"long-halloween.m4b","format":"m4b","duration":36000,"narrator":"Alfred Pennyworth","edition":"Unabridged","description":"A year of holiday murders in Gotham.","language":"en","publisher":"Wayne Press","print_year":1997,"audiobook_release_year":2020,"isbn10":"0000000001","isbn13":"9780000000001","itunes_persistent_id":"ABCDEF0123456789","itunes_date_added":"2026-01-02T03:04:05Z","itunes_play_count":5,"itunes_last_played":"2026-02-03T04:05:06Z","itunes_rating":80,"itunes_bookmark":123456,"itunes_import_source":"itunes-library.xml","bitrate_kbps":64,"codec":"AAC","sample_rate_hz":44100,"channels":2,"bit_depth":16,"quality":"64 kbps AAC","is_primary_version":true,"version_group_id":"vg-1","version_notes":"Remastered 2020","author":{"id":2,"name":"Bruce Wayne"},"series":{"id":3,"name":"Batman Year Two","author_id":2,"author":{"id":2,"name":"Bruce Wayne"}}}`

func goldenAudiobook() Audiobook {
	i := func(v int) *int { return &v }
	s := func(v string) *string { return &v }
	added := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	played := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	bookmark := int64(123456)
	primary := true
	return Audiobook{
		ID: 1, Title: "The Long Halloween", AuthorID: i(2), SeriesID: i(3), SeriesSequence: i(4),
		FilePath: "/library/batman/long-halloween.m4b", OriginalFilename: s("long-halloween.m4b"),
		Format: "m4b", Duration: i(36000), Narrator: s("Alfred Pennyworth"), Edition: s("Unabridged"),
		Description: s("A year of holiday murders in Gotham."), Language: s("en"), Publisher: s("Wayne Press"),
		PrintYear: i(1997), AudiobookReleaseYear: i(2020), ISBN10: s("0000000001"), ISBN13: s("9780000000001"),
		ITunesPersistentID: s("ABCDEF0123456789"), ITunesDateAdded: &added, ITunesPlayCount: i(5),
		ITunesLastPlayed: &played, ITunesRating: i(80), ITunesBookmark: &bookmark, ITunesImportSource: s("itunes-library.xml"),
		Bitrate: i(64), Codec: s("AAC"), SampleRate: i(44100), Channels: i(2), BitDepth: i(16), Quality: s("64 kbps AAC"),
		IsPrimaryVersion: &primary, VersionGroupID: s("vg-1"), VersionNotes: s("Remastered 2020"),
		Author: &Author{ID: 2, Name: "Bruce Wayne"},
		Series: &Series{ID: 3, Name: "Batman Year Two", AuthorID: i(2), Author: &Author{ID: 2, Name: "Bruce Wayne"}},
	}
}

// TestAudiobookJSONGoldenBytes pins the wire form of Audiobook (with its
// Author and Series): marshalling gives the golden bytes and the golden bytes
// decode and re-encode unchanged.
func TestAudiobookJSONGoldenBytes(t *testing.T) {
	b, err := json.Marshal(goldenAudiobook())
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != goldenAudiobookJSON {
		t.Fatalf("Audiobook JSON changed\n got: %s\nwant: %s", b, goldenAudiobookJSON)
	}
	var back Audiobook
	if err := json.Unmarshal([]byte(goldenAudiobookJSON), &back); err != nil {
		t.Fatal(err)
	}
	b2, err := json.Marshal(back)
	if err != nil {
		t.Fatal(err)
	}
	if string(b2) != goldenAudiobookJSON {
		t.Fatalf("Audiobook round trip changed the bytes\n got: %s\nwant: %s", b2, goldenAudiobookJSON)
	}
}
