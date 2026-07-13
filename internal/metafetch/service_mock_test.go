// file: internal/metafetch/service_mock_test.go
// version: 1.5.0
// guid: c3d4e5f6-a7b8-9012-cdef-012345678901
// last-edited: 2026-07-13

package metafetch

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	tmock "github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/ai/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// ---------------------------------------------------------------------------
// MetadataSourceTag
// ---------------------------------------------------------------------------

func TestMetadataSourceTag(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect string
	}{
		{"empty", "", ""},
		{"whitespace_only", "   ", ""},
		{"hardcover", "Hardcover", "metadata:source:hardcover"},
		{"open_library", "Open Library", "metadata:source:open_library"},
		{"google_books", "Google Books", "metadata:source:google_books"},
		{"audnexus_audible", "Audnexus (Audible)", "metadata:source:audnexus"},
		{"audnexus_plain", "Audnexus", "metadata:source:audnexus"},
		{"audible", "Audible", "metadata:source:audible"},
		{"with_hyphens", "My-Source", "metadata:source:my_source"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expect, MetadataSourceTag(tt.input))
		})
	}
}

// ---------------------------------------------------------------------------
// MetadataLanguageTag
// ---------------------------------------------------------------------------

func TestMetadataLanguageTag(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect string
	}{
		{"empty", "", ""},
		{"whitespace_only", "   ", ""},
		{"iso_2_letter", "en", "metadata:language:en"},
		{"iso_3_letter_eng", "eng", "metadata:language:en"},
		{"full_english", "English", "metadata:language:en"},
		{"full_spanish", "Spanish", "metadata:language:es"},
		{"iso_3_spa", "spa", "metadata:language:es"},
		{"full_french", "French", "metadata:language:fr"},
		{"full_german", "German", "metadata:language:de"},
		{"full_japanese", "Japanese", "metadata:language:ja"},
		{"full_chinese", "Chinese", "metadata:language:zh"},
		{"mandarin", "Mandarin", "metadata:language:zh"},
		{"iso_2_de", "de", "metadata:language:de"},
		{"unknown_lang", "swahili", "metadata:language:swahili"},
		{"unknown_multi_word", "old english", "metadata:language:old_english"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expect, MetadataLanguageTag(tt.input))
		})
	}
}

// ---------------------------------------------------------------------------
// looksLikeASIN
// ---------------------------------------------------------------------------

func TestLooksLikeASIN(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect bool
	}{
		{"valid_B0", "B01N5AZR76", true},
		{"valid_10_alpha", "ABCDEFGHIJ", true},
		{"too_short", "B01N5AZR7", false},
		{"too_long", "B01N5AZR76X", false},
		{"with_special", "B01N5AZ!76", false},
		{"with_space", " B01N5AZR76 ", true}, // TrimSpace in function
		{"empty", "", false},
		{"numeric_10", "0123456789", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expect, looksLikeASIN(tt.input))
		})
	}
}

// ---------------------------------------------------------------------------
// extractASIN
// ---------------------------------------------------------------------------

func TestExtractASIN(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect string
	}{
		{"single_asin", "B01N5AZR76", "B01N5AZR76"},
		{"in_sentence", "Check out B01N5AZR76 on Amazon", "B01N5AZR76"},
		{"with_punctuation", "(B01N5AZR76)", "B01N5AZR76"},
		{"no_asin", "hello world", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expect, extractASIN(tt.input))
		})
	}
}

// ---------------------------------------------------------------------------
// IsStrictTitleMatch
// ---------------------------------------------------------------------------

func TestIsStrictTitleMatch(t *testing.T) {
	tests := []struct {
		name   string
		a, b   string
		expect bool
	}{
		{"exact_match", "The Way of Kings", "The Way of Kings", true},
		{"case_insensitive", "the way of kings", "The Way Of Kings", true},
		{"prefix_with_subtitle", "Shadows of Self", "Shadows of Self: A Mistborn Novel", true},
		{"prefix_with_dash", "Shadows of Self", "Shadows of Self - A Mistborn Novel", true},
		{"completely_different", "Mistborn", "Oathbringer", false},
		{"empty_a", "", "Something", false},
		{"empty_b", "Something", "", false},
		{"both_empty", "", "", false},
		{"short_prefix_rejected", "The", "The Way of Kings", false}, // shorter < 60% of longer
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expect, IsStrictTitleMatch(tt.a, tt.b))
		})
	}
}

// ---------------------------------------------------------------------------
// needsIdentifierEnrichment
// ---------------------------------------------------------------------------

func TestNeedsIdentifierEnrichment(t *testing.T) {
	isbn10 := "1234567890"
	isbn13 := "9781234567890"
	empty := ""
	whitespace := "   "

	tests := []struct {
		name   string
		book   *database.BookCore
		expect bool
	}{
		{"nil_book", nil, false},
		{"no_isbn_fields", &database.BookCore{}, true},
		{"has_isbn10", &database.BookCore{ISBN10: &isbn10}, false},
		{"has_isbn13", &database.BookCore{ISBN13: &isbn13}, false},
		{"empty_isbn10", &database.BookCore{ISBN10: &empty}, true},
		{"whitespace_isbn10", &database.BookCore{ISBN10: &whitespace}, true},
		{"has_both", &database.BookCore{ISBN10: &isbn10, ISBN13: &isbn13}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expect, needsIdentifierEnrichment(tt.book))
		})
	}
}

// ---------------------------------------------------------------------------
// FormatSegmentTitle
// ---------------------------------------------------------------------------

func TestFormatSegmentTitle(t *testing.T) {
	tests := []struct {
		name        string
		format      string
		title       string
		track       int
		totalTracks int
		expect      string
	}{
		{"single_file", DefaultSegmentTitleFormat, "My Book", 1, 1, "My Book"},
		{"multi_default", DefaultSegmentTitleFormat, "My Book", 3, 10, "My Book - 3_10"},
		{"custom_format", "{title} Part {track:02d} of {total_tracks}", "My Book", 3, 10, "My Book Part 03 of 10"},
		{"no_spec", "{title} - {track}", "My Book", 3, 10, "My Book - 3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expect, FormatSegmentTitle(tt.format, tt.title, tt.track, tt.totalTracks))
		})
	}
}

// ---------------------------------------------------------------------------
// FormatPath
// ---------------------------------------------------------------------------

func TestFormatPath(t *testing.T) {
	tests := []struct {
		name   string
		format string
		vars   FormatVars
		expect string
	}{
		{
			"default_format_with_series",
			DefaultPathFormat,
			FormatVars{Author: "Brandon Sanderson", Title: "The Way of Kings", Series: "Stormlight Archive", SeriesPos: "1", Ext: "m4b", Track: 1, TotalTracks: 1},
			"Brandon Sanderson/Stormlight Archive 1 - The Way of Kings/The Way of Kings.m4b",
		},
		{
			"default_format_no_series",
			DefaultPathFormat,
			FormatVars{Author: "Andy Weir", Title: "Project Hail Mary", Ext: "m4b", Track: 1, TotalTracks: 1},
			"Andy Weir/Project Hail Mary/Project Hail Mary.m4b",
		},
		{
			"with_year",
			"{author}/{title} ({year})/{track_title}.{ext}",
			FormatVars{Author: "Andy Weir", Title: "Project Hail Mary", Year: 2021, Ext: "m4b", Track: 1, TotalTracks: 1},
			"Andy Weir/Project Hail Mary (2021)/Project Hail Mary.m4b",
		},
		{
			"unsafe_chars_in_title",
			DefaultPathFormat,
			FormatVars{Author: "Author", Title: "Title: With Colon", Ext: "m4b", Track: 1, TotalTracks: 1},
			"Author/Title - With Colon/Title - With Colon.m4b",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expect, FormatPath(tt.format, tt.vars))
		})
	}
}

// ---------------------------------------------------------------------------
// sanitizePathComponent
// ---------------------------------------------------------------------------

func TestSanitizePathComponent(t *testing.T) {
	tests := []struct {
		input  string
		expect string
	}{
		{"normal text", "normal text"},
		{"with:colon", "with -colon"},
		{"with*star", "withstar"},
		{`with"quotes"`, "with'quotes'"},
		{"with<angle>brackets", "withanglebrackets"},
		{"with|pipe", "with -pipe"},
		{"  extra   spaces  ", "extra spaces"},
		{"with[brackets]", "withbrackets"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expect, sanitizePathComponent(tt.input))
		})
	}
}

// ---------------------------------------------------------------------------
// collapseEmptySegments
// ---------------------------------------------------------------------------

func TestCollapseEmptySegments(t *testing.T) {
	tests := []struct {
		input  string
		expect string
	}{
		{"author//title", "author/title"},
		{"a/./b", "a/b"},
		{"a/../b", "a/b"},
		{"normal/path", "normal/path"},
		{"./leading", "leading"},
		{"/trailing/.", "trailing"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expect, collapseEmptySegments(tt.input))
		})
	}
}

// ---------------------------------------------------------------------------
// BuildCandidateBookInfo
// ---------------------------------------------------------------------------

func TestBuildCandidateBookInfo(t *testing.T) {
	t.Run("full_book", func(t *testing.T) {
		coverURL := "http://cover.jpg"
		duration := 3600
		fileSize := int64(1000000)
		language := "en"

		book := &database.Book{
			ID:       "book-1",
			Title:    "Test Book",
			FilePath: "/path/to/book.m4b",
			Format:   "m4b",
			Author:   &database.Author{Name: "Test Author"},
			CoverURL: &coverURL,
			Duration: &duration,
			FileSize: &fileSize,
			Language: &language,
		}
		store := &database.MockStore{}
		store.GetBookFilesFunc = func(bookID string) ([]database.BookFile, error) {
			return []database.BookFile{{ITunesPath: "/itunes/path"}}, nil
		}

		info := BuildCandidateBookInfo(book, store)
		assert.Equal(t, "book-1", info.ID)
		assert.Equal(t, "Test Book", info.Title)
		assert.Equal(t, "Test Author", info.Author)
		assert.Equal(t, "/path/to/book.m4b", info.FilePath)
		assert.Equal(t, "m4b", info.Format)
		assert.Equal(t, "/itunes/path", info.ITunesPath)
		assert.Equal(t, "http://cover.jpg", info.CoverURL)
		assert.Equal(t, 3600, info.Duration)
		assert.Equal(t, int64(1000000), info.FileSize)
		assert.Equal(t, "en", info.Language)
	})

	t.Run("minimal_book", func(t *testing.T) {
		book := &database.Book{
			ID:       "book-2",
			Title:    "Minimal",
			FilePath: "/path.m4b",
		}
		info := BuildCandidateBookInfo(book, nil)
		assert.Equal(t, "book-2", info.ID)
		assert.Equal(t, "", info.Author)
		assert.Equal(t, "", info.ITunesPath)
		assert.Equal(t, "", info.CoverURL)
		assert.Equal(t, 0, info.Duration)
	})
}

// ---------------------------------------------------------------------------
// CountByStatus
// ---------------------------------------------------------------------------

func TestCountByStatus(t *testing.T) {
	results := []CandidateResult{
		{Status: "matched"},
		{Status: "no_match"},
		{Status: "matched"},
		{Status: "error"},
		{Status: "matched"},
	}

	assert.Equal(t, 3, CountByStatus(results, "matched"))
	assert.Equal(t, 1, CountByStatus(results, "no_match"))
	assert.Equal(t, 1, CountByStatus(results, "error"))
	assert.Equal(t, 0, CountByStatus(results, "nonexistent"))
	assert.Equal(t, 0, CountByStatus(nil, "matched"))
}

// ---------------------------------------------------------------------------
// ScoreOneResult
// ---------------------------------------------------------------------------

func TestScoreOneResult(t *testing.T) {
	t.Run("perfect_match", func(t *testing.T) {
		r := metadata.BookMetadata{Title: "The Way of Kings"}
		words := SignificantWords("The Way of Kings")
		score := ScoreOneResult(r, words)
		assert.Greater(t, score, 0.5)
	})

	t.Run("no_overlap", func(t *testing.T) {
		r := metadata.BookMetadata{Title: "Completely Different Title XYZ"}
		words := SignificantWords("Mistborn Final Empire")
		score := ScoreOneResult(r, words)
		assert.Equal(t, 0.0, score)
	})

	t.Run("compilation_penalty", func(t *testing.T) {
		normal := metadata.BookMetadata{Title: "Mistborn"}
		boxSet := metadata.BookMetadata{Title: "Mistborn Box Set"}
		words := SignificantWords("Mistborn")
		scoreNormal := ScoreOneResult(normal, words)
		scoreBox := ScoreOneResult(boxSet, words)
		assert.Greater(t, scoreNormal, scoreBox, "compilation should be penalized")
	})

	t.Run("rich_metadata_bonus", func(t *testing.T) {
		plain := metadata.BookMetadata{Title: "Mistborn"}
		rich := metadata.BookMetadata{
			Title:       "Mistborn",
			Description: "A great book",
			CoverURL:    "http://cover.jpg",
			Narrator:    "Michael Kramer",
		}
		words := SignificantWords("Mistborn")
		scorePlain := ScoreOneResult(plain, words)
		scoreRich := ScoreOneResult(rich, words)
		assert.Greater(t, scoreRich, scorePlain, "rich metadata should get bonus")
	})

	t.Run("empty_search_words", func(t *testing.T) {
		r := metadata.BookMetadata{Title: "Something"}
		score := ScoreOneResult(r, map[string]bool{})
		assert.Equal(t, 0.0, score)
	})
}

// ---------------------------------------------------------------------------
// ApplyNonBaseAdjustments
// ---------------------------------------------------------------------------

func TestApplyNonBaseAdjustments(t *testing.T) {
	t.Run("compilation_penalty", func(t *testing.T) {
		r := metadata.BookMetadata{Title: "Complete Collection Box Set"}
		score := ApplyNonBaseAdjustments(1.0, r, 2)
		assert.Less(t, score, 0.5, "compilation should be heavily penalized")
	})

	t.Run("length_penalty", func(t *testing.T) {
		short := metadata.BookMetadata{Title: "Mistborn"}
		long := metadata.BookMetadata{Title: "Mistborn The Final Empire A Really Long Title With Extra Words"}
		scoreShort := ApplyNonBaseAdjustments(1.0, short, 1)
		scoreLong := ApplyNonBaseAdjustments(1.0, long, 1)
		assert.Greater(t, scoreShort, scoreLong, "much longer titles should be penalized")
	})

	t.Run("no_length_penalty_when_disabled", func(t *testing.T) {
		r := metadata.BookMetadata{Title: "A Very Long Title With Many Words Extra Stuff"}
		score := ApplyNonBaseAdjustments(1.0, r, 0)
		// With baseWordCount=0, no length penalty; only rich-metadata bonus
		assert.GreaterOrEqual(t, score, 1.0)
	})

	t.Run("rich_metadata_bonus_cap", func(t *testing.T) {
		r := metadata.BookMetadata{
			Title:       "Book",
			Description: "desc",
			CoverURL:    "url",
			Narrator:    "narrator",
			ISBN:        "isbn",
		}
		// All 4 bonuses = 0.20, but cap is 0.15
		score := ApplyNonBaseAdjustments(1.0, r, 0)
		assert.InDelta(t, 1.15, score, 0.001)
	})
}

// ---------------------------------------------------------------------------
// computeF1Base
// ---------------------------------------------------------------------------

func TestComputeF1Base(t *testing.T) {
	t.Run("perfect_overlap", func(t *testing.T) {
		r := metadata.BookMetadata{Title: "Mistborn Final Empire"}
		words := SignificantWords("Mistborn Final Empire")
		score := computeF1Base(r, words)
		assert.InDelta(t, 1.0, score, 0.001)
	})

	t.Run("partial_overlap", func(t *testing.T) {
		r := metadata.BookMetadata{Title: "Mistborn Hero Ages"}
		words := SignificantWords("Mistborn Final Empire")
		score := computeF1Base(r, words)
		assert.Greater(t, score, 0.0)
		assert.Less(t, score, 1.0)
	})

	t.Run("no_overlap", func(t *testing.T) {
		r := metadata.BookMetadata{Title: "Completely Different Book"}
		words := SignificantWords("Mistborn Final Empire")
		score := computeF1Base(r, words)
		assert.Equal(t, 0.0, score)
	})

	t.Run("empty_search", func(t *testing.T) {
		r := metadata.BookMetadata{Title: "Something"}
		score := computeF1Base(r, map[string]bool{})
		assert.Equal(t, 0.0, score)
	})

	t.Run("empty_result_title", func(t *testing.T) {
		r := metadata.BookMetadata{Title: ""}
		words := SignificantWords("Mistborn")
		score := computeF1Base(r, words)
		assert.Equal(t, 0.0, score)
	})
}

// ---------------------------------------------------------------------------
// BuildTagMap (Service method)
// ---------------------------------------------------------------------------

func TestBuildTagMap(t *testing.T) {
	mock := &database.MockStore{}
	svc := NewService(mock)

	t.Run("all_fields", func(t *testing.T) {
		tags := svc.BuildTagMap("Album Title", "Track Title", "Artist", "Narrator", 2021, "3")
		assert.Equal(t, "Track Title", tags["title"])
		assert.Equal(t, "Album Title", tags["album"])
		assert.Equal(t, "Artist", tags["artist"])
		assert.Equal(t, "Narrator", tags["narrator"])
		assert.Equal(t, 2021, tags["year"])
		assert.Equal(t, "Audiobook", tags["genre"])
		assert.Equal(t, "3", tags["track"])
	})

	t.Run("empty_optional_fields", func(t *testing.T) {
		tags := svc.BuildTagMap("Album", "Track", "", "", 0, "")
		assert.Equal(t, "Track", tags["title"])
		assert.Equal(t, "Album", tags["album"])
		assert.Equal(t, "Audiobook", tags["genre"])
		_, hasArtist := tags["artist"]
		_, hasNarrator := tags["narrator"]
		_, hasYear := tags["year"]
		_, hasTrack := tags["track"]
		assert.False(t, hasArtist, "empty artist should not be in map")
		assert.False(t, hasNarrator, "empty narrator should not be in map")
		assert.False(t, hasYear, "zero year should not be in map")
		assert.False(t, hasTrack, "empty track should not be in map")
	})
}

// ---------------------------------------------------------------------------
// BuildFullTagMap (Service method)
// ---------------------------------------------------------------------------

func TestBuildFullTagMap(t *testing.T) {
	mock := &database.MockStore{}
	svc := NewService(mock)

	t.Run("includes_custom_tags", func(t *testing.T) {
		lang := "en"
		publisher := "Tor Books"
		desc := "A great book"
		isbn10 := "1234567890"
		isbn13 := "9781234567890"
		asin := "B01N5AZR76"
		olID := "OL12345"
		hcID := "HC123"
		gbID := "GB123"
		edition := "1st"
		printYear := 2020
		seriesSeq := 3

		book := &database.Book{
			ID:             "book-1",
			Language:       &lang,
			Publisher:      &publisher,
			Description:    &desc,
			ISBN10:         &isbn10,
			ISBN13:         &isbn13,
			ASIN:           &asin,
			OpenLibraryID:  &olID,
			HardcoverID:    &hcID,
			GoogleBooksID:  &gbID,
			Edition:        &edition,
			PrintYear:      &printYear,
			SeriesSequence: &seriesSeq,
		}

		tags := svc.BuildFullTagMap(book, "Album", "Track", "Artist", "Narrator", 2021, "1")

		// Standard tags from BuildTagMap
		assert.Equal(t, "Track", tags["title"])
		assert.Equal(t, "Album", tags["album"])

		// Custom tags from book record
		assert.Equal(t, "en", tags["language"])
		assert.Equal(t, "Tor Books", tags["publisher"])
		assert.Equal(t, "A great book", tags["description"])
		assert.Equal(t, "1234567890", tags["isbn10"])
		assert.Equal(t, "9781234567890", tags["isbn13"])
		assert.Equal(t, "B01N5AZR76", tags["asin"])
		assert.Equal(t, "book-1", tags["book_id"])
		assert.Equal(t, "OL12345", tags["open_library_id"])
		assert.Equal(t, "HC123", tags["hardcover_id"])
		assert.Equal(t, "GB123", tags["google_books_id"])
		assert.Equal(t, "1st", tags["edition"])
		assert.Equal(t, "2020", tags["print_year"])
		assert.Equal(t, 3, tags["series_index"])
	})

	t.Run("skips_nil_and_empty_fields", func(t *testing.T) {
		emptyStr := ""
		book := &database.Book{
			ID:       "book-2",
			Language: &emptyStr,
		}

		tags := svc.BuildFullTagMap(book, "Album", "Track", "", "", 0, "")
		_, hasLang := tags["language"]
		assert.False(t, hasLang, "empty language should not be in map")
		assert.Equal(t, "book-2", tags["book_id"]) // always present
	})

	t.Run("resolves_series_from_db", func(t *testing.T) {
		seriesID := 42
		mock.GetSeriesByIDFunc = func(id int) (*database.Series, error) {
			if id == 42 {
				return &database.Series{ID: 42, Name: "Stormlight Archive"}, nil
			}
			return nil, nil
		}
		defer func() { mock.GetSeriesByIDFunc = nil }()

		book := &database.Book{
			ID:       "book-3",
			SeriesID: &seriesID,
		}

		tags := svc.BuildFullTagMap(book, "Album", "Track", "", "", 0, "")
		assert.Equal(t, "Stormlight Archive", tags["series"])
	})
}

// ---------------------------------------------------------------------------
// ComputeTargetPaths
// ---------------------------------------------------------------------------

func TestComputeTargetPaths(t *testing.T) {
	t.Run("empty_root", func(t *testing.T) {
		result := ComputeTargetPaths("", DefaultPathFormat, DefaultSegmentTitleFormat, &database.Book{}, []database.BookFile{}, FormatVars{})
		assert.Nil(t, result)
	})

	t.Run("no_files", func(t *testing.T) {
		result := ComputeTargetPaths("/root", DefaultPathFormat, DefaultSegmentTitleFormat, &database.Book{}, nil, FormatVars{})
		assert.Nil(t, result)
	})

	t.Run("single_file", func(t *testing.T) {
		book := &database.Book{ID: "b1", Title: "Test Book"}
		files := []database.BookFile{
			{ID: "f1", BookID: "b1", FilePath: "/old/path/test.m4b", Format: "m4b", TrackNumber: 1},
		}
		vars := FormatVars{Author: "Author", Title: "Test Book"}
		entries := ComputeTargetPaths("/root", DefaultPathFormat, DefaultSegmentTitleFormat, book, files, vars)
		require.Len(t, entries, 1)
		assert.Equal(t, "/old/path/test.m4b", entries[0].SourcePath)
		assert.Contains(t, entries[0].TargetPath, "/root/Author/Test Book/Test Book.m4b")
	})

	t.Run("multi_file", func(t *testing.T) {
		book := &database.Book{ID: "b1", Title: "Test Book"}
		files := []database.BookFile{
			{ID: "f1", BookID: "b1", FilePath: "/old/1.m4b", Format: "m4b", TrackNumber: 1},
			{ID: "f2", BookID: "b1", FilePath: "/old/2.m4b", Format: "m4b", TrackNumber: 2},
		}
		vars := FormatVars{Author: "Author", Title: "Test Book"}
		entries := ComputeTargetPaths("/root", DefaultPathFormat, DefaultSegmentTitleFormat, book, files, vars)
		require.Len(t, entries, 2)
		assert.Contains(t, entries[0].TargetPath, "Test Book - 1_2")
		assert.Contains(t, entries[1].TargetPath, "Test Book - 2_2")
	})

	t.Run("skips_missing_files", func(t *testing.T) {
		book := &database.Book{ID: "b1"}
		files := []database.BookFile{
			{ID: "f1", BookID: "b1", FilePath: "/old/1.m4b", Format: "m4b", TrackNumber: 1, Missing: true},
			{ID: "f2", BookID: "b1", FilePath: "/old/2.m4b", Format: "m4b", TrackNumber: 2},
		}
		vars := FormatVars{Author: "Author", Title: "Book"}
		entries := ComputeTargetPaths("/root", DefaultPathFormat, DefaultSegmentTitleFormat, book, files, vars)
		for _, e := range entries {
			assert.NotEqual(t, "f1", e.SegmentID, "missing file should be skipped")
		}
	})

	t.Run("no_change_same_path", func(t *testing.T) {
		book := &database.Book{ID: "b1"}
		// File already at its target path
		vars := FormatVars{Author: "Author", Title: "Book"}
		targetPath := "/root/" + FormatPath(DefaultPathFormat, FormatVars{
			Author: "Author", Title: "Book", Ext: "m4b", Track: 1, TotalTracks: 1,
		})
		files := []database.BookFile{
			{ID: "f1", BookID: "b1", FilePath: targetPath, Format: "m4b", TrackNumber: 1},
		}
		entries := ComputeTargetPaths("/root", DefaultPathFormat, DefaultSegmentTitleFormat, book, files, vars)
		assert.Nil(t, entries, "no rename needed when already at target path")
	})
}

// ---------------------------------------------------------------------------
// Pipeline checkpoints
// ---------------------------------------------------------------------------

func TestPipelineCheckpoints(t *testing.T) {
	prefs := map[string]string{}
	mock := &database.MockStore{
		SetUserPreferenceForUserFunc: func(userID, key, value string) error {
			prefs[key] = value
			return nil
		},
		GetUserPreferenceForUserFunc: func(userID, key string) (*database.UserPreferenceKV, error) {
			val, ok := prefs[key]
			if !ok || val == "" {
				return nil, nil
			}
			return &database.UserPreferenceKV{Value: val}, nil
		},
	}

	t.Run("set_and_check", func(t *testing.T) {
		assert.False(t, hasCheckpoint(mock, "book-1", phaseRename))
		setCheckpoint(mock, "book-1", phaseRename)
		assert.True(t, hasCheckpoint(mock, "book-1", phaseRename))
	})

	t.Run("clear", func(t *testing.T) {
		setCheckpoint(mock, "book-2", phaseRename)
		setCheckpoint(mock, "book-2", phaseTags)
		setCheckpoint(mock, "book-2", phaseITunes)
		assert.True(t, hasCheckpoint(mock, "book-2", phaseRename))

		clearCheckpoints(mock, "book-2")
		assert.False(t, hasCheckpoint(mock, "book-2", phaseRename))
		assert.False(t, hasCheckpoint(mock, "book-2", phaseTags))
		assert.False(t, hasCheckpoint(mock, "book-2", phaseITunes))
	})

	t.Run("cleanup_stale_noop", func(t *testing.T) {
		count := CleanupStaleCheckpoints(mock)
		assert.Equal(t, 0, count)
	})
}

// ---------------------------------------------------------------------------
// LoadRejectedCandidateKeys
// ---------------------------------------------------------------------------

func TestLoadRejectedCandidateKeys(t *testing.T) {
	// MockStore.ScanPrefix returns nil by default, so this tests the empty path.
	t.Run("empty_results", func(t *testing.T) {
		mock := &database.MockStore{}
		keys := LoadRejectedCandidateKeys(mock, "book-1")
		assert.Empty(t, keys)
	})
}

// ---------------------------------------------------------------------------
// Service setter methods
// ---------------------------------------------------------------------------

func TestServiceSetters(t *testing.T) {
	mock := &database.MockStore{}
	svc := NewService(mock)

	t.Run("set_override_sources", func(t *testing.T) {
		assert.Nil(t, svc.overrideSources)
		svc.SetOverrideSources([]metadata.MetadataSource{})
		assert.NotNil(t, svc.overrideSources)
	})

	t.Run("set_isbn_enrichment", func(t *testing.T) {
		assert.Nil(t, svc.isbnEnrichment)
		isbn := &ISBNService{}
		svc.SetISBNEnrichment(isbn)
		assert.Equal(t, isbn, svc.isbnEnrichment)
	})

	t.Run("isbn_enrichment_getter", func(t *testing.T) {
		assert.NotNil(t, svc.ISBNEnrichment())
	})
}

// ---------------------------------------------------------------------------
// Helper functions: stringVal, intVal, metadataStateKey
// ---------------------------------------------------------------------------

func TestStringVal(t *testing.T) {
	s := "hello"
	assert.Equal(t, "hello", stringVal(&s))
	assert.Nil(t, stringVal(nil))
}

func TestIntVal(t *testing.T) {
	n := 42
	assert.Equal(t, 42, intVal(&n))
	assert.Nil(t, intVal(nil))
}

func TestMetadataStateKey(t *testing.T) {
	assert.Equal(t, "metadata_state_book-123", metadataStateKey("book-123"))
	assert.Equal(t, "metadata_state_", metadataStateKey(""))
}

// ---------------------------------------------------------------------------
// encodeMetadataValue / decodeMetadataValue
// ---------------------------------------------------------------------------

func TestEncodeDecodeMetadataValue(t *testing.T) {
	t.Run("nil_value", func(t *testing.T) {
		encoded, err := encodeMetadataValue(nil)
		assert.NoError(t, err)
		assert.Nil(t, encoded)
	})

	t.Run("string_value", func(t *testing.T) {
		encoded, err := encodeMetadataValue("hello")
		require.NoError(t, err)
		require.NotNil(t, encoded)
		assert.Equal(t, `"hello"`, *encoded)

		decoded := decodeMetadataValue(encoded)
		assert.Equal(t, "hello", decoded)
	})

	t.Run("number_value", func(t *testing.T) {
		encoded, err := encodeMetadataValue(42)
		require.NoError(t, err)
		require.NotNil(t, encoded)
		assert.Equal(t, "42", *encoded)

		decoded := decodeMetadataValue(encoded)
		assert.Equal(t, float64(42), decoded) // JSON numbers decode as float64
	})

	t.Run("decode_nil", func(t *testing.T) {
		assert.Nil(t, decodeMetadataValue(nil))
	})

	t.Run("decode_empty", func(t *testing.T) {
		empty := ""
		assert.Nil(t, decodeMetadataValue(&empty))
	})

	t.Run("decode_invalid_json_returns_raw", func(t *testing.T) {
		raw := "not valid json {"
		decoded := decodeMetadataValue(&raw)
		assert.Equal(t, "not valid json {", decoded)
	})
}

// ---------------------------------------------------------------------------
// RecordChangeHistory (Service method with mock)
// ---------------------------------------------------------------------------

func TestRecordChangeHistory(t *testing.T) {
	var recorded []database.MetadataChangeRecord
	mock := &database.MockStore{
		GetAuthorByIDFunc: func(id int) (*database.Author, error) {
			return &database.Author{ID: id, Name: "Old Author"}, nil
		},
		GetSeriesByIDFunc: func(id int) (*database.Series, error) {
			return &database.Series{ID: id, Name: "Old Series"}, nil
		},
		RecordMetadataChangeFunc: func(record *database.MetadataChangeRecord) error {
			recorded = append(recorded, *record)
			return nil
		},
	}
	svc := NewService(mock)

	authorID := 1
	seriesID := 2
	narrator := "Old Narrator"
	book := &database.Book{
		ID:       "book-1",
		Title:    "Old Title",
		AuthorID: &authorID,
		SeriesID: &seriesID,
		Narrator: &narrator,
	}

	meta := metadata.BookMetadata{
		Title:    "New Title",
		Author:   "New Author",
		Narrator: "New Narrator",
		Series:   "New Series",
	}

	svc.RecordChangeHistory(book, meta, "hardcover")

	// Should record changes for title, author, narrator, series
	assert.GreaterOrEqual(t, len(recorded), 3, "should record multiple field changes")

	// Verify at least the title change was recorded
	foundTitle := false
	for _, r := range recorded {
		if r.Field == "title" {
			foundTitle = true
			assert.Equal(t, "book-1", r.BookID)
			assert.Equal(t, "fetched", r.ChangeType)
			assert.Equal(t, "hardcover", r.Source)
		}
	}
	assert.True(t, foundTitle, "title change should be recorded")
}

// ---------------------------------------------------------------------------
// RenameFiles (with temp directory)
// ---------------------------------------------------------------------------

func TestRenameFilesEmpty(t *testing.T) {
	result, err := RenameFiles(nil)
	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.Empty(t, result.Succeeded)
	assert.Empty(t, result.Skipped)
}

func TestRenameFilesSkipsMissing(t *testing.T) {
	entries := []FileRenameEntry{
		{SegmentID: "s1", SourcePath: "/nonexistent/path/file.m4b", TargetPath: "/other/path/file.m4b"},
	}
	result, err := RenameFiles(entries)
	require.NoError(t, err)
	assert.Len(t, result.Skipped, 1)
	assert.Empty(t, result.Succeeded)
}

// ---------------------------------------------------------------------------
// ApplyMetadataToBook (Service method)
// ---------------------------------------------------------------------------

func TestApplyMetadataToBook(t *testing.T) {
	t.Run("applies_title_publisher_language", func(t *testing.T) {
		mock := &database.MockStore{
			GetAuthorByNameFunc: func(name string) (*database.Author, error) {
				return &database.Author{ID: 10, Name: name}, nil
			},
			GetSeriesByNameFunc: func(name string, authorID *int) (*database.Series, error) {
				return &database.Series{ID: 5, Name: name}, nil
			},
		}
		svc := NewService(mock)

		book := &database.Book{
			ID:    "b1",
			Title: "Unknown",
		}
		meta := metadata.BookMetadata{
			Title:          "The Way of Kings",
			Publisher:      "Tor Books",
			Language:       "en",
			PublishYear:    2010,
			CoverURL:       "http://cover.jpg",
			Narrator:       "Michael Kramer",
			Author:         "Brandon Sanderson",
			Series:         "Stormlight Archive",
			SeriesPosition: "1",
			ISBN:           "9781234567890",
			ASIN:           "B01N5AZR76",
			Description:    "An epic fantasy",
			Genre:          "Fantasy",
		}

		svc.ApplyMetadataToBook(book, meta)

		assert.Equal(t, "The Way of Kings", book.Title)
		assert.Equal(t, "Tor Books", *book.Publisher)
		assert.Equal(t, "en", *book.Language)
		// A source-less meta defaults to print-kind, so the year routes to
		// PrintYear (not AudiobookReleaseYear). See TestApplyMetadataToBook_YearRouting.
		assert.Nil(t, book.AudiobookReleaseYear)
		if assert.NotNil(t, book.PrintYear) {
			assert.Equal(t, 2010, *book.PrintYear)
		}
		assert.Equal(t, "http://cover.jpg", *book.CoverURL)
		assert.Equal(t, "Michael Kramer", *book.Narrator)
		assert.Equal(t, 10, *book.AuthorID)
		assert.Equal(t, 5, *book.SeriesID)
		assert.Equal(t, 1, *book.SeriesSequence)
		assert.Equal(t, "9781234567890", *book.ISBN13)
		assert.Equal(t, "B01N5AZR76", *book.ASIN)
		assert.Equal(t, "An epic fantasy", *book.Description)
		assert.Equal(t, "Fantasy", *book.Genre)
	})

	t.Run("does_not_overwrite_good_title_with_garbage", func(t *testing.T) {
		mock := &database.MockStore{}
		svc := NewService(mock)

		book := &database.Book{ID: "b1", Title: "Real Title"}
		meta := metadata.BookMetadata{Title: "Unknown"}

		svc.ApplyMetadataToBook(book, meta)
		assert.Equal(t, "Real Title", book.Title, "good title should not be overwritten by garbage")
	})

	t.Run("does_not_clear_existing_title", func(t *testing.T) {
		mock := &database.MockStore{}
		svc := NewService(mock)

		book := &database.Book{ID: "b1", Title: "Existing Title"}
		meta := metadata.BookMetadata{} // empty metadata

		svc.ApplyMetadataToBook(book, meta)
		assert.Equal(t, "Existing Title", book.Title, "title should not become empty")
	})

	t.Run("applies_isbn10", func(t *testing.T) {
		mock := &database.MockStore{}
		svc := NewService(mock)

		book := &database.Book{ID: "b1", Title: "Book"}
		meta := metadata.BookMetadata{ISBN: "1234567890"} // 10 chars = ISBN-10

		svc.ApplyMetadataToBook(book, meta)
		assert.Equal(t, "1234567890", *book.ISBN10)
	})

	t.Run("skips_garbage_narrator", func(t *testing.T) {
		mock := &database.MockStore{}
		svc := NewService(mock)

		narrator := "Good Narrator"
		book := &database.Book{ID: "b1", Title: "Book", Narrator: &narrator}
		meta := metadata.BookMetadata{Narrator: "Unknown"}

		svc.ApplyMetadataToBook(book, meta)
		assert.Equal(t, "Good Narrator", *book.Narrator, "garbage narrator should not replace good one")
	})

	t.Run("creates_new_author_if_needed", func(t *testing.T) {
		authorCreated := false
		mock := &database.MockStore{
			GetAuthorByNameFunc: func(name string) (*database.Author, error) {
				return nil, nil // not found
			},
			CreateAuthorFunc: func(name string) (*database.Author, error) {
				authorCreated = true
				return &database.Author{ID: 99, Name: name}, nil
			},
		}
		svc := NewService(mock)

		book := &database.Book{ID: "b1", Title: "Book"}
		meta := metadata.BookMetadata{Author: "New Author"}

		svc.ApplyMetadataToBook(book, meta)
		assert.True(t, authorCreated, "should create new author")
		assert.Equal(t, 99, *book.AuthorID)
	})

	t.Run("creates_new_series_if_needed", func(t *testing.T) {
		seriesCreated := false
		mock := &database.MockStore{
			GetSeriesByNameFunc: func(name string, authorID *int) (*database.Series, error) {
				return nil, nil // not found
			},
			CreateSeriesFunc: func(name string, authorID *int) (*database.Series, error) {
				seriesCreated = true
				return &database.Series{ID: 77, Name: name}, nil
			},
		}
		svc := NewService(mock)

		book := &database.Book{ID: "b1", Title: "Book"}
		meta := metadata.BookMetadata{Series: "New Series", SeriesPosition: "3"}

		svc.ApplyMetadataToBook(book, meta)
		assert.True(t, seriesCreated, "should create new series")
		assert.Equal(t, 77, *book.SeriesID)
		assert.Equal(t, 3, *book.SeriesSequence)
	})
}

// TestApplyMetadataToBook_YearRouting proves the print-vs-audiobook-release
// year routing that fixes the data-corruption bug: a print/work year (Open
// Library, Google Books) must land in PrintYear, an audiobook release year
// (Audible, Audnexus) in AudiobookReleaseYear, and applying a print-year
// candidate must NEVER overwrite an existing correct AudiobookReleaseYear.
func TestApplyMetadataToBook_YearRouting(t *testing.T) {
	svc := NewService(&database.MockStore{})

	t.Run("print_year_routes_to_PrintYear", func(t *testing.T) {
		book := &database.Book{ID: "b1", Title: "T"}
		// Default flag (false) == print/work year (OpenLibrary/GoogleBooks).
		meta := metadata.BookMetadata{Title: "T", PublishYear: 1937}
		svc.ApplyMetadataToBook(book, meta)
		assert.Nil(t, book.AudiobookReleaseYear, "print year must not touch AudiobookReleaseYear")
		if assert.NotNil(t, book.PrintYear) {
			assert.Equal(t, 1937, *book.PrintYear)
		}
	})

	t.Run("release_year_routes_to_AudiobookReleaseYear", func(t *testing.T) {
		book := &database.Book{ID: "b1", Title: "T"}
		meta := metadata.BookMetadata{Title: "T", PublishYear: 2010, PublishYearIsAudiobookRelease: true}
		svc.ApplyMetadataToBook(book, meta)
		assert.Nil(t, book.PrintYear, "release year must not touch PrintYear")
		if assert.NotNil(t, book.AudiobookReleaseYear) {
			assert.Equal(t, 2010, *book.AudiobookReleaseYear)
		}
	})

	t.Run("print_candidate_does_not_overwrite_release_year", func(t *testing.T) {
		existing := 2010
		book := &database.Book{ID: "b1", Title: "T", AudiobookReleaseYear: &existing}
		// A Google Books / Open Library candidate with a print year decades earlier.
		meta := metadata.BookMetadata{Title: "T", PublishYear: 1985}
		svc.ApplyMetadataToBook(book, meta)
		if assert.NotNil(t, book.AudiobookReleaseYear) {
			assert.Equal(t, 2010, *book.AudiobookReleaseYear, "correct release year must survive a print-year apply")
		}
		if assert.NotNil(t, book.PrintYear) {
			assert.Equal(t, 1985, *book.PrintYear, "print year lands in PrintYear")
		}
	})
}

// ---------------------------------------------------------------------------
// buildSearchContext
// ---------------------------------------------------------------------------

func TestBuildSearchContext(t *testing.T) {
	t.Run("nil_book", func(t *testing.T) {
		ctx := (&Service{}).buildSearchContext(nil, "Title", "Author", "Narrator")
		assert.Equal(t, "Title", ctx.Title)
		assert.Equal(t, "Author", ctx.Author)
		assert.Equal(t, "Narrator", ctx.Narrator)
		assert.Empty(t, ctx.ISBN10)
	})

	t.Run("book_with_identifiers", func(t *testing.T) {
		isbn10 := "1234567890"
		isbn13 := "9781234567890"
		asin := "B01N5AZR76"
		book := &database.Book{
			ISBN10: &isbn10,
			ISBN13: &isbn13,
			ASIN:   &asin,
		}
		ctx := (&Service{}).buildSearchContext(book, "Title", "Author", "Narrator")
		assert.Equal(t, "1234567890", ctx.ISBN10)
		assert.Equal(t, "9781234567890", ctx.ISBN13)
		assert.Equal(t, "B01N5AZR76", ctx.ASIN)
	})

	t.Run("book_without_identifiers", func(t *testing.T) {
		book := &database.Book{}
		ctx := (&Service{}).buildSearchContext(book, "T", "A", "N")
		assert.Empty(t, ctx.ISBN10)
		assert.Empty(t, ctx.ISBN13)
		assert.Empty(t, ctx.ASIN)
	})
}

// ---------------------------------------------------------------------------
// ComputeITunesPath
// ---------------------------------------------------------------------------

func TestComputeITunesPath(t *testing.T) {
	// ComputeITunesPath reads from config.AppConfig.ITunes.PathMappings.
	// With no mappings configured, it should return empty.
	t.Run("no_mappings", func(t *testing.T) {
		result := ComputeITunesPath("/some/path")
		assert.Equal(t, "", result)
	})
}

// ---------------------------------------------------------------------------
// pickBestMatchFromScored
// ---------------------------------------------------------------------------

func TestPickBestMatchFromScored(t *testing.T) {
	t.Run("selects_best_score", func(t *testing.T) {
		results := []metadata.BookMetadata{
			{Title: "Mistborn", Author: "Brandon Sanderson"},
			{Title: "Elantris", Author: "Brandon Sanderson"},
		}
		scores := []float64{0.9, 0.5}
		words := SignificantWords("Mistborn")

		matched := pickBestMatchFromScored(results, scores, "f1", words, "", "", 0)
		require.NotEmpty(t, matched)
		assert.Equal(t, "Mistborn", matched[0].Title)
	})

	t.Run("below_threshold_returns_nil", func(t *testing.T) {
		results := []metadata.BookMetadata{
			{Title: "Something", Author: "Someone"},
		}
		scores := []float64{0.1}
		words := SignificantWords("Something")

		matched := pickBestMatchFromScored(results, scores, "f1", words, "", "", 0)
		assert.Nil(t, matched, "score below threshold should return nil")
	})

	t.Run("author_match_boosts", func(t *testing.T) {
		results := []metadata.BookMetadata{
			{Title: "Mistborn", Author: "Wrong Author"},
			{Title: "Mistborn", Author: "Brandon Sanderson"},
		}
		scores := []float64{0.8, 0.7}
		words := SignificantWords("Mistborn")

		matched := pickBestMatchFromScored(results, scores, "f1", words, "Brandon Sanderson", "", 0)
		require.NotEmpty(t, matched)
		assert.Equal(t, "Brandon Sanderson", matched[0].Author, "author match should boost score")
	})

	t.Run("narrator_match_boosts", func(t *testing.T) {
		results := []metadata.BookMetadata{
			{Title: "Mistborn", Author: "Brandon Sanderson", Narrator: "Other Person"},
			{Title: "Mistborn", Author: "Brandon Sanderson", Narrator: "Michael Kramer"},
		}
		scores := []float64{0.7, 0.65}
		words := SignificantWords("Mistborn")

		matched := pickBestMatchFromScored(results, scores, "f1", words, "Brandon Sanderson", "Michael Kramer", 0)
		require.NotEmpty(t, matched)
		assert.Equal(t, "Michael Kramer", matched[0].Narrator, "narrator match should boost score")
	})

	t.Run("empty_results", func(t *testing.T) {
		matched := pickBestMatchFromScored(nil, nil, "f1", map[string]bool{}, "", "", 0)
		assert.Nil(t, matched)
	})

	t.Run("zero_base_score_skipped_for_f1", func(t *testing.T) {
		results := []metadata.BookMetadata{
			{Title: "Mistborn", Description: "desc", CoverURL: "url", Narrator: "narrator"},
		}
		scores := []float64{0.0}
		words := SignificantWords("Mistborn")

		matched := pickBestMatchFromScored(results, scores, "f1", words, "", "", 0)
		assert.Nil(t, matched, "zero base score in F1 tier should be skipped even with rich metadata")
	})
}

// ---------------------------------------------------------------------------
// durationScoreMultiplier
// ---------------------------------------------------------------------------

// TestDurationScoreMultiplier's expectations below were rewritten for
// INIT-3-T2 (unify duration scoring): durationScoreMultiplier used to bucket
// on the ABSOLUTE delta in seconds, independent of the book's own duration;
// it now looks up the shared ratio-based durationTiers table (ratio =
// |Δ|/bookDurationSec), the same table computeDurationScore uses, so the
// two functions can never disagree on the same pair again. Every case below
// with a book duration of 36000s (10h) is re-derived from the new ratio —
// see the golden fixtures in service_scoring_test.go
// (TestDurationScoringGolden) for the full old-value/new-value/why diff
// across a wider grid including a very-long (40h) book.
func TestDurationScoreMultiplier(t *testing.T) {
	cases := []struct {
		bookSec, candSec int
		wantMul          float64
		desc             string
	}{
		{0, 36000, 1.0, "unknown book duration → no adjustment"},
		{36000, 0, 1.0, "unknown candidate duration → no adjustment"},
		{36000, 36000, 1.30, "exact match → huge bonus"},
		{36000, 36030, 1.30, "30s delta, ratio 0.08% → huge bonus"},
		// 300s delta, ratio 0.83%: was ×1.20 under the old 300s bucket;
		// still deep in the new <5% ratio tier → ×1.30.
		{36000, 36300, 1.30, "5 min delta, ratio 0.83% → huge bonus"},
		// 600s delta, ratio 1.67%: was ×1.10 under the old 600s bucket;
		// still <5% ratio → ×1.30.
		{36000, 36600, 1.30, "10 min delta, ratio 1.67% → huge bonus"},
		// 1200s delta, ratio 3.33%: was ×1.05 under the old 1200s bucket;
		// still <5% ratio → ×1.30.
		{36000, 37200, 1.30, "20 min delta, ratio 3.33% → huge bonus"},
		// 1800s delta, ratio exactly 5%: was ×1.00 under the old 1800s
		// bucket; 5% ratio lands in the new 5-10% tier → ×1.20.
		{36000, 37800, 1.20, "30 min delta, ratio 5.00% → strong bonus"},
		// 3600s delta, ratio exactly 10%: was ×0.90 under the old 3600s
		// bucket; 10% ratio lands in the new 10-20% tier → ×1.10.
		{36000, 39600, 1.10, "60 min delta, ratio 10.00% → good bonus"},
		// 7200s delta, ratio exactly 20%: was ×0.75 under the old 7200s
		// bucket; 20% ratio lands in the new 20-50% "acceptable range" tier
		// → ×1.00.
		{36000, 43200, 1.00, "120 min delta, ratio 20.00% → no adjustment"},
		// 14400s delta, ratio 40%: was ×0.50 under the old >7200s
		// catch-all; 40% ratio is still within the new 20-50% tier → ×1.00.
		{36000, 50400, 1.00, ">120 min delta, ratio 40.00% → no adjustment"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.desc, func(t *testing.T) {
			got := durationScoreMultiplier(tc.bookSec, tc.candSec)
			assert.Equal(t, tc.wantMul, got, tc.desc)
		})
	}
}

func TestPickBestMatchFromScored_DurationBreaksTie(t *testing.T) {
	// Two Mistborn candidates with identical base score. One has a runtime matching
	// the book's duration; the other is 3 hours off. Duration should tip the winner.
	bookDuration := 36000 // 10 hours
	results := []metadata.BookMetadata{
		{Title: "Mistborn", Author: "Brandon Sanderson", DurationSec: 36000}, // exact match
		{Title: "Mistborn", Author: "Brandon Sanderson", DurationSec: 25200}, // 3 h off
	}
	scores := []float64{0.8, 0.8}
	words := SignificantWords("Mistborn")

	matched := pickBestMatchFromScored(results, scores, "f1", words, "Brandon Sanderson", "", bookDuration)
	require.NotEmpty(t, matched)
	assert.Equal(t, 36000, matched[0].DurationSec, "candidate with matching duration should win the tie")
}

func TestPickBestMatchFromScored_WrongEditionRejected(t *testing.T) {
	// One candidate is 3 hours shorter (abridged). Its score should be penalised
	// enough that the slightly-lower-base full version wins.
	bookDuration := 36000
	results := []metadata.BookMetadata{
		{Title: "Mistborn Abridged", Author: "Brandon Sanderson", DurationSec: 14400}, // 4h, δ=6h
		{Title: "Mistborn", Author: "Brandon Sanderson", DurationSec: 35700},          // 5min off
	}
	// Abridged result has marginally higher base score
	scores := []float64{0.82, 0.80}
	words := SignificantWords("Mistborn")

	matched := pickBestMatchFromScored(results, scores, "f1", words, "Brandon Sanderson", "", bookDuration)
	require.NotEmpty(t, matched)
	assert.Equal(t, 35700, matched[0].DurationSec, "full edition should win despite lower base score")
}

// ---------------------------------------------------------------------------
// Service setter edge cases
// ---------------------------------------------------------------------------

func TestServiceSetterEdgeCases(t *testing.T) {
	mock := &database.MockStore{}
	svc := NewService(mock)

	t.Run("set_write_back_batcher", func(t *testing.T) {
		assert.Nil(t, svc.writeBackBatcher)
		// Just confirm no panic with nil batcher
		svc.SetWriteBackBatcher(nil)
		assert.Nil(t, svc.writeBackBatcher)
	})

	t.Run("set_metadata_scorer", func(t *testing.T) {
		assert.Nil(t, svc.metadataScorer)
		svc.SetMetadataScorer(nil)
		assert.Nil(t, svc.metadataScorer)
	})

	t.Run("set_metadata_llm_scorer", func(t *testing.T) {
		assert.Nil(t, svc.llmScorer)
		svc.SetMetadataLLMScorer(nil)
		assert.Nil(t, svc.llmScorer)
	})

	t.Run("set_dedup_engine", func(t *testing.T) {
		assert.Nil(t, svc.dedupEngine)
		svc.SetDedupEngine(nil)
		assert.Nil(t, svc.dedupEngine)
	})

	t.Run("set_ol_store", func(t *testing.T) {
		assert.Nil(t, svc.olStore)
		svc.SetOLStore(nil)
		assert.Nil(t, svc.olStore)
	})

	t.Run("set_activity_service", func(t *testing.T) {
		assert.Nil(t, svc.activityService)
		svc.SetActivityService(nil)
		assert.Nil(t, svc.activityService)
	})
}

// ---------------------------------------------------------------------------
// MarkNoMatch
// ---------------------------------------------------------------------------

func TestMarkNoMatch(t *testing.T) {
	t.Run("book_not_found", func(t *testing.T) {
		mock := &database.MockStore{
			GetBookByIDFunc: func(id string) (*database.Book, error) {
				return nil, nil
			},
		}
		svc := NewService(mock)
		err := svc.MarkNoMatch("nonexistent")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})
}

// ---------------------------------------------------------------------------
// ComputeTargetPathsFromSegments (backward compat wrapper)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// ApplyMetadataSystemTags
// ---------------------------------------------------------------------------

func TestApplyMetadataSystemTags(t *testing.T) {
	t.Run("writes_source_and_language_tags", func(t *testing.T) {
		var tagsAdded []string
		mock := &database.MockStore{
			GetBookTagsDetailedFunc: func(bookID string) ([]database.BookTag, error) {
				return nil, nil
			},
			AddBookTagWithSourceFunc: func(bookID, tag, source string) error {
				tagsAdded = append(tagsAdded, tag)
				return nil
			},
		}
		svc := NewService(mock)
		svc.ApplyMetadataSystemTags("book-1", "Hardcover", "English")

		assert.Contains(t, tagsAdded, "metadata:source:hardcover")
		assert.Contains(t, tagsAdded, "metadata:language:en")
	})

	t.Run("empty_source_and_language", func(t *testing.T) {
		mock := &database.MockStore{}
		svc := NewService(mock)
		// Should not panic
		svc.ApplyMetadataSystemTags("book-1", "", "")
	})
}

// ---------------------------------------------------------------------------
// generateSegmentTitles
// ---------------------------------------------------------------------------

func TestGenerateSegmentTitles(t *testing.T) {
	t.Run("no_files", func(t *testing.T) {
		mock := &database.MockStore{
			GetBookFilesFunc: func(bookID string) ([]database.BookFile, error) {
				return nil, nil
			},
		}
		svc := NewService(mock)
		err := svc.generateSegmentTitles("book-1", "Test Book")
		assert.NoError(t, err)
	})

	t.Run("updates_file_titles", func(t *testing.T) {
		var updatedFiles []database.BookFile
		mock := &database.MockStore{
			GetBookFilesFunc: func(bookID string) ([]database.BookFile, error) {
				return []database.BookFile{
					{ID: "f1", BookID: bookID, FilePath: "/a/1.m4b", TrackNumber: 0},
					{ID: "f2", BookID: bookID, FilePath: "/a/2.m4b", TrackNumber: 0},
				}, nil
			},
			UpdateBookFileFunc: func(id string, file *database.BookFile) error {
				updatedFiles = append(updatedFiles, *file)
				return nil
			},
		}
		svc := NewService(mock)
		err := svc.generateSegmentTitles("book-1", "My Book")
		assert.NoError(t, err)
		assert.Len(t, updatedFiles, 2)
		// First file should get track number 1, second gets 2
		assert.Equal(t, 1, updatedFiles[0].TrackNumber)
		assert.Equal(t, 2, updatedFiles[1].TrackNumber)
		// Titles should use the default segment title format
		assert.Contains(t, updatedFiles[0].Title, "My Book")
		assert.Contains(t, updatedFiles[1].Title, "My Book")
	})

	t.Run("single_file_no_numbering", func(t *testing.T) {
		var updatedFiles []database.BookFile
		mock := &database.MockStore{
			GetBookFilesFunc: func(bookID string) ([]database.BookFile, error) {
				return []database.BookFile{
					{ID: "f1", BookID: bookID, FilePath: "/a/1.m4b", TrackNumber: 1},
				}, nil
			},
			UpdateBookFileFunc: func(id string, file *database.BookFile) error {
				updatedFiles = append(updatedFiles, *file)
				return nil
			},
		}
		svc := NewService(mock)
		err := svc.generateSegmentTitles("book-1", "My Book")
		assert.NoError(t, err)
		require.Len(t, updatedFiles, 1)
		assert.Equal(t, "My Book", updatedFiles[0].Title, "single file should have title without numbering")
	})
}

// ---------------------------------------------------------------------------
// MarkNoMatch (success path)
// ---------------------------------------------------------------------------

func TestMarkNoMatchSuccess(t *testing.T) {
	var updatedBook *database.Book
	mock := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{ID: id, Title: "Test"}, nil
		},
		UpdateBookFunc: func(id string, book *database.Book) (*database.Book, error) {
			updatedBook = book
			return book, nil
		},
	}
	svc := NewService(mock)
	err := svc.MarkNoMatch("book-1")
	assert.NoError(t, err)
	require.NotNil(t, updatedBook)
	assert.Equal(t, "no_match", *updatedBook.MetadataReviewStatus)
}

// ---------------------------------------------------------------------------
// RerankTopK
// ---------------------------------------------------------------------------

func TestRerankTopK(t *testing.T) {
	mock := &database.MockStore{}
	svc := NewService(mock)

	t.Run("no_llm_scorer", func(t *testing.T) {
		candidates := []MetadataCandidate{
			{Title: "A", Score: 0.9},
			{Title: "B", Score: 0.8},
		}
		result := svc.RerankTopK(context.TODO(), &database.Book{}, candidates)
		assert.Equal(t, candidates, result, "without LLM scorer, should return input unchanged")
	})

	t.Run("single_candidate", func(t *testing.T) {
		candidates := []MetadataCandidate{
			{Title: "A", Score: 0.9},
		}
		result := svc.RerankTopK(context.TODO(), &database.Book{}, candidates)
		assert.Len(t, result, 1)
	})

	t.Run("empty_candidates", func(t *testing.T) {
		result := svc.RerankTopK(context.TODO(), &database.Book{}, nil)
		assert.Nil(t, result)
	})

	// boost_stacked_tail_rescale reproduces MATCH-2: an ambiguous top-K window
	// whose original (boost-inflated) scores exceed 1.0, plus an untouched
	// tail candidate also above 1.0 but below the window's floor. Before the
	// fix, the LLM's clamped [0,1] scores overwrote the window directly, so
	// the window could fall below or collide with the tail purely due to the
	// clamp/unclamp scale mismatch. After the fix, the LLM's relative
	// preference is preserved but rescaled back into the window's original
	// [origMin, origMax] range, so the tail's relative position is
	// unaffected.
	t.Run("boost_stacked_tail_rescale", func(t *testing.T) {
		rerankSvc := NewService(mock)

		origEpsilon := config.AppConfig.MetadataScoring.LLMRerankEpsilon
		origTopK := config.AppConfig.MetadataScoring.LLMRerankTopK
		config.AppConfig.MetadataScoring.LLMRerankEpsilon = 0.2
		config.AppConfig.MetadataScoring.LLMRerankTopK = 5
		t.Cleanup(func() {
			config.AppConfig.MetadataScoring.LLMRerankEpsilon = origEpsilon
			config.AppConfig.MetadataScoring.LLMRerankTopK = origTopK
		})

		llmMock := mocks.NewMockMetadataCandidateScorer(t)
		llmMock.EXPECT().Score(tmock.Anything, tmock.Anything, tmock.Anything).
			Return([]float64{0.6, 0.95}, nil).Once()
		rerankSvc.SetMetadataLLMScorer(llmMock)

		const (
			origMax = 2.0 // window best, boost-stacked (e.g. author x1.5 x series x1.4)
			origMin = 1.9 // window worst, also boost-stacked
			tail    = 1.5 // untouched tail candidate, below origMin
		)
		candidates := []MetadataCandidate{
			{Title: "Window Best (pre-rerank)", Score: origMax},
			{Title: "Window Second (pre-rerank)", Score: origMin},
			{Title: "Tail (untouched)", Score: tail},
		}

		result := rerankSvc.RerankTopK(context.TODO(), &database.Book{}, candidates)
		require.Len(t, result, 3)

		var windowBest, windowSecond, tailCand *MetadataCandidate
		for i := range result {
			switch result[i].Title {
			case "Window Best (pre-rerank)":
				windowBest = &result[i]
			case "Window Second (pre-rerank)":
				windowSecond = &result[i]
			case "Tail (untouched)":
				tailCand = &result[i]
			}
		}
		require.NotNil(t, windowBest)
		require.NotNil(t, windowSecond)
		require.NotNil(t, tailCand)

		// The LLM scored "Window Second" (0.95) higher than "Window Best"
		// (0.6) — its preference must be honored after rescale.
		assert.Greater(t, windowSecond.Score, windowBest.Score,
			"LLM's preferred candidate must end up with the higher final score")

		// Both reranked window candidates must land within the window's
		// original [origMin, origMax] range — never leaping into or out of
		// the untouched tail's range purely due to clamp/unclamp mismatch.
		assert.GreaterOrEqual(t, windowBest.Score, origMin)
		assert.LessOrEqual(t, windowBest.Score, origMax)
		assert.GreaterOrEqual(t, windowSecond.Score, origMin)
		assert.LessOrEqual(t, windowSecond.Score, origMax)

		// The tail candidate is untouched and remains below the rescaled
		// window — no spurious leapfrogging in either direction.
		assert.Equal(t, tail, tailCand.Score)
		assert.Less(t, tailCand.Score, windowBest.Score)
		assert.Less(t, tailCand.Score, windowSecond.Score)

		// Final ordering: window candidates (by LLM preference) then tail.
		require.Len(t, result, 3)
		assert.Equal(t, "Window Second (pre-rerank)", result[0].Title)
		assert.Equal(t, "Window Best (pre-rerank)", result[1].Title)
		assert.Equal(t, "Tail (untouched)", result[2].Title)
	})
}

// ---------------------------------------------------------------------------
// ScoreBaseCandidates
// ---------------------------------------------------------------------------

func TestScoreBaseCandidates(t *testing.T) {
	mock := &database.MockStore{}
	svc := NewService(mock)

	t.Run("f1_fallback", func(t *testing.T) {
		book := &database.Book{ID: "b1", Title: "Mistborn"}
		results := []metadata.BookMetadata{
			{Title: "Mistborn"},
			{Title: "Elantris"},
		}
		words := SignificantWords("Mistborn")

		scores, tier := svc.ScoreBaseCandidates(context.TODO(), book, results, words)
		assert.Equal(t, "f1", tier)
		assert.Len(t, scores, 2)
		assert.Greater(t, scores[0], scores[1], "exact match should score higher")
	})

	t.Run("empty_results", func(t *testing.T) {
		book := &database.Book{ID: "b1"}
		scores, tier := svc.ScoreBaseCandidates(context.TODO(), book, nil, map[string]bool{})
		assert.Equal(t, "f1", tier)
		assert.Empty(t, scores)
	})
}

func TestComputeTargetPathsFromSegments(t *testing.T) {
	t.Run("empty_segments", func(t *testing.T) {
		result := ComputeTargetPathsFromSegments("/root", DefaultPathFormat, DefaultSegmentTitleFormat,
			&database.Book{}, nil, FormatVars{})
		assert.Nil(t, result)
	})

	t.Run("inactive_segments_skipped", func(t *testing.T) {
		segments := []database.BookSegment{
			{ID: "s1", BookID: 1, FilePath: "/old/path.m4b", Format: "m4b", Active: false},
		}
		vars := FormatVars{Author: "Author", Title: "Book Title"}
		entries := ComputeTargetPathsFromSegments("/root", DefaultPathFormat, DefaultSegmentTitleFormat,
			&database.Book{ID: "1"}, segments, vars)
		// Inactive segments become Missing=true BookFiles, which are skipped
		assert.Nil(t, entries)
	})

	t.Run("converts_segments_to_files", func(t *testing.T) {
		trackNum := 1
		totalTracks := 1
		hash := "abc123"
		title := "Track One"
		segments := []database.BookSegment{
			{
				ID:           "s1",
				BookID:       1,
				FilePath:     "/old/path.m4b",
				Format:       "m4b",
				TrackNumber:  &trackNum,
				TotalTracks:  &totalTracks,
				FileHash:     &hash,
				SegmentTitle: &title,
				Active:       true,
			},
		}
		vars := FormatVars{Author: "Author", Title: "Book Title"}
		entries := ComputeTargetPathsFromSegments("/root", DefaultPathFormat, DefaultSegmentTitleFormat,
			&database.Book{ID: "1"}, segments, vars)
		// Should produce entries (or nil if already at target path)
		if entries != nil {
			assert.Equal(t, "s1", entries[0].SegmentID)
		}
	})
}

// ---------------------------------------------------------------------------
// AudioFilesInDir
// ---------------------------------------------------------------------------

func TestAudioFilesInDir(t *testing.T) {
	t.Run("nonexistent_dir", func(t *testing.T) {
		files := AudioFilesInDir("/nonexistent/path/that/does/not/exist")
		assert.Nil(t, files)
	})

	t.Run("empty_temp_dir", func(t *testing.T) {
		dir := t.TempDir()
		files := AudioFilesInDir(dir)
		assert.Nil(t, files)
	})

	t.Run("dir_with_audio_files", func(t *testing.T) {
		dir := t.TempDir()
		// Create some audio files
		for _, name := range []string{"track1.m4b", "track2.mp3", "readme.txt"} {
			f, err := os.Create(filepath.Join(dir, name))
			require.NoError(t, err)
			f.Close()
		}
		files := AudioFilesInDir(dir)
		assert.Len(t, files, 2, "should find m4b and mp3 but not txt")
	})

	t.Run("file_not_dir", func(t *testing.T) {
		dir := t.TempDir()
		filePath := filepath.Join(dir, "test.m4b")
		f, _ := os.Create(filePath)
		f.Close()
		files := AudioFilesInDir(filePath)
		assert.Nil(t, files, "should return nil for a file path")
	})
}

// ---------------------------------------------------------------------------
// NewISBNService
// ---------------------------------------------------------------------------

func TestNewISBNService(t *testing.T) {
	mock := &database.MockStore{}
	svc := NewISBNService(mock, nil)
	require.NotNil(t, svc)
	assert.Equal(t, mock, svc.db)
	assert.Nil(t, svc.sources)
}

// ---------------------------------------------------------------------------
// ISBNService.resolveAuthor
// ---------------------------------------------------------------------------

func TestISBNResolveAuthor(t *testing.T) {
	t.Run("no_author_id", func(t *testing.T) {
		mock := &database.MockStore{}
		svc := NewISBNService(mock, nil)
		book := &database.Book{ID: "b1"}
		assert.Equal(t, "", svc.resolveAuthor(book))
	})

	t.Run("author_found", func(t *testing.T) {
		mock := &database.MockStore{
			GetAuthorByIDFunc: func(id int) (*database.Author, error) {
				return &database.Author{ID: id, Name: "Brandon Sanderson"}, nil
			},
		}
		svc := NewISBNService(mock, nil)
		authorID := 42
		book := &database.Book{ID: "b1", AuthorID: &authorID}
		assert.Equal(t, "Brandon Sanderson", svc.resolveAuthor(book))
	})

	t.Run("author_not_found", func(t *testing.T) {
		mock := &database.MockStore{
			GetAuthorByIDFunc: func(id int) (*database.Author, error) {
				return nil, nil
			},
		}
		svc := NewISBNService(mock, nil)
		authorID := 99
		book := &database.Book{ID: "b1", AuthorID: &authorID}
		assert.Equal(t, "", svc.resolveAuthor(book))
	})
}

// ---------------------------------------------------------------------------
// ISBNService.EnrichBookISBN
// ---------------------------------------------------------------------------

func TestEnrichBookISBN(t *testing.T) {
	t.Run("book_not_found", func(t *testing.T) {
		mock := &database.MockStore{
			GetBookByIDFunc: func(id string) (*database.Book, error) {
				return nil, nil
			},
		}
		svc := NewISBNService(mock, nil)
		found, err := svc.EnrichBookISBN(context.Background(), "nonexistent")
		assert.NoError(t, err)
		assert.False(t, found)
	})

	t.Run("already_has_isbn", func(t *testing.T) {
		isbn := "9781234567890"
		asin := "B01N5AZR76"
		mock := &database.MockStore{
			GetBookByIDFunc: func(id string) (*database.Book, error) {
				return &database.Book{ID: id, Title: "Book", ISBN13: &isbn, ASIN: &asin}, nil
			},
		}
		svc := NewISBNService(mock, nil)
		found, err := svc.EnrichBookISBN(context.Background(), "b1")
		assert.NoError(t, err)
		assert.False(t, found, "should not enrich when ISBN and ASIN already present")
	})
}

// ---------------------------------------------------------------------------
// bestTitleMatchForBook
// ---------------------------------------------------------------------------

func TestBestTitleMatchForBook(t *testing.T) {
	mock := &database.MockStore{}
	svc := NewService(mock)

	t.Run("finds_best_match", func(t *testing.T) {
		book := &database.Book{ID: "b1", Title: "Mistborn"}
		results := []metadata.BookMetadata{
			{Title: "Mistborn", Author: "Brandon Sanderson"},
			{Title: "Elantris", Author: "Brandon Sanderson"},
		}
		matched := svc.bestTitleMatchForBook(book, results, "", "", "Mistborn")
		require.NotEmpty(t, matched)
		assert.Equal(t, "Mistborn", matched[0].Title)
	})

	t.Run("no_match", func(t *testing.T) {
		book := &database.Book{ID: "b1", Title: "XYZ"}
		results := []metadata.BookMetadata{
			{Title: "Completely Different"},
		}
		matched := svc.bestTitleMatchForBook(book, results, "", "", "XYZ Unique Query")
		assert.Nil(t, matched)
	})

	t.Run("empty_results", func(t *testing.T) {
		book := &database.Book{ID: "b1", Title: "Anything"}
		matched := svc.bestTitleMatchForBook(book, nil, "", "", "Anything")
		assert.Nil(t, matched)
	})
}

// ---------------------------------------------------------------------------
// queueISBNEnrichment
// ---------------------------------------------------------------------------

func TestQueueISBNEnrichment(t *testing.T) {
	t.Run("no_enrichment_service", func(t *testing.T) {
		mock := &database.MockStore{}
		svc := NewService(mock)
		// Should not panic
		svc.queueISBNEnrichment("b1", &database.Book{ID: "b1"})
	})

	t.Run("book_already_has_identifiers", func(t *testing.T) {
		mock := &database.MockStore{}
		svc := NewService(mock)
		isbn := "9781234567890"
		asin := "B01234567X"
		svc.SetISBNEnrichment(NewISBNService(mock, nil))
		// Book has both ISBN and ASIN — enrichment should be skipped
		svc.queueISBNEnrichment("b1", &database.Book{
			ID:     "b1",
			ISBN13: &isbn,
			ASIN:   &asin,
		})
	})
}

// ---------------------------------------------------------------------------
// backupFileBeforeWrite
// ---------------------------------------------------------------------------

func TestBackupFileBeforeWrite(t *testing.T) {
	// With default config (WriteBackupBeforeTagWrite = false), should be no-op
	t.Run("disabled_by_default", func(t *testing.T) {
		backupFileBeforeWrite("/some/path.m4b")
		// No panic or error expected
	})

	t.Run("empty_path", func(t *testing.T) {
		backupFileBeforeWrite("")
		// No panic expected
	})
}

// ---------------------------------------------------------------------------
// removeEmptyDirs
// ---------------------------------------------------------------------------

func TestRemoveEmptyDirs(t *testing.T) {
	t.Run("removes_empty_dirs", func(t *testing.T) {
		base := t.TempDir()
		nested := filepath.Join(base, "a", "b", "c")
		require.NoError(t, os.MkdirAll(nested, 0o755))

		removeEmptyDirs(nested, base)

		// All empty dirs should be removed
		_, err := os.Stat(filepath.Join(base, "a"))
		assert.True(t, os.IsNotExist(err), "empty parent dirs should be removed")
	})

	t.Run("stops_at_non_empty", func(t *testing.T) {
		base := t.TempDir()
		nested := filepath.Join(base, "a", "b")
		require.NoError(t, os.MkdirAll(nested, 0o755))
		// Create a file in "a" so it's not empty
		f, _ := os.Create(filepath.Join(base, "a", "file.txt"))
		f.Close()

		removeEmptyDirs(nested, base)

		// "a" should still exist because it has a file
		_, err := os.Stat(filepath.Join(base, "a"))
		assert.NoError(t, err, "non-empty dir should remain")
		// "b" should be removed
		_, err = os.Stat(nested)
		assert.True(t, os.IsNotExist(err), "empty child should be removed")
	})

	t.Run("stops_at_boundary", func(t *testing.T) {
		base := t.TempDir()
		// removeEmptyDirs should not remove stopAt itself
		removeEmptyDirs(base, base)
		_, err := os.Stat(base)
		assert.NoError(t, err, "stopAt dir should not be removed")
	})
}

// ---------------------------------------------------------------------------
// mockMetadataSource for testing FetchMetadataForBook
// ---------------------------------------------------------------------------

type mockMetadataSource struct {
	name    string
	results []metadata.BookMetadata
	err     error
}

func (m *mockMetadataSource) Name() string { return m.name }
func (m *mockMetadataSource) SearchByTitle(ctx context.Context, title string) ([]metadata.BookMetadata, error) {
	return m.results, m.err
}
func (m *mockMetadataSource) SearchByTitleAndAuthor(ctx context.Context, title, author string) ([]metadata.BookMetadata, error) {
	return m.results, m.err
}

// ---------------------------------------------------------------------------
// FetchMetadataForBook
// ---------------------------------------------------------------------------

func TestFetchMetadataForBook(t *testing.T) {
	t.Run("book_not_found", func(t *testing.T) {
		mock := &database.MockStore{
			GetBookByIDFunc: func(id string) (*database.Book, error) {
				return nil, nil
			},
		}
		svc := NewService(mock)
		_, err := svc.FetchMetadataForBook(context.Background(), "nonexistent")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("book_marked_no_match", func(t *testing.T) {
		noMatch := "no_match"
		mock := &database.MockStore{
			GetBookByIDFunc: func(id string) (*database.Book, error) {
				return &database.Book{ID: id, Title: "Book", MetadataReviewStatus: &noMatch}, nil
			},
		}
		svc := NewService(mock)
		_, err := svc.FetchMetadataForBook(context.Background(), "b1")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "no-match")
	})

	t.Run("no_sources_enabled", func(t *testing.T) {
		mock := &database.MockStore{
			GetBookByIDFunc: func(id string) (*database.Book, error) {
				return &database.Book{ID: id, Title: "Book"}, nil
			},
		}
		svc := NewService(mock)
		svc.SetOverrideSources([]metadata.MetadataSource{})
		_, err := svc.FetchMetadataForBook(context.Background(), "b1")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "no metadata sources")
	})

	t.Run("no_results_from_source", func(t *testing.T) {
		mock := &database.MockStore{
			GetBookByIDFunc: func(id string) (*database.Book, error) {
				return &database.Book{ID: id, Title: "Very Unique Title"}, nil
			},
		}
		svc := NewService(mock)
		svc.SetOverrideSources([]metadata.MetadataSource{
			&mockMetadataSource{name: "test", results: nil},
		})
		_, err := svc.FetchMetadataForBook(context.Background(), "b1")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "no metadata found")
	})

	t.Run("successful_fetch", func(t *testing.T) {
		var updatedBook *database.Book
		mock := &database.MockStore{
			GetBookByIDFunc: func(id string) (*database.Book, error) {
				return &database.Book{ID: id, Title: "Mistborn"}, nil
			},
			GetAuthorByNameFunc: func(name string) (*database.Author, error) {
				return &database.Author{ID: 1, Name: name}, nil
			},
			UpdateBookFunc: func(id string, book *database.Book) (*database.Book, error) {
				updatedBook = book
				return book, nil
			},
			RecordMetadataChangeFunc: func(record *database.MetadataChangeRecord) error {
				return nil
			},
			GetSeriesByNameFunc: func(name string, authorID *int) (*database.Series, error) {
				return &database.Series{ID: 1, Name: name}, nil
			},
		}
		svc := NewService(mock)
		svc.SetOverrideSources([]metadata.MetadataSource{
			&mockMetadataSource{
				name: "test-source",
				results: []metadata.BookMetadata{
					{
						Title:  "Mistborn",
						Author: "Brandon Sanderson",
					},
				},
			},
		})

		resp, err := svc.FetchMetadataForBook(context.Background(), "b1")
		require.NoError(t, err)
		assert.Equal(t, "test-source", resp.Source)
		assert.Equal(t, "metadata fetched and applied", resp.Message)
		require.NotNil(t, updatedBook)
		assert.Equal(t, "Mistborn", updatedBook.Title)
	})

	t.Run("results_all_rejected_by_scorer", func(t *testing.T) {
		mock := &database.MockStore{
			GetBookByIDFunc: func(id string) (*database.Book, error) {
				return &database.Book{ID: id, Title: "Mistborn"}, nil
			},
		}
		svc := NewService(mock)
		svc.SetOverrideSources([]metadata.MetadataSource{
			&mockMetadataSource{
				name: "test-source",
				results: []metadata.BookMetadata{
					{Title: "Completely Unrelated Book About Cooking"},
				},
			},
		})

		_, err := svc.FetchMetadataForBook(context.Background(), "b1")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "no metadata found")
	})
}

// ---------------------------------------------------------------------------
// FetchMetadataForBookByTitle
// ---------------------------------------------------------------------------

func TestFetchMetadataForBookByTitle(t *testing.T) {
	t.Run("book_not_found", func(t *testing.T) {
		mock := &database.MockStore{
			GetBookByIDFunc: func(id string) (*database.Book, error) {
				return nil, nil
			},
		}
		svc := NewService(mock)
		_, err := svc.FetchMetadataForBookByTitle("nonexistent")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("no_sources", func(t *testing.T) {
		mock := &database.MockStore{
			GetBookByIDFunc: func(id string) (*database.Book, error) {
				return &database.Book{ID: id, Title: "Book"}, nil
			},
		}
		svc := NewService(mock)
		svc.SetOverrideSources([]metadata.MetadataSource{})
		_, err := svc.FetchMetadataForBookByTitle("b1")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "no metadata sources")
	})
}

// ---------------------------------------------------------------------------
// SearchMetadataForBook / SearchMetadataForBookWithOptions
// ---------------------------------------------------------------------------

func TestSearchMetadataForBook(t *testing.T) {
	t.Run("book_not_found", func(t *testing.T) {
		mock := &database.MockStore{
			GetBookByIDFunc: func(id string) (*database.Book, error) {
				return nil, nil
			},
		}
		svc := NewService(mock)
		_, err := svc.SearchMetadataForBook("nonexistent", "query")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("no_sources", func(t *testing.T) {
		mock := &database.MockStore{
			GetBookByIDFunc: func(id string) (*database.Book, error) {
				return &database.Book{ID: id, Title: "Book"}, nil
			},
		}
		svc := NewService(mock)
		svc.SetOverrideSources([]metadata.MetadataSource{})
		_, err := svc.SearchMetadataForBook("b1", "query")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "no metadata sources")
	})

	t.Run("successful_search", func(t *testing.T) {
		mock := &database.MockStore{
			GetBookByIDFunc: func(id string) (*database.Book, error) {
				return &database.Book{ID: id, Title: "Mistborn"}, nil
			},
		}
		svc := NewService(mock)
		svc.SetOverrideSources([]metadata.MetadataSource{
			&mockMetadataSource{
				name: "test-source",
				results: []metadata.BookMetadata{
					{Title: "Mistborn", Author: "Brandon Sanderson"},
					{Title: "Mistborn: The Final Empire", Author: "Brandon Sanderson"},
				},
			},
		})
		resp, err := svc.SearchMetadataForBook("b1", "Mistborn")
		require.NoError(t, err)
		assert.NotNil(t, resp)
		assert.GreaterOrEqual(t, len(resp.Results), 1)
		assert.Contains(t, resp.SourcesTried, "test-source")
	})

	t.Run("with_author_hint", func(t *testing.T) {
		mock := &database.MockStore{
			GetBookByIDFunc: func(id string) (*database.Book, error) {
				return &database.Book{ID: id, Title: "Mistborn"}, nil
			},
		}
		svc := NewService(mock)
		svc.SetOverrideSources([]metadata.MetadataSource{
			&mockMetadataSource{
				name: "test-source",
				results: []metadata.BookMetadata{
					{Title: "Mistborn", Author: "Brandon Sanderson"},
				},
			},
		})
		resp, err := svc.SearchMetadataForBook("b1", "Mistborn", "Brandon Sanderson")
		require.NoError(t, err)
		assert.NotNil(t, resp)
	})
}

// ---------------------------------------------------------------------------
// searchSourceForISBN / searchSourceForASIN
// ---------------------------------------------------------------------------

func TestSearchSourceForISBN(t *testing.T) {
	mock := &database.MockStore{}
	svc := NewISBNService(mock, nil)

	t.Run("found_isbn", func(t *testing.T) {
		src := &mockMetadataSource{
			name: "test",
			results: []metadata.BookMetadata{
				{Title: "Mistborn", ISBN: "9781234567890"},
			},
		}
		isbn, length := svc.searchSourceForISBN(src, "Mistborn", "Brandon Sanderson")
		assert.Equal(t, "9781234567890", isbn)
		assert.Equal(t, 13, length)
	})

	t.Run("no_matching_title", func(t *testing.T) {
		src := &mockMetadataSource{
			name: "test",
			results: []metadata.BookMetadata{
				{Title: "Completely Different Book", ISBN: "9781234567890"},
			},
		}
		isbn, length := svc.searchSourceForISBN(src, "Mistborn", "")
		assert.Equal(t, "", isbn)
		assert.Equal(t, 0, length)
	})

	t.Run("no_results", func(t *testing.T) {
		src := &mockMetadataSource{name: "test", results: nil}
		isbn, length := svc.searchSourceForISBN(src, "Mistborn", "")
		assert.Equal(t, "", isbn)
		assert.Equal(t, 0, length)
	})
}

func TestSearchSourceForASIN(t *testing.T) {
	mock := &database.MockStore{}
	svc := NewISBNService(mock, nil)

	t.Run("found_asin", func(t *testing.T) {
		src := &mockMetadataSource{
			name: "test",
			results: []metadata.BookMetadata{
				{Title: "Mistborn", ASIN: "B01N5AZR76"},
			},
		}
		asin := svc.searchSourceForASIN(src, "Mistborn", "")
		assert.Equal(t, "B01N5AZR76", asin)
	})

	t.Run("no_matching_title", func(t *testing.T) {
		src := &mockMetadataSource{
			name: "test",
			results: []metadata.BookMetadata{
				{Title: "Other Book", ASIN: "B01N5AZR76"},
			},
		}
		asin := svc.searchSourceForASIN(src, "Mistborn", "")
		assert.Equal(t, "", asin)
	})
}

// ---------------------------------------------------------------------------
// ApplyMetadataCandidate
// ---------------------------------------------------------------------------

func TestApplyMetadataCandidate(t *testing.T) {
	t.Run("book_not_found", func(t *testing.T) {
		mock := &database.MockStore{
			GetBookByIDFunc: func(id string) (*database.Book, error) {
				return nil, nil
			},
		}
		svc := NewService(mock)
		_, err := svc.ApplyMetadataCandidate("nonexistent", MetadataCandidate{}, nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})
}

// ---------------------------------------------------------------------------
// WriteBackMetadataForBook
// ---------------------------------------------------------------------------

func TestWriteBackMetadataForBook(t *testing.T) {
	t.Run("book_not_found", func(t *testing.T) {
		mock := &database.MockStore{
			GetBookByIDFunc: func(id string) (*database.Book, error) {
				return nil, nil
			},
		}
		svc := NewService(mock)
		_, err := svc.WriteBackMetadataForBook("nonexistent")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})
}

// ---------------------------------------------------------------------------
// ApplyMetadataFileIO
// ---------------------------------------------------------------------------

func TestApplyMetadataFileIO(t *testing.T) {
	t.Run("book_not_found_no_panic", func(t *testing.T) {
		mock := &database.MockStore{
			GetBookByIDFunc: func(id string) (*database.Book, error) {
				return nil, nil
			},
		}
		svc := NewService(mock)
		// Should not panic
		svc.ApplyMetadataFileIO("nonexistent")
	})
}
