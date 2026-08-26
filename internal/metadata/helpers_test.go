// file: internal/metadata/helpers_test.go
// version: 1.0.1
// guid: 1f2e3d4c-5b6a-7c8d-9e0f-1a2b3c4d5e6f

package metadata

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dhowden/tag"
)

func TestFirstNonEmpty(t *testing.T) {
	tests := []struct {
		name     string
		values   []string
		expected string
	}{
		{
			name:     "FirstNonEmpty",
			values:   []string{"", "", "first", "second"},
			expected: "first",
		},
		{
			name:     "AllEmpty",
			values:   []string{"", "", ""},
			expected: "",
		},
		{
			name:     "WithWhitespace",
			values:   []string{"  ", "", "value"},
			expected: "value",
		},
		{
			name:     "NoValues",
			values:   []string{},
			expected: "",
		},
		{
			name:     "FirstIsValue",
			values:   []string{"first", "second"},
			expected: "first",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := firstNonEmpty(tt.values...)
			if got != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestExtractSeriesFromComments(t *testing.T) {
	tests := []struct {
		name     string
		comment  string
		expected string
	}{
		{
			name:     "SeriesColon",
			comment:  "This book is Series: The Foundation Series and it's great",
			expected: "The Foundation Series and it's great",
		},
		{
			name:     "SeriesColonWithSpace",
			comment:  "Great book! Series : The Expanse",
			expected: "The Expanse",
		},
		{
			name:     "PartOf",
			comment:  "Part of: The Wheel of Time series",
			expected: "The Wheel of Time series",
		},
		{
			name:     "WithNewline",
			comment:  "Series: The Stormlight Archive\nBook 1 of 4",
			expected: "The Stormlight Archive",
		},
		{
			name:     "WithComma",
			comment:  "Series: Harry Potter, Book 1",
			expected: "Harry Potter",
		},
		{
			name:     "WithPeriod",
			comment:  "Series: The Dresden Files. Best series ever!",
			expected: "The Dresden Files",
		},
		{
			name:     "NoMatch",
			comment:  "This is just a regular comment",
			expected: "",
		},
		{
			name:     "EmptyComment",
			comment:  "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractSeriesFromComments(tt.comment)
			if got != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestExtractYearDigits(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "YearOnly",
			input:    "2024",
			expected: "2024",
		},
		{
			name:     "YearWithText",
			input:    "Published in 2024",
			expected: "2024",
		},
		{
			name:     "DateFormat",
			input:    "2024-01-15",
			expected: "2024",
		},
		{
			name:     "NoYear",
			input:    "No year here",
			expected: "No year here",
		},
		{
			name:     "ThreeDigits",
			input:    "123",
			expected: "123",
		},
		{
			name:     "MultipleYears",
			input:    "From 2020 to 2024",
			expected: "2020", // Returns first match
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractYearDigits(tt.input)
			if got != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestExtractAuthorFromDirectory(t *testing.T) {
	tests := []struct {
		name     string
		filePath string
		expected string
	}{
		{
			name:     "AuthorTitlePattern",
			filePath: "/library/J.K. Rowling - Harry Potter/book.m4b",
			expected: "J.K. Rowling",
		},
		{
			name:     "SimpleAuthorName",
			filePath: "/library/Stephen King/book.m4b",
			expected: "Stephen King",
		},
		{
			name:     "SkipBooksDirectory",
			filePath: "/books/book.m4b",
			expected: "",
		},
		{
			name:     "SkipAudiobooksDirectory",
			filePath: "/audiobooks/book.m4b",
			expected: "",
		},
		{
			name:     "SkipDownloadsDirectory",
			filePath: "/downloads/book.m4b",
			expected: "",
		},
		{
			name:     "TranslatorPattern",
			filePath: "/library/J.R.R. Tolkien, Christopher Tolkien - translator - The Silmarillion/book.m4b",
			expected: "J.R.R. Tolkien, Christopher Tolkien",
		},
		{
			name:     "NarratedByPattern",
			filePath: "/library/Brandon Sanderson - narrated by - Mistborn/book.m4b",
			expected: "Brandon Sanderson",
		},
		{
			name:     "InvalidAuthorBook",
			filePath: "/library/Book 1/file.m4b",
			expected: "",
		},
		{
			name:     "InvalidAuthorChapter",
			filePath: "/library/Chapter 1/file.m4b",
			expected: "",
		},
		{
			name:     "CaseInsensitiveSkip",
			filePath: "/AUDIOBOOKS/file.m4b",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractAuthorFromDirectory(tt.filePath)
			if got != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestIsValidAuthor(t *testing.T) {
	tests := []struct {
		name     string
		author   string
		expected bool
	}{
		{
			name:     "ValidName",
			author:   "John Smith",
			expected: true,
		},
		{
			name:     "EmptyString",
			author:   "",
			expected: false,
		},
		{
			name:     "StartsWithBook",
			author:   "book1",
			expected: false,
		},
		{
			name:     "StartsWithChapter",
			author:   "Chapter 1",
			expected: false,
		},
		{
			name:     "StartsWithPart",
			author:   "Part 2",
			expected: false,
		},
		{
			name:     "StartsWithVol",
			author:   "Vol 3",
			expected: false,
		},
		{
			name:     "StartsWithVolume",
			author:   "Volume 4",
			expected: false,
		},
		{
			name:     "StartsWithDisc",
			author:   "Disc 1",
			expected: false,
		},
		{
			name:     "PurelyNumeric",
			author:   "123",
			expected: false,
		},
		{
			name:     "ChapterPattern",
			author:   "chapter 5",
			expected: false,
		},
		{
			name:     "ValidWithNumber",
			author:   "Author 2000",
			expected: true,
		},
		{
			name:     "CaseInsensitive",
			author:   "BOOK TEST",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isValidAuthor(tt.author)
			if got != tt.expected {
				t.Errorf("For %q, expected %v, got %v", tt.author, tt.expected, got)
			}
		})
	}
}

func TestParseFilenameForAuthor(t *testing.T) {
	tests := []struct {
		name          string
		filename      string
		expectedTitle string
		expectedAuth  string
	}{
		{
			name:          "TitleDashAuthor",
			filename:      "The Great Book - John Smith",
			expectedTitle: "The Great Book",
			expectedAuth:  "John Smith",
		},
		{
			name:          "AuthorDashTitle",
			filename:      "J.K. Rowling - Harry Potter",
			expectedTitle: "Harry Potter",
			expectedAuth:  "J.K. Rowling",
		},
		{
			name:          "NotTwoParts",
			filename:      "Title - Author - Extra",
			expectedTitle: "",
			expectedAuth:  "",
		},
		{
			name:          "NoDash",
			filename:      "Just a Title",
			expectedTitle: "",
			expectedAuth:  "",
		},
		{
			name:          "BothLookLikeNames",
			filename:      "Stephen King - John Doe",
			expectedTitle: "Stephen King",
			expectedAuth:  "John Doe",
		},
		{
			name:          "NeitherLooksLikeName",
			filename:      "the book - the title",
			expectedTitle: "",
			expectedAuth:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			title, author := parseFilenameForAuthor(tt.filename)
			if title != tt.expectedTitle {
				t.Errorf("Expected title %q, got %q", tt.expectedTitle, title)
			}
			if author != tt.expectedAuth {
				t.Errorf("Expected author %q, got %q", tt.expectedAuth, author)
			}
		})
	}
}

func TestLooksLikePersonName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "InitialsWithPeriods",
			input:    "J. K. Rowling",
			expected: true,
		},
		{
			name:     "InitialsNoPeriods",
			input:    "JK Rowling",
			expected: true,
		},
		{
			name:     "ProperCase",
			input:    "John Smith",
			expected: true,
		},
		{
			name:     "ThreeWords",
			input:    "Mary Jane Watson",
			expected: true,
		},
		{
			name:     "LowerCase",
			input:    "john smith",
			expected: false,
		},
		{
			name:     "SingleWord",
			input:    "Smith",
			expected: false,
		},
		{
			name:     "TooManyWords",
			input:    "One Two Three Four Five",
			expected: false,
		},
		{
			name:     "StartsWithLowercase",
			input:    "john Smith",
			expected: false,
		},
		{
			name:     "SecondWordLowercase",
			input:    "John smith",
			expected: false,
		},
		{
			name:     "InvalidAuthor",
			input:    "Book 1",
			expected: false,
		},
		{
			name:     "EmptyString",
			input:    "",
			expected: false,
		},
		{
			name:     "SingleInitial",
			input:    "J. Smith",
			expected: true,
		},
		{
			name:     "MixedCase",
			input:    "McDonald's",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := looksLikePersonName(tt.input)
			if got != tt.expected {
				t.Errorf("For %q, expected %v, got %v", tt.input, tt.expected, got)
			}
		})
	}
}

func TestIsTitleCaseCandidate(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{name: "TitleCase", input: "Title", expected: true},
		{name: "Lowercase", input: "title", expected: false},
		{name: "Whitespace", input: "  ", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTitleCaseCandidate(tt.input); got != tt.expected {
				t.Errorf("Expected %v for %q, got %v", tt.expected, tt.input, got)
			}
		})
	}
}

func TestExtractFromFilename(t *testing.T) {
	tests := []struct {
		name          string
		filePath      string
		expectedTitle string
		expectedAuth  string
	}{
		{
			name:          "UnderscoreSeparator",
			filePath:      "/path/Title_John Smith.m4b",
			expectedTitle: "Title",
			expectedAuth:  "John Smith",
		},
		{
			name:          "UnderscoreAuthorFirst",
			filePath:      "/path/John Smith_Title.m4b",
			expectedTitle: "Title",
			expectedAuth:  "John Smith",
		},
		{
			name:          "DashSeparator",
			filePath:      "/path/Title - Author.m4b",
			expectedTitle: "Title",
			expectedAuth:  "Author",
		},
		{
			name:          "LeadingTrackNumber",
			filePath:      "/path/01 Title.m4b",
			expectedTitle: "Title",
			expectedAuth:  "",
		},
		{
			name:          "ChapterSuffix",
			filePath:      "/path/Title-10 Chapter 10.m4b",
			expectedTitle: "Title",
			expectedAuth:  "",
		},
		{
			name:          "NoSeparator",
			filePath:      "/path/Simple Title.m4b",
			expectedTitle: "Simple Title",
			expectedAuth:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta := extractFromFilename(tt.filePath)
			if meta.Title != tt.expectedTitle {
				t.Errorf("Expected title %q, got %q", tt.expectedTitle, meta.Title)
			}
			if meta.Artist != tt.expectedAuth {
				t.Errorf("Expected author %q, got %q", tt.expectedAuth, meta.Artist)
			}
		})
	}
}

func TestExtractFromFilename_WithDirectory(t *testing.T) {
	// Create a temporary directory structure
	tmpDir := t.TempDir()
	authorDir := filepath.Join(tmpDir, "Stephen King")
	if err := os.MkdirAll(authorDir, 0755); err != nil {
		t.Fatalf("Failed to create test directory: %v", err)
	}

	filePath := filepath.Join(authorDir, "Book Title.m4b")

	meta := extractFromFilename(filePath)

	// Should extract author from directory when not in filename
	if meta.Artist != "Stephen King" {
		t.Errorf("Expected author 'Stephen King' from directory, got %q", meta.Artist)
	}
	if meta.Title != "Book Title" {
		t.Errorf("Expected title 'Book Title', got %q", meta.Title)
	}
}

func TestNormalizeRawTagValue(t *testing.T) {
	tests := []struct {
		name     string
		value    any
		expected string
	}{
		{
			name:     "String",
			value:    "test value",
			expected: "test value",
		},
		{
			name:     "StringArray",
			value:    []string{"first", "second"},
			expected: "first",
		},
		{
			name:     "StringArrayWithReleaseGroup",
			value:    []string{"[RG]", "actual value"},
			expected: "actual value",
		},
		{
			name:     "ByteArray",
			value:    []byte("byte value"),
			expected: "byte value",
		},
		{
			name:     "CommPointer",
			value:    &tag.Comm{Text: "comment text"},
			expected: "comment text",
		},
		{
			name:     "CommValue",
			value:    tag.Comm{Text: "comment text"},
			expected: "comment text",
		},
		{
			name:     "Integer",
			value:    123,
			expected: "123",
		},
		{
			name:     "Nil",
			value:    nil,
			expected: "",
		},
		{
			name:     "EmptyString",
			value:    "",
			expected: "",
		},
		{
			name:     "Whitespace",
			value:    "  trimmed  ",
			expected: "trimmed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeRawTagValue(tt.value)
			if got != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestLooksLikeReleaseGroupTag(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		expected bool
	}{
		{
			name:     "ReleaseGroup",
			value:    "[PZG]",
			expected: true,
		},
		{
			name:     "ReleaseGroupWithText",
			value:    "[FLAC]",
			expected: true,
		},
		{
			name:     "NotReleaseGroup",
			value:    "Regular Text",
			expected: false,
		},
		{
			name:     "EmptyBrackets",
			value:    "[]",
			expected: false,
		},
		{
			name:     "EmptyString",
			value:    "",
			expected: false,
		},
		{
			name:     "OnlyOpenBracket",
			value:    "[text",
			expected: false,
		},
		{
			name:     "OnlyCloseBracket",
			value:    "text]",
			expected: false,
		},
		{
			name:     "WithWhitespace",
			value:    "  [RG]  ",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := looksLikeReleaseGroupTag(tt.value)
			if got != tt.expected {
				t.Errorf("For %q, expected %v, got %v", tt.value, tt.expected, got)
			}
		})
	}
}

func TestGetRawString_NilMap(t *testing.T) {
	result := getRawString(nil, "any_key")
	if result != "" {
		t.Errorf("Expected empty string for nil map, got %q", result)
	}
}

func TestGetRawString_CaseInsensitiveComm(t *testing.T) {
	raw := map[string]any{
		"COMM": &tag.Comm{Description: "narrator", Text: "Test Narrator"},
	}

	result := getRawString(raw, "NARRATOR")
	if result != "Test Narrator" {
		t.Errorf("Expected 'Test Narrator', got %q", result)
	}
}

func TestGetRawString_CommValueType(t *testing.T) {
	raw := map[string]any{
		"COMM": tag.Comm{Description: "narrator", Text: "Test Narrator"},
	}

	result := getRawString(raw, "NARRATOR")
	if result != "Test Narrator" {
		t.Errorf("Expected 'Test Narrator', got %q", result)
	}
}

func TestSetFieldSource_NilMap(t *testing.T) {
	// Should not panic
	setFieldSource(nil, "field", "source")
}

func TestSetFieldSource_EmptySource(t *testing.T) {
	sources := make(map[string]string)
	setFieldSource(sources, "field", "")

	// Should not add to map
	if _, ok := sources["field"]; ok {
		t.Error("Expected field not to be added with empty source")
	}
}

func TestSourceOrUnknown_NilMap(t *testing.T) {
	result := sourceOrUnknown(nil, "field")
	if result != "unset" {
		t.Errorf("Expected 'unset' for nil map, got %q", result)
	}
}

func TestSourceOrUnknown_MissingField(t *testing.T) {
	sources := make(map[string]string)
	result := sourceOrUnknown(sources, "missing")
	if result != "unset" {
		t.Errorf("Expected 'unset' for missing field, got %q", result)
	}
}

func TestSourceOrUnknown_EmptyValue(t *testing.T) {
	sources := map[string]string{
		"field": "",
	}
	result := sourceOrUnknown(sources, "field")
	if result != "unset" {
		t.Errorf("Expected 'unset' for empty value, got %q", result)
	}
}

func TestSourceOrUnknown_ValidValue(t *testing.T) {
	sources := map[string]string{
		"field": "tag.Title",
	}
	result := sourceOrUnknown(sources, "field")
	if result != "tag.Title" {
		t.Errorf("Expected 'tag.Title', got %q", result)
	}
}

func TestExtractMetadata_FileNotFound(t *testing.T) {
	_, err := ExtractMetadata("/nonexistent/file.m4b", nil)
	if err == nil {
		t.Error("Expected error for nonexistent file")
	}
}
