// file: internal/matcher/matcher_test.go
// version: 1.1.0
// guid: 3c4d5e6f-7a8b-9c0d-1e2f-3a4b5c6d7e8f
// last-edited: 2026-09-14

package matcher

import (
	"path/filepath"
	"testing"
)

func TestIdentifySeries_PatternMatching(t *testing.T) {
	tests := []struct {
		name             string
		title            string
		filePath         string
		expectedSeries   string
		expectedPosition int
	}{
		{
			name:             "Series dash Title format",
			title:            "Harry Potter - The Philosopher's Stone",
			filePath:         "/books/file.m4b",
			expectedSeries:   "Harry Potter",
			expectedPosition: 0,
		},
		{
			name:             "Series Number: Title format",
			title:            "Foundation 1: Foundation",
			filePath:         "/books/file.m4b",
			expectedSeries:   "Foundation",
			expectedPosition: 1,
		},
		{
			name:             "Series Book Number: Title format",
			title:            "Wheel of Time Book 1: The Eye of the World",
			filePath:         "/books/file.m4b",
			expectedSeries:   "Wheel of Time",
			expectedPosition: 1,
		},
		{
			name:             "Series #Number: Title format",
			title:            "Alex Cross #1: Along Came a Spider",
			filePath:         "/books/file.m4b",
			expectedSeries:   "Alex Cross",
			expectedPosition: 1,
		},
		{
			name:             "Series Vol Number: Title format",
			title:            "The Dark Tower Vol. 1: The Gunslinger",
			filePath:         "/books/file.m4b",
			expectedSeries:   "The Dark Tower",
			expectedPosition: 1,
		},
		{
			name:             "Series Volume Number: Title format",
			title:            "Dune Volume 2: Dune Messiah",
			filePath:         "/books/file.m4b",
			expectedSeries:   "Dune",
			expectedPosition: 2,
		},
		{
			name:             "No series pattern",
			title:            "Standalone Book Title",
			filePath:         "/books/standalone.m4b",
			expectedSeries:   "",
			expectedPosition: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			series, position := IdentifySeries(tt.title, tt.filePath)

			if series != tt.expectedSeries {
				t.Errorf("Expected series %q, got %q", tt.expectedSeries, series)
			}

			if position != tt.expectedPosition {
				t.Errorf("Expected position %d, got %d", tt.expectedPosition, position)
			}
		})
	}
}

func TestIdentifySeries_DirectoryStructure(t *testing.T) {
	tests := []struct {
		name         string
		title        string
		filePath     string
		expectSeries bool
	}{
		{
			name:         "Trilogy in path",
			title:        "Book Title",
			filePath:     "/books/Lord of the Rings Trilogy/Book1.m4b",
			expectSeries: true,
		},
		{
			name:         "Series in path",
			title:        "Book Title",
			filePath:     "/books/Harry Potter Series/Book1.m4b",
			expectSeries: true,
		},
		{
			name:         "No series keywords",
			title:        "Book Title",
			filePath:     "/books/Author Name/Book1.m4b",
			expectSeries: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			series, _ := IdentifySeries(tt.title, tt.filePath)

			if tt.expectSeries && series == "" {
				t.Error("Expected to find series from directory structure")
			}

			if !tt.expectSeries && series != "" {
				t.Errorf("Did not expect to find series, but got: %s", series)
			}
		})
	}
}

func TestIdentifySeries_ColonAndDashFormats(t *testing.T) {
	tests := []struct {
		name             string
		title            string
		expectedSeries   string
		expectedPosition int
	}{
		{
			name:             "Colon separator",
			title:            "Series Name 1: Book Title",
			expectedSeries:   "Series Name",
			expectedPosition: 1,
		},
		{
			name:             "Dash separator",
			title:            "Series Name 2 - Book Title",
			expectedSeries:   "Series Name",
			expectedPosition: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			series, position := IdentifySeries(tt.title, "/books/file.m4b")

			if series != tt.expectedSeries {
				t.Errorf("Expected series %q, got %q", tt.expectedSeries, series)
			}

			if position != tt.expectedPosition {
				t.Errorf("Expected position %d, got %d", tt.expectedPosition, position)
			}
		})
	}
}

func TestIsSingleWord(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"word", true},
		{"two words", false},
		{"", false},
		{"  ", false},
		{"multiple word phrase", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := isSingleWord(tt.input)
			if got != tt.want {
				t.Errorf("isSingleWord(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestIdentifySeries_EmptyTitle(t *testing.T) {
	// When title is empty, should try to extract from filename
	filePath := filepath.Join("/books", "Series 1 - Book Title.m4b")
	series, _ := IdentifySeries("", filePath)

	if series == "" {
		t.Error("Expected series to be extracted from filename when title is empty")
	}
}

func TestIdentifySeries_ComplexPaths(t *testing.T) {
	tests := []struct {
		name     string
		title    string
		filePath string
	}{
		{
			name:     "Deep nested path",
			title:    "Book Title",
			filePath: "/media/audiobooks/Fantasy/Author Name/Series Name/Book 1.m4b",
		},
		{
			name:     "Path with special characters",
			title:    "Book: A Story",
			filePath: "/books/Author's Collection/Book.m4b",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Should not panic or error
			series, position := IdentifySeries(tt.title, tt.filePath)

			// Basic validation
			if position < 0 {
				t.Error("Position should not be negative")
			}

			// Series can be empty or non-empty, both are valid
			_ = series
		})
	}
}

// TestIdentifySeries_PartSuffixIsNotAPosition: "Rogue Lawyer - 001" is a
// title plus the part number of a multi-part rip. The "Series - Title"
// pattern used to Atoi its TITLE group, so "001" became series "Rogue Lawyer"
// #1, and the apply gate later refused the book as "book is #1" (2026-09-14
// prod preview). A bare part number after the dash means the title names no
// series and no position.
func TestIdentifySeries_PartSuffixIsNotAPosition(t *testing.T) {
	for _, title := range []string{
		"Rogue Lawyer - 001",
		"All Tomorrow's Parties - 001",
		"The Rooster Bar - 001",
		"Witness to a Trial - 001",
	} {
		series, position := IdentifySeries(title, "/books/file.m4b")
		if position != 0 || series != "" {
			t.Errorf("IdentifySeries(%q) = (%q, %d), want (\"\", 0)", title, series, position)
		}
	}
	// Controls: a real number before the dash still positions the book.
	if s, p := IdentifySeries("Mistborn 01 - The Final Empire", "/books/file.m4b"); s != "Mistborn" || p != 1 {
		t.Errorf("Mistborn = (%q, %d), want (\"Mistborn\", 1)", s, p)
	}
	if _, p := IdentifySeries("The Sorcerer's Ring - 04 - A Cry of Honor", "/books/file.m4b"); p != 4 {
		t.Errorf("Sorcerer's Ring position = %d, want 4", p)
	}
	if s, p := IdentifySeries("Big Cats - 3", "/books/file.m4b"); s != "Big Cats" || p != 3 {
		t.Errorf("Big Cats - 3 = (%q, %d), want (\"Big Cats\", 3)", s, p)
	}
}
