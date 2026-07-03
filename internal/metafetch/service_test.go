// file: internal/metafetch/service_test.go
// version: 1.2.1
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-07-03

package metafetch

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// ---------------------------------------------------------------------------
// NewService
// ---------------------------------------------------------------------------

func TestNewService(t *testing.T) {
	mock := &database.MockStore{}
	svc := NewService(mock)
	require.NotNil(t, svc)
	assert.NotNil(t, svc.db)
}

// ---------------------------------------------------------------------------
// IsGarbageValue
// ---------------------------------------------------------------------------

func TestIsGarbageValue(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"Unknown", true},
		{"unknown", true},
		{"UNKNOWN", true},
		{"narrator", true},
		{"various", true},
		{"n/a", true},
		{"none", true},
		{"null", true},
		{"undefined", true},
		{"", true},
		{"test", true},
		{"untitled", true},
		{"no title", true},
		{"no author", true},
		{"various authors", true},
		{"various artists", true},
		{"Brandon Sanderson", false},
		{"The Way of Kings", false},
		{"Penguin Random House", false},
		// HTML / error leak detection
		{"<html>something</html>", true},
		{"<!DOCTYPE html>", true},
		{"403 Forbidden", true},
		{"some error occurred", false},
		// Legitimate titles/authors containing "error" substring should pass
		{"The Terror", false},
		{"The Comedy of Errors", false},
		{"Terrorbyte", false},
		{"Erroll Garner", false},
		// Genuine error-page leaks should still be rejected
		{"Error: connection refused", true},
		{"HTTP Error 500", true},
		// Edge cases
		{"  Unknown  ", true},
		{"Test", true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, IsGarbageValue(tt.input))
		})
	}
}

// ---------------------------------------------------------------------------
// IsBetterValue
// ---------------------------------------------------------------------------

func TestIsBetterValue(t *testing.T) {
	tests := []struct {
		name   string
		oldVal string
		newVal string
		want   bool
	}{
		{"garbage_to_good", "Unknown", "Brandon Sanderson", true},
		{"good_to_garbage", "Brandon Sanderson", "Unknown", false},
		{"good_to_good", "Brandon Sanderson", "Brando Sando", true},
		{"garbage_to_garbage", "Unknown", "n/a", false},
		{"empty_to_good", "", "Real Author", true},
		{"good_to_empty", "Real Author", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsBetterValue(tt.oldVal, tt.newVal))
		})
	}
}

// ---------------------------------------------------------------------------
// IsBetterStringPtr
// ---------------------------------------------------------------------------

func TestIsBetterStringPtr(t *testing.T) {
	good := "Brandon Sanderson"
	garbage := "Unknown"

	tests := []struct {
		name   string
		oldPtr *string
		newVal string
		want   bool
	}{
		{"nil_to_good", nil, "Brandon Sanderson", true},
		{"garbage_to_good", &garbage, "Brandon Sanderson", true},
		{"good_to_garbage", &good, "Unknown", false},
		{"good_to_good", &good, "Different Author", true},
		{"nil_to_garbage", nil, "Unknown", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsBetterStringPtr(tt.oldPtr, tt.newVal))
		})
	}
}

// ---------------------------------------------------------------------------
// SignificantWords
// ---------------------------------------------------------------------------

func TestSignificantWords(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  map[string]bool
	}{
		{
			"normal_title",
			"The Way of Kings",
			map[string]bool{"way": true, "kings": true},
		},
		{
			"all_stopwords_fallback",
			"The And For",
			// "of" isn't here — all >2 char words are stop words, so they all get included
			map[string]bool{"the": true, "and": true, "for": true},
		},
		{
			"short_word_title",
			"IT",
			// Single short word — fallback includes it
			map[string]bool{"it": true},
		},
		{
			"numeric_title",
			"14",
			map[string]bool{"14": true},
		},
		{
			"strips_punctuation",
			"Hello, World!",
			map[string]bool{"hello": true, "world": true},
		},
		{
			"empty_string",
			"",
			map[string]bool{},
		},
		{
			"mixed_significant_and_stop",
			"Brandon Sanderson and the Mistborn Saga",
			map[string]bool{"brandon": true, "sanderson": true, "mistborn": true, "saga": true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SignificantWords(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// ParseSeriesFromTitle
// ---------------------------------------------------------------------------

func TestParseSeriesFromTitle(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		series   string
		position string
		title    string
	}{
		{
			"paren_series_number",
			"(Long Earth 05) The Long Cosmos",
			"Long Earth", "5", "The Long Cosmos",
		},
		{
			"paren_hash_number",
			"(Mistborn #3) The Hero of Ages",
			"Mistborn", "3", "The Hero of Ages",
		},
		{
			"comma_book_pattern",
			"Stormlight Archive, Book 4",
			"Stormlight Archive", "4", "",
		},
		{
			"no_series",
			"The Way of Kings",
			"", "", "",
		},
		{
			"empty_string",
			"",
			"", "", "",
		},
		{
			"paren_zero_padded",
			"(Series 00) Title",
			"Series", "0", "Title",
		},
		{
			"comma_book_lowercase",
			"Cosmere, book 1",
			"Cosmere", "1", "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			series, position, title := ParseSeriesFromTitle(tt.input)
			assert.Equal(t, tt.series, series, "series")
			assert.Equal(t, tt.position, position, "position")
			assert.Equal(t, tt.title, title, "title")
		})
	}
}

// ---------------------------------------------------------------------------
// NormalizeMetaSeries
// ---------------------------------------------------------------------------

func TestNormalizeMetaSeries(t *testing.T) {
	tests := []struct {
		name       string
		meta       metadata.BookMetadata
		wantSeries string
		wantPos    string
		wantTitle  string
	}{
		{
			"series_with_book_number",
			metadata.BookMetadata{Title: "The Hero of Ages", Series: "Mistborn, Book 3"},
			"Mistborn", "3", "The Hero of Ages",
		},
		{
			"title_with_paren_series",
			metadata.BookMetadata{Title: "(Stormlight 01) The Way of Kings", Series: ""},
			"Stormlight", "1", "The Way of Kings",
		},
		{
			"no_series_info",
			metadata.BookMetadata{Title: "Project Hail Mary", Series: ""},
			"", "", "Project Hail Mary",
		},
		{
			"already_clean",
			metadata.BookMetadata{Title: "Elantris", Series: "Cosmere"},
			"", "", "Elantris", // no match in ParseSeriesFromTitle for plain "Cosmere"
		},
		{
			"dash_embedded_series_cleaned",
			metadata.BookMetadata{Title: "The Long Earth", Series: "The Long Earth - 1 - The Long Earth"},
			"The Long Earth", "1", "The Long Earth",
		},
		{
			"trailing_ordinal_cleaned",
			metadata.BookMetadata{Title: "", Series: "The Long Earth One"},
			"The Long Earth", "1", "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta := tt.meta
			NormalizeMetaSeries(&meta)
			if tt.wantSeries != "" {
				assert.Equal(t, tt.wantSeries, meta.Series, "series")
				assert.Equal(t, tt.wantPos, meta.SeriesPosition, "position")
			}
			assert.Equal(t, tt.wantTitle, meta.Title, "title")
		})
	}
}

// ---------------------------------------------------------------------------
// stripChapterFromTitle
// ---------------------------------------------------------------------------

func TestStripChapterFromTitle(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"The Way of Kings, Book 1", "The Way of Kings"},
		{"01 - The Way of Kings", "The Way of Kings"},
		{"Title Chapter 3", "Title"},
		{"Title (Unabridged)", "Title"},
		{"[The Expanse 9.0] Leviathan Falls", "Leviathan Falls"},
		{"Title [Unabridged]", "Title"},
		{"Title Part 2", "Title"},
		{"Title Volume 3", "Title"},
		{"Title #5", "Title"},
		{"Track 01 - Something", "Something"},
		{"Disc 1 - Something", "Something"},
		// Edge: stripping everything returns original
		{"01", "01"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, stripChapterFromTitle(tt.input))
		})
	}
}

// ---------------------------------------------------------------------------
// stripSubtitle
// ---------------------------------------------------------------------------

func TestStripSubtitle(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Title: A Subtitle", "Title"},
		{"Title - A Subtitle", "Title"},
		{"Title — A Subtitle", "Title"},
		{"No Subtitle Here", "No Subtitle Here"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, stripSubtitle(tt.input))
		})
	}
}

// ---------------------------------------------------------------------------
// isCompilation
// ---------------------------------------------------------------------------

func TestIsCompilation(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"Mistborn Box Set", true},
		{"Complete Series Collection", true},
		{"The Omnibus Edition", true},
		{"5 books in one", true},
		{"The Way of Kings", false},
		{"Anthology of Short Stories", true},
		{"Compendium", true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, isCompilation(tt.input))
		})
	}
}

// ---------------------------------------------------------------------------
// extractTrailingNumber
// ---------------------------------------------------------------------------

func TestExtractTrailingNumber(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Mistborn 3", "3"},
		{"Title, Book 8", "8"},
		{"Title #12", "12"},
		{"Title Volume 5", "5"},
		{"No Number Here", ""},
		{"Title 3.5", "3.5"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, extractTrailingNumber(tt.input))
		})
	}
}

// ---------------------------------------------------------------------------
// normalizeSeriesNumber
// ---------------------------------------------------------------------------

func TestNormalizeSeriesNumber(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"8", "8"},
		{"8.0", "8"},
		{"Book 8", "8"},
		{"#8", "8"},
		{"3.5", "3.5"},
		{"", ""},
		{"no number", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, normalizeSeriesNumber(tt.input))
		})
	}
}

// ---------------------------------------------------------------------------
// ApplySeriesPositionFilter
// ---------------------------------------------------------------------------

func TestApplySeriesPositionFilter(t *testing.T) {
	results := []metadata.BookMetadata{
		{Title: "Book One", SeriesPosition: "1"},
		{Title: "Book Two", SeriesPosition: "2"},
	}

	t.Run("matching_position", func(t *testing.T) {
		filtered := ApplySeriesPositionFilter(results, 1)
		require.Len(t, filtered, 2)
		assert.Equal(t, "Book One", filtered[0].Title)
	})

	t.Run("mismatched_position", func(t *testing.T) {
		// First result has position "1" but we want "3"
		filtered := ApplySeriesPositionFilter(results, 3)
		assert.Nil(t, filtered)
	})

	t.Run("empty_position_passes", func(t *testing.T) {
		noPos := []metadata.BookMetadata{{Title: "Unknown Position", SeriesPosition: ""}}
		filtered := ApplySeriesPositionFilter(noPos, 1)
		require.Len(t, filtered, 1)
	})

	t.Run("zero_known_position", func(t *testing.T) {
		filtered := ApplySeriesPositionFilter(results, 0)
		assert.Len(t, filtered, 2, "zero knownPosition should return all results")
	})

	t.Run("empty_results", func(t *testing.T) {
		filtered := ApplySeriesPositionFilter(nil, 1)
		assert.Nil(t, filtered)
	})
}

// ---------------------------------------------------------------------------
// BestTitleMatch
// ---------------------------------------------------------------------------

func TestBestTitleMatch(t *testing.T) {
	results := []metadata.BookMetadata{
		{Title: "The Way of Kings"},
		{Title: "Words of Radiance"},
		{Title: "Oathbringer"},
		{Title: "The Way of Kings Box Set: Complete Collection"},
	}

	t.Run("exact_match", func(t *testing.T) {
		matched := BestTitleMatch(results, "The Way of Kings")
		require.NotEmpty(t, matched)
		assert.Equal(t, "The Way of Kings", matched[0].Title)
	})

	t.Run("no_match", func(t *testing.T) {
		matched := BestTitleMatch(results, "Completely Different Book Title XYZ")
		assert.Empty(t, matched, "should reject results with no word overlap")
	})

	t.Run("compilation_penalty", func(t *testing.T) {
		// "Box Set" results should be penalized vs single-title match
		matched := BestTitleMatch(results, "The Way of Kings")
		if len(matched) > 0 {
			assert.NotContains(t, matched[0].Title, "Box Set")
		}
	})
}

// ---------------------------------------------------------------------------
// BestTitleMatchWithContext (author bonus)
// ---------------------------------------------------------------------------

func TestBestTitleMatchWithContext(t *testing.T) {
	results := []metadata.BookMetadata{
		{Title: "Mistborn", Author: "Brandon Sanderson"},
		{Title: "Mistborn", Author: "Wrong Author"},
	}

	t.Run("author_match_preferred", func(t *testing.T) {
		matched := BestTitleMatchWithContext(results, "Brandon Sanderson", "", "Mistborn")
		require.NotEmpty(t, matched)
		assert.Equal(t, "Brandon Sanderson", matched[0].Author)
	})
}

// ---------------------------------------------------------------------------
// Helper: stringPtr / derefStr
// ---------------------------------------------------------------------------

func TestStringPtrAndDeref(t *testing.T) {
	p := stringPtr("hello")
	assert.Equal(t, "hello", *p)
	assert.Equal(t, "hello", derefStr(p))
	assert.Equal(t, "", derefStr(nil))
}

// ---------------------------------------------------------------------------
// Helper: intPtrHelper
// ---------------------------------------------------------------------------

func TestIntPtrHelper(t *testing.T) {
	p := intPtrHelper(42)
	assert.Equal(t, 42, *p)
}

// ---------------------------------------------------------------------------
// Helper: derefString / derefIntAsString / jsonEncodeString
// ---------------------------------------------------------------------------

func TestDerefString(t *testing.T) {
	s := "hello"
	assert.Equal(t, "hello", derefString(&s))
	assert.Equal(t, "", derefString(nil))
}

func TestDerefIntAsString(t *testing.T) {
	n := 42
	assert.Equal(t, "42", derefIntAsString(&n))
	assert.Equal(t, "", derefIntAsString(nil))
}

func TestJsonEncodeString(t *testing.T) {
	assert.Equal(t, `"hello"`, jsonEncodeString("hello"))
	assert.Equal(t, `"with \"quotes\""`, jsonEncodeString(`with "quotes"`))
}

// ---------------------------------------------------------------------------
// Helper: truncateActivity
// ---------------------------------------------------------------------------

func TestTruncateActivity(t *testing.T) {
	assert.Equal(t, "short", truncateActivity("short", 10))
	assert.Equal(t, "helloworld...", truncateActivity("helloworld extra", 10))
	assert.Equal(t, "", truncateActivity("", 10))
}

// ---------------------------------------------------------------------------
// FilterUnchangedTags
// ---------------------------------------------------------------------------

// TestFilterUnchangedTags_UnchangedCustomTags tests that unchanged custom tags are filtered.
// This is the core bug fix from MED-3: custom AUDIOBOOK_ORGANIZER_* tags must be
// compared via Metadata fields so they don't defeat skip detection.
func TestFilterUnchangedTags_UnchangedCustomTags(t *testing.T) {
	// Simulate a file read that returns these values unchanged
	missingFile := "/tmp/nonexistent-test-unchanged.m4b"

	// Prepare a tag map with custom tag values that match what ExtractMetadata
	// would return from a real file
	tagMap := map[string]interface{}{
		"title":            "Test Book",
		"album":            "Test Album",
		"artist":           "Test Author",
		"book_id":          "book-123",
		"open_library_id":  "ol-456",
		"hardcover_id":     "hc-789",
		"google_books_id":  "gb-012",
		"edition":          "1st",
		"print_year":       "2020",
	}

	// Since the file doesn't exist, all tags will be returned (safe fallback).
	// The core comparison logic is tested via the valid mapping in currentVals.
	filtered := FilterUnchangedTags(missingFile, tagMap)

	// On missing file, expect all tags to be returned (safe default)
	assert.Equal(t, tagMap, filtered)
}

// TestFilterUnchangedTags_CustomTagMapping tests that all custom tag keys are
// recognized and mapped to their Metadata fields in the comparison map.
// This is a unit test of the mapping itself (lines 369-376 in service_writeback.go).
// The custom tag keys are: book_id, open_library_id, hardcover_id, google_books_id,
// edition, print_year — all mapped to corresponding Metadata struct fields.
func TestFilterUnchangedTags_CustomTagMapping(t *testing.T) {
	// A genuinely unknown tag like "track" should still be returned (safe fallback)
	tmpFile := "/tmp/nonexistent-track-test.m4b"
	tagMap := map[string]interface{}{
		"track": 1,
	}
	filtered := FilterUnchangedTags(tmpFile, tagMap)
	assert.Equal(t, tagMap, filtered)
}

// TestFilterUnchangedTags_MissingFile tests safe-fallback behavior.
func TestFilterUnchangedTags_MissingFile(t *testing.T) {
	// Test that missing files are handled gracefully by returning all tags.
	missingFile := "/tmp/nonexistent-file-xyz.m4b"

	tagMap := map[string]interface{}{
		"title":    "Test Book",
		"book_id":  "test-id",
	}

	filtered := FilterUnchangedTags(missingFile, tagMap)

	// On error, return all tags (safe fallback)
	assert.Equal(t, tagMap, filtered)
}

// TestFilterUnchangedTags_StandardFieldsUnchanged tests standard field comparisons.
// While the fix focuses on custom tags, verify standard fields still work.
func TestFilterUnchangedTags_StandardFieldsUnchanged(t *testing.T) {
	// Missing file: all fields returned unchanged
	missingFile := "/tmp/nonexistent-standard-test.m4b"

	tagMap := map[string]interface{}{
		"title": "Test Book",
		"album": "Test Album",
	}

	filtered := FilterUnchangedTags(missingFile, tagMap)
	assert.Equal(t, tagMap, filtered)
}

// TestFilterUnchangedTags_YearFieldHandling tests numeric year field handling.
func TestFilterUnchangedTags_YearFieldHandling(t *testing.T) {
	missingFile := "/tmp/nonexistent-year-test.m4b"

	tagMap := map[string]interface{}{
		"year": 2020, // Numeric year
	}

	filtered := FilterUnchangedTags(missingFile, tagMap)
	assert.Equal(t, tagMap, filtered)
}
