// file: internal/organizer/pattern_test.go
// version: 1.5.0
// guid: 9a0b1c2d-3e4f-5a6b-7c8d-9e0f1a2b3c4d
// last-edited: 2026-08-11

package organizer

import (
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestPatternExpansionWithRealData tests pattern expansion with real-world examples
func TestPatternExpansionWithRealData(t *testing.T) {
	tests := []struct {
		name           string
		book           *database.Book
		folderPattern  string
		filePattern    string
		expectedFolder string
		expectedFile   string
	}{
		// Basic author/title patterns
		{
			name: "simple author and title",
			book: &database.Book{
				Title:    "Ready Player One",
				FilePath: "/source/ready-player-one.m4b",
				Author:   &database.Author{Name: "Ernest Cline"},
			},
			folderPattern:  "{author}",
			filePattern:    "{title}",
			expectedFolder: "Ernest Cline",
			expectedFile:   "Ready Player One.m4b",
		},
		// Series with position patterns
		{
			name: "series with position - Book N format",
			book: &database.Book{
				Title:          "Woken Furies",
				FilePath:       "/source/book3.m4b",
				Author:         &database.Author{Name: "Richard Morgan"},
				Series:         &database.Series{Name: "Takeshi Kovacs"},
				SeriesSequence: intPtr(3),
			},
			folderPattern:  "{author}/{series}",
			filePattern:    "{series_number} - {title}",
			expectedFolder: "Richard Morgan/Takeshi Kovacs",
			expectedFile:   "3 - Woken Furies.m4b",
		},
		{
			name: "series with zero-padded position",
			book: &database.Book{
				Title:          "Altered Carbon",
				FilePath:       "/source/book1.m4b",
				Author:         &database.Author{Name: "Richard Morgan"},
				Series:         &database.Series{Name: "Takeshi Kovacs"},
				SeriesSequence: intPtr(1),
			},
			folderPattern:  "{author}/{series}",
			filePattern:    "{series} #{series_number} - {title}",
			expectedFolder: "Richard Morgan/Takeshi Kovacs",
			expectedFile:   "Takeshi Kovacs #1 - Altered Carbon.m4b",
		},
		// Series without position
		{
			name: "series without explicit position",
			book: &database.Book{
				Title:    "Rogue Ascension",
				FilePath: "/source/rogue.m4b",
				Author:   &database.Author{Name: "Hunter Mythos"},
				Series:   &database.Series{Name: "Hunter Mythos"},
			},
			folderPattern:  "{author}/{series}",
			filePattern:    "{title}",
			expectedFolder: "Hunter Mythos/Hunter Mythos",
			expectedFile:   "Rogue Ascension.m4b",
		},
		// Multiple authors pattern (series author might differ)
		{
			name: "book with narrator info",
			book: &database.Book{
				Title:    "A Court of Wings and Ruin",
				FilePath: "/source/acowar.m4b",
				Author:   &database.Author{Name: "Sarah J. Maas"},
				Narrator: stringPtr("Jennifer Ikeda"),
			},
			folderPattern:  "{author}",
			filePattern:    "{title} - {narrator}",
			expectedFolder: "Sarah J. Maas",
			expectedFile:   "A Court of Wings and Ruin - Jennifer Ikeda.m4b",
		},
		// Book with edition info
		{
			name: "book with edition",
			book: &database.Book{
				Title:    "The Hobbit",
				FilePath: "/source/hobbit.m4b",
				Author:   &database.Author{Name: "J.R.R. Tolkien"},
				Edition:  stringPtr("Remastered 2020"),
			},
			folderPattern:  "{author}",
			filePattern:    "{title} ({edition})",
			expectedFolder: "J.R.R. Tolkien",
			expectedFile:   "The Hobbit (Remastered 2020).m4b",
		},
		// Complex multi-field pattern
		{
			name: "complex pattern with multiple fields",
			book: &database.Book{
				Title:          "Gone - Hunger",
				FilePath:       "/source/hunger.m4b",
				Author:         &database.Author{Name: "Michael Grant"},
				Series:         &database.Series{Name: "Gone"},
				SeriesSequence: intPtr(2),
				Narrator:       stringPtr("Nick Podehl"),
			},
			folderPattern:  "{author}/{series}",
			filePattern:    "{series} {series_number} - {title} - {narrator}",
			expectedFolder: "Michael Grant/Gone",
			expectedFile:   "Gone 2 - Gone - Hunger - Nick Podehl.m4b",
		},
		// Empty series handling
		{
			name: "book without series (pattern should handle empty)",
			book: &database.Book{
				Title:    "Curious Wine",
				FilePath: "/source/curious-wine.m4b",
				Author:   &database.Author{Name: "Katherine V. Forrest"},
			},
			folderPattern:  "{author}",
			filePattern:    "{title}",
			expectedFolder: "Katherine V. Forrest",
			expectedFile:   "Curious Wine.m4b",
		},
		// Book with publisher and year
		{
			name: "book with publisher and year",
			book: &database.Book{
				Title:     "Memory Mambo",
				FilePath:  "/source/memory-mambo.m4b",
				Author:    &database.Author{Name: "Achy Obejas"},
				Publisher: stringPtr("Audible Studios"),
				PrintYear: intPtr(2019),
			},
			folderPattern:  "{author}",
			filePattern:    "{title} - {publisher} ({year})",
			expectedFolder: "Achy Obejas",
			expectedFile:   "Memory Mambo - Audible Studios (2019).m4b",
		},
		// Book with quality metadata
		{
			name: "book with quality info",
			book: &database.Book{
				Title:    "Neural Wraith",
				FilePath: "/source/neural.m4b",
				Author:   &database.Author{Name: "K.D. Robertson"},
				Codec:    stringPtr("AAC"),
				Bitrate:  intPtr(128),
				Quality:  stringPtr("High"),
			},
			folderPattern:  "{author}",
			filePattern:    "{title} [{codec} {bitrate}kbps]",
			expectedFolder: "K.D. Robertson",
			expectedFile:   "Neural Wraith [AAC 128kbps].m4b",
		},
		// ISBN patterns
		{
			name: "book with ISBN",
			book: &database.Book{
				Title:    "The Passion",
				FilePath: "/source/passion.m4b",
				Author:   &database.Author{Name: "Jeanette Winterson"},
				ISBN13:   stringPtr("978-0-375-70438-1"),
			},
			folderPattern:  "{author}",
			filePattern:    "{title} - ISBN {isbn13}",
			expectedFolder: "Jeanette Winterson",
			expectedFile:   "The Passion - ISBN 978-0-375-70438-1.m4b",
		},
		// Language pattern
		{
			name: "book with language",
			book: &database.Book{
				Title:    "Oranges Are Not The Only Fruit",
				FilePath: "/source/oranges.m4b",
				Author:   &database.Author{Name: "Jeanette Winterson"},
				Language: stringPtr("English"),
			},
			folderPattern:  "{author}",
			filePattern:    "{title} [{language}]",
			expectedFolder: "Jeanette Winterson",
			expectedFile:   "Oranges Are Not The Only Fruit [English].m4b",
		},
		// Missing metadata uses defaults where required and strips empty placeholders
		{
			name: "missing metadata uses defaults",
			book: &database.Book{
				Title:    "",
				FilePath: "/source/unknown-title.m4b",
			},
			folderPattern:  "{author}/{series}",
			filePattern:    "{title} - {narrator}",
			expectedFolder: "Unknown Author",
			// Title falls back to "Unknown Title", but an absent narrator is
			// NOT defaulted to the word "narrator" — its segment is dropped.
			expectedFile: "Unknown Title.m4b",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			org := &Organizer{
				config: &config.Config{
					FolderNamingPattern: tt.folderPattern,
					FileNamingPattern:   tt.filePattern,
				},
			}

			// Test folder pattern
			folderResult, err := org.expandPattern(tt.folderPattern, tt.book)
			if err != nil {
				t.Fatalf("expand folder pattern: %v", err)
			}
			if folderResult != tt.expectedFolder {
				t.Errorf("folder pattern:\n  expected: %q\n  got:      %q", tt.expectedFolder, folderResult)
			}

			// Test file pattern (without extension)
			fileResult, err := org.expandPattern(tt.filePattern, tt.book)
			if err != nil {
				t.Fatalf("expand file pattern: %v", err)
			}
			expectedFileNoExt := tt.expectedFile[:len(tt.expectedFile)-len(filepath.Ext(tt.expectedFile))]
			if fileResult != expectedFileNoExt {
				t.Errorf("file pattern:\n  expected: %q\n  got:      %q", expectedFileNoExt, fileResult)
			}
		})
	}
}

// TestEmptyFieldRemoval tests that empty placeholders are properly removed
func TestEmptyFieldRemoval(t *testing.T) {
	tests := []struct {
		name     string
		book     *database.Book
		pattern  string
		expected string
	}{
		{
			name: "remove empty series in parentheses",
			book: &database.Book{
				Title:  "Standalone Book",
				Author: &database.Author{Name: "John Doe"},
			},
			pattern:  "{title} ({series})",
			expected: "Standalone Book",
		},
		{
			name: "remove empty narrator with dash",
			book: &database.Book{
				Title:  "Book Title",
				Author: &database.Author{Name: "Jane Doe"},
			},
			pattern: "{title} - {narrator}",
			// Was "Book Title - narrator" — the defaultNarrator bug.
			expected: "Book Title",
		},
		{
			name: "keep filled narrator",
			book: &database.Book{
				Title:    "Book Title",
				Author:   &database.Author{Name: "Jane Doe"},
				Narrator: stringPtr("Famous Narrator"),
			},
			pattern:  "{title} - {narrator}",
			expected: "Book Title - Famous Narrator",
		},
		{
			name: "series with empty number",
			book: &database.Book{
				Title:  "Book Title",
				Author: &database.Author{Name: "Author Name"},
				Series: &database.Series{Name: "Series Name"},
			},
			pattern:  "{series} - {title}",
			expected: "Series Name - Book Title",
		},
		{
			name: "series with actual number",
			book: &database.Book{
				Title:          "Book Title",
				Author:         &database.Author{Name: "Author Name"},
				Series:         &database.Series{Name: "Series Name"},
				SeriesSequence: intPtr(5),
			},
			pattern:  "{series} #{series_number} - {title}",
			expected: "Series Name #5 - Book Title",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			org := &Organizer{
				config: &config.Config{},
			}

			result, err := org.expandPattern(tt.pattern, tt.book)
			if err != nil {
				t.Fatalf("expand pattern: %v", err)
			}
			if result != tt.expected {
				t.Errorf("expected: %q\ngot:      %q", tt.expected, result)
			}
		})
	}
}

// TestComplexRealWorldPaths tests patterns from the actual test data
func TestComplexRealWorldPaths(t *testing.T) {
	tests := []struct {
		name         string
		book         *database.Book
		folderPat    string
		filePat      string
		expectedPath string
	}{
		{
			name: "series with book number in directory",
			book: &database.Book{
				Title:          "Woken Furies",
				FilePath:       "/old/path/file.m4b",
				Author:         &database.Author{Name: "Richard Morgan"},
				Series:         &database.Series{Name: "Takeshi Kovacs"},
				SeriesSequence: intPtr(3),
			},
			folderPat:    "{author}/{series}",
			filePat:      "Book {series_number} - {title}",
			expectedPath: "Richard Morgan/Takeshi Kovacs/Book 3 - Woken Furies.m4b",
		},
		{
			name: "author-only organization",
			book: &database.Book{
				Title:    "Black Dawn",
				FilePath: "/old/black-dawn.mp3",
				Author:   &database.Author{Name: "Rachel Caine"},
			},
			folderPat:    "{author}",
			filePat:      "{title}",
			expectedPath: "Rachel Caine/Black Dawn.mp3",
		},
		{
			name: "multi-book series with position",
			book: &database.Book{
				Title:          "Shadowmage",
				FilePath:       "/old/spellmonger9.m4b",
				Author:         &database.Author{Name: "Terry Mancour"},
				Series:         &database.Series{Name: "Spellmonger"},
				SeriesSequence: intPtr(9),
			},
			folderPat:    "{author}/{series}",
			filePat:      "Book {series_number} - {title}",
			expectedPath: "Terry Mancour/Spellmonger/Book 9 - Shadowmage.m4b",
		},
		{
			name: "collection series",
			book: &database.Book{
				Title:    "Girls Only - The Complete Series",
				FilePath: "/old/girls-only.m4b",
				Author:   &database.Author{Name: "Selena Kitt"},
			},
			folderPat:    "{author}",
			filePat:      "{title}",
			expectedPath: "Selena Kitt/Girls Only - The Complete Series.m4b",
		},
	}

	tmpDir := t.TempDir()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			org := &Organizer{
				config: &config.Config{
					RootDir:             tmpDir,
					FolderNamingPattern: tt.folderPat,
					FileNamingPattern:   tt.filePat,
				},
			}

			result, err := org.generateTargetPath(tt.book)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			expected := filepath.Join(tmpDir, tt.expectedPath)
			if result != expected {
				t.Errorf("expected: %q\ngot:      %q", expected, result)
			}
		})
	}
}

// TestSanitizationWithRealWorldData tests filename sanitization
func TestSanitizationWithRealWorldData(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "colon in title",
			input:    "Title: Subtitle",
			expected: "Title_ Subtitle",
		},
		{
			name:     "multiple special chars - slash not replaced",
			input:    "Book/Title? With<Bad>Chars|",
			expected: "Book/Title_ With_Bad_Chars_",
		},
		{
			name:     "question mark and asterisk",
			input:    "What? Why* Title",
			expected: "What_ Why_ Title",
		},
		{
			name:     "series with hashtag",
			input:    "Series #5 - Title",
			expected: "Series #5 - Title",
		},
		{
			name:     "parentheses preserved, brackets stripped",
			input:    "Title (Narrator) [2020]",
			expected: "Title (Narrator) 2020",
		},
		{
			name:     "brackets with series info stripped",
			input:    "[The Expanse 9.0] Leviathan Falls",
			expected: "The Expanse 9.0 Leviathan Falls",
		},
		{
			name:     "parentheses unabridged preserved",
			input:    "Title (Unabridged)",
			expected: "Title (Unabridged)",
		},
		{
			name:     "em dash and en dash",
			input:    "Title — Subtitle",
			expected: "Title — Subtitle",
		},
		{
			name:     "quotes replaced",
			input:    `"Quoted Title"`,
			expected: `_Quoted Title_`,
		},
		{
			name:     "multiple spaces collapsed",
			input:    "Title   With    Spaces",
			expected: "Title With Spaces",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeFilename(tt.input)
			if result != tt.expected {
				t.Errorf("expected: %q\ngot:      %q", tt.expected, result)
			}
		})
	}
}

// Helper function for int pointer
func intPtr(i int) *int {
	return &i
}

func TestPatternPlaceholderNormalization(t *testing.T) {
	org := &Organizer{
		config: &config.Config{},
	}
	book := &database.Book{
		Title:  "The Hobbit",
		Author: &database.Author{Name: "J.R.R. Tolkien"},
	}

	result, err := org.expandPattern("{Author}/{Title}", book)
	if err != nil {
		t.Fatalf("expand pattern: %v", err)
	}
	expected := "J.R.R. Tolkien/The Hobbit"
	if result != expected {
		t.Fatalf("expected %q, got %q", expected, result)
	}
}

func TestPatternRejectsUnknownPlaceholder(t *testing.T) {
	org := &Organizer{
		config: &config.Config{},
	}
	book := &database.Book{
		Title:  "The Hobbit",
		Author: &database.Author{Name: "J.R.R. Tolkien"},
	}

	_, err := org.expandPattern("{title}/{unsupported}", book)
	if err == nil {
		t.Fatalf("expected error for unresolved placeholder")
	}
}

// TestCollapseRedundantDup tests the collapse logic for "X - X" duplications.
func TestCollapseRedundantDup(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "exact duplicate with case difference",
			input:    "Cobra Outlaw - Cobra Outlaw",
			expected: "Cobra Outlaw",
		},
		{
			name:     "duplicate with whitespace difference",
			input:    "The Moon is a Harsh Mistress - The Moon Is a Harsh Mistress",
			expected: "The Moon is a Harsh Mistress",
		},
		{
			name:     "duplicate with extra spaces",
			input:    "Series Name  -  Series Name",
			expected: "Series Name",
		},
		{
			name:     "different segments no collapse",
			input:    "Series Name - Book Title",
			expected: "Series Name - Book Title",
		},
		{
			name:     "no separator no collapse",
			input:    "Cobra Outlaw",
			expected: "Cobra Outlaw",
		},
		{
			name:     "three parts no collapse (handles 2-part only)",
			input:    "Part A - Part B - Part C",
			expected: "Part A - Part B - Part C",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := collapseRedundantDup(tt.input)
			if result != tt.expected {
				t.Errorf("collapseRedundantDup(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}
