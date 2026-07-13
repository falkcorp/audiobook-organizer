// file: internal/organizer/unit_test.go
// version: 1.0.3
// guid: d4e5f6a7-b8c9-0d1e-2f3a-4b5c6d7e8f90
// last-edited: 2026-07-13

package organizer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/stretchr/testify/mock"
)

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
		want        string
	}{
		{
			name:        "single file book returns title only",
			format:      DefaultSegmentTitleFormat,
			title:       "My Book",
			track:       1,
			totalTracks: 1,
			want:        "My Book",
		},
		{
			name:        "zero total tracks returns title only",
			format:      DefaultSegmentTitleFormat,
			title:       "My Book",
			track:       1,
			totalTracks: 0,
			want:        "My Book",
		},
		{
			name:        "multi track default format",
			format:      DefaultSegmentTitleFormat,
			title:       "My Book",
			track:       3,
			totalTracks: 10,
			want:        "My Book - 3_10",
		},
		{
			name:        "track with format spec 02d",
			format:      "{title} - {track:02d} of {total_tracks}",
			title:       "Album",
			track:       3,
			totalTracks: 12,
			want:        "Album - 03 of 12",
		},
		{
			name:        "track without format spec",
			format:      "{title} Part {track}",
			title:       "Story",
			track:       7,
			totalTracks: 20,
			want:        "Story Part 7",
		},
		{
			name:        "no track placeholder in format",
			format:      "{title} segment",
			title:       "Podcast",
			track:       5,
			totalTracks: 10,
			want:        "Podcast segment",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatSegmentTitle(tt.format, tt.title, tt.track, tt.totalTracks)
			if got != tt.want {
				t.Errorf("FormatSegmentTitle() = %q, want %q", got, tt.want)
			}
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
		want   string
	}{
		{
			name:   "default format with all vars",
			format: DefaultPathFormat,
			vars: FormatVars{
				Author:      "Isaac Asimov",
				Title:       "Foundation",
				Series:      "Foundation",
				SeriesPos:   "1",
				Track:       1,
				TotalTracks: 5,
				Ext:         "m4b",
			},
			want: "Isaac Asimov/Foundation 1 - Foundation/Foundation - 1_5.m4b",
		},
		{
			name:   "no series prefix when series empty",
			format: "{author}/{series_prefix}{title}/{track_title}.{ext}",
			vars: FormatVars{
				Author:      "Author",
				Title:       "Title",
				Track:       1,
				TotalTracks: 3,
				Ext:         "mp3",
			},
			want: "Author/Title/Title - 1_3.mp3",
		},
		{
			name:   "series prefix without position",
			format: "{series_prefix}{title}",
			vars: FormatVars{
				Title:  "Book",
				Series: "MySeries",
			},
			want: "MySeries - Book",
		},
		{
			name:   "pre-computed track title used",
			format: "{author}/{track_title}.{ext}",
			vars: FormatVars{
				Author:     "Auth",
				TrackTitle: "Chapter 1",
				Ext:        "flac",
			},
			want: "Auth/Chapter 1.flac",
		},
		{
			name:   "year substitution",
			format: "{author}/{title} ({year}).{ext}",
			vars: FormatVars{
				Author: "Auth",
				Title:  "Book",
				Year:   2023,
				Ext:    "m4b",
			},
			want: "Auth/Book (2023).m4b",
		},
		{
			name:   "year zero omitted leaves parens",
			format: "{author}/{title} ({year}).{ext}",
			vars: FormatVars{
				Author: "Auth",
				Title:  "Book",
				Year:   0,
				Ext:    "m4b",
			},
			want: "Auth/Book ().m4b",
		},
		{
			name:   "narrator and lang",
			format: "{narrator}/{lang}/{title}.{ext}",
			vars: FormatVars{
				Narrator: "Narrator A",
				Lang:     "en",
				Title:    "Book",
				Ext:      "mp3",
			},
			want: "Narrator A/en/Book.mp3",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatPath(tt.format, tt.vars)
			if got != tt.want {
				t.Errorf("FormatPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// CollapseEmptySegments
// ---------------------------------------------------------------------------

func TestCollapseEmptySegments(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"double dots collapsed", "author..title", "author.title"},
		{"triple dots", "a...b", "a.b"},
		{"dot-slash removed", "a./b", "a/b"},
		{"slash-dot removed", "a/.b", "a/b"},
		{"double slashes", "a//b", "a/b"},
		{"leading/trailing trimmed", "/path/to/file/", "path/to/file"},
		{"dots at edges", ".path.", "path"},
		{"clean path unchanged", "author/title/file", "author/title/file"},
		{"empty string", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CollapseEmptySegments(tt.input)
			if got != tt.want {
				t.Errorf("CollapseEmptySegments(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// SanitizePathComponent
// ---------------------------------------------------------------------------

func TestSanitizePathComponent(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"slash replaced", "a/b", "a b"},
		{"backslash replaced", "a\\b", "a b"},
		{"colon replaced", "a:b", "a -b"},
		{"asterisk removed", "a*b", "ab"},
		{"question mark removed", "a?b", "ab"},
		{"quotes replaced", `a"b`, "a'b"},
		{"angle brackets removed", "a<b>c", "abc"},
		{"pipe replaced", "a|b", "a -b"},
		{"brackets removed", "a[b]c", "abc"},
		{"double spaces collapsed", "a  b", "a b"},
		{"leading/trailing spaces trimmed", "  hello  ", "hello"},
		{"clean string unchanged", "hello world", "hello world"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizePathComponent(tt.input)
			if got != tt.want {
				t.Errorf("SanitizePathComponent(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ComputeTargetPaths
// ---------------------------------------------------------------------------

func TestComputeTargetPaths(t *testing.T) {
	book := &database.Book{
		ID:     "book-1",
		Title:  "My Book",
		Author: &database.Author{Name: "Author"},
	}
	vars := FormatVars{
		Author: "Author",
		Title:  "My Book",
	}

	t.Run("empty root returns nil", func(t *testing.T) {
		result := ComputeTargetPaths("", DefaultPathFormat, DefaultSegmentTitleFormat, book, []database.BookFile{
			{ID: "f1", FilePath: "/old/file.m4b", Format: "m4b"},
		}, vars)
		if result != nil {
			t.Errorf("expected nil for empty root, got %v", result)
		}
	})

	t.Run("empty files returns nil", func(t *testing.T) {
		result := ComputeTargetPaths("/root", DefaultPathFormat, DefaultSegmentTitleFormat, book, nil, vars)
		if result != nil {
			t.Errorf("expected nil for empty files, got %v", result)
		}
	})

	t.Run("missing files skipped", func(t *testing.T) {
		files := []database.BookFile{
			{ID: "f1", FilePath: "/old/file1.m4b", Format: "m4b", Missing: true},
		}
		result := ComputeTargetPaths("/root", DefaultPathFormat, DefaultSegmentTitleFormat, book, files, vars)
		if len(result) != 0 {
			t.Errorf("expected 0 entries for all-missing files, got %d", len(result))
		}
	})

	t.Run("single file produces entry when path differs", func(t *testing.T) {
		files := []database.BookFile{
			{ID: "f1", FilePath: "/old/file.m4b", Format: "m4b"},
		}
		result := ComputeTargetPaths("/root", DefaultPathFormat, "", book, files, vars)
		if len(result) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(result))
		}
		if result[0].SegmentID != "f1" {
			t.Errorf("segment ID = %q, want f1", result[0].SegmentID)
		}
		if result[0].SourcePath != "/old/file.m4b" {
			t.Errorf("source = %q", result[0].SourcePath)
		}
		// Target should be under /root
		if !filepath.IsAbs(result[0].TargetPath) {
			t.Errorf("target not absolute: %q", result[0].TargetPath)
		}
	})

	t.Run("file already at target not included", func(t *testing.T) {
		// Compute what the target would be, then set source = target
		files := []database.BookFile{
			{ID: "f1", FilePath: "/old/file.m4b", Format: "m4b"},
		}
		entries := ComputeTargetPaths("/root", DefaultPathFormat, "", book, files, vars)
		if len(entries) == 0 {
			t.Skip("no entries produced")
		}
		// Now set source = computed target
		files[0].FilePath = entries[0].TargetPath
		result := ComputeTargetPaths("/root", DefaultPathFormat, "", book, files, vars)
		if len(result) != 0 {
			t.Errorf("expected 0 entries when source == target, got %d", len(result))
		}
	})

	t.Run("sorts by track number", func(t *testing.T) {
		files := []database.BookFile{
			{ID: "f3", FilePath: "/old/c.m4b", Format: "m4b", TrackNumber: 3},
			{ID: "f1", FilePath: "/old/a.m4b", Format: "m4b", TrackNumber: 1},
			{ID: "f2", FilePath: "/old/b.m4b", Format: "m4b", TrackNumber: 2},
		}
		result := ComputeTargetPaths("/root", DefaultPathFormat, "", book, files, vars)
		// All should produce entries (paths differ from /old/...)
		if len(result) < 1 {
			t.Skip("no entries produced")
		}
		// Check ordering by verifying segment IDs
		ids := make([]string, len(result))
		for i, e := range result {
			ids[i] = e.SegmentID
		}
		// f1 should come before f2 which should come before f3
		for i := 0; i < len(ids)-1; i++ {
			if ids[i] > ids[i+1] {
				t.Errorf("entries not sorted by track: %v", ids)
				break
			}
		}
	})

	t.Run("uses file extension from path", func(t *testing.T) {
		files := []database.BookFile{
			{ID: "f1", FilePath: "/old/file.mp3"},
		}
		result := ComputeTargetPaths("/root", "{title}.{ext}", "", book, files, vars)
		if len(result) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(result))
		}
		if ext := filepath.Ext(result[0].TargetPath); ext != ".mp3" {
			t.Errorf("expected .mp3 extension, got %q", ext)
		}
	})

	t.Run("falls back to format when no extension", func(t *testing.T) {
		files := []database.BookFile{
			{ID: "f1", FilePath: "/old/file", Format: "m4b"},
		}
		result := ComputeTargetPaths("/root", "{title}.{ext}", "", book, files, vars)
		if len(result) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(result))
		}
		if ext := filepath.Ext(result[0].TargetPath); ext != ".m4b" {
			t.Errorf("expected .m4b extension, got %q in path %q", ext, result[0].TargetPath)
		}
	})
}

// ---------------------------------------------------------------------------
// ComputeTargetPathsFromSegments
// ---------------------------------------------------------------------------

func TestComputeTargetPathsFromSegments(t *testing.T) {
	book := &database.Book{
		ID:     "book-1",
		Title:  "Test",
		Author: &database.Author{Name: "Auth"},
	}
	vars := FormatVars{Author: "Auth", Title: "Test"}

	trackNum := 1
	totalTracks := 2
	hash := "abc123"
	segTitle := "Chapter 1"

	segments := []database.BookSegment{
		{
			ID:           "seg-1",
			BookID:       42,
			FilePath:     "/old/ch1.m4b",
			Format:       "m4b",
			SizeBytes:    1000,
			DurationSec:  300,
			TrackNumber:  &trackNum,
			TotalTracks:  &totalTracks,
			SegmentTitle: &segTitle,
			FileHash:     &hash,
			Active:       true,
		},
		{
			ID:       "seg-2",
			BookID:   42,
			FilePath: "/old/ch2.m4b",
			Format:   "m4b",
			Active:   false, // inactive = Missing
		},
	}

	result := ComputeTargetPathsFromSegments("/root", DefaultPathFormat, "", book, segments, vars)
	// seg-2 is inactive (Missing=true), should be skipped
	if len(result) != 1 {
		t.Fatalf("expected 1 entry (inactive skipped), got %d", len(result))
	}
	if result[0].SegmentID != "seg-1" {
		t.Errorf("expected seg-1, got %q", result[0].SegmentID)
	}
}

// ---------------------------------------------------------------------------
// RenameFiles
// ---------------------------------------------------------------------------

func TestRenameFiles(t *testing.T) {
	t.Run("empty entries returns empty result", func(t *testing.T) {
		result, err := RenameFiles(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result == nil {
			t.Fatal("expected non-nil result")
		}
		if len(result.Succeeded) != 0 || len(result.Skipped) != 0 {
			t.Errorf("expected empty result, got succeeded=%d skipped=%d", len(result.Succeeded), len(result.Skipped))
		}
	})

	t.Run("missing source files are skipped", func(t *testing.T) {
		entries := []FileRenameEntry{
			{SegmentID: "s1", SourcePath: "/nonexistent/file.m4b", TargetPath: "/tmp/out.m4b"},
		}
		result, err := RenameFiles(entries)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result.Skipped) != 1 {
			t.Errorf("expected 1 skipped, got %d", len(result.Skipped))
		}
	})

	t.Run("successful rename", func(t *testing.T) {
		tmpDir := t.TempDir()
		src := filepath.Join(tmpDir, "source.m4b")
		dst := filepath.Join(tmpDir, "subdir", "dest.m4b")
		if err := os.WriteFile(src, []byte("audio"), 0644); err != nil {
			t.Fatal(err)
		}

		entries := []FileRenameEntry{
			{SegmentID: "s1", SourcePath: src, TargetPath: dst},
		}
		result, err := RenameFiles(entries)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result.Succeeded) != 1 {
			t.Errorf("expected 1 succeeded, got %d", len(result.Succeeded))
		}
		// Verify file is at destination
		if _, err := os.Stat(dst); err != nil {
			t.Errorf("destination file not found: %v", err)
		}
		// Source should be gone
		if _, err := os.Stat(src); !os.IsNotExist(err) {
			t.Error("source file still exists after rename")
		}
	})

	t.Run("all missing returns empty valid", func(t *testing.T) {
		entries := []FileRenameEntry{
			{SegmentID: "s1", SourcePath: "/no/such/a.m4b", TargetPath: "/tmp/a.m4b"},
			{SegmentID: "s2", SourcePath: "/no/such/b.m4b", TargetPath: "/tmp/b.m4b"},
		}
		result, err := RenameFiles(entries)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result.Skipped) != 2 {
			t.Errorf("expected 2 skipped, got %d", len(result.Skipped))
		}
		if len(result.Succeeded) != 0 {
			t.Errorf("expected 0 succeeded, got %d", len(result.Succeeded))
		}
	})
}

// ---------------------------------------------------------------------------
// NewPreviewService
// ---------------------------------------------------------------------------

func TestNewPreviewService(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewPreviewService(mockStore)
	if svc == nil {
		t.Fatal("NewPreviewService returned nil")
	}
	if svc.db != mockStore {
		t.Error("db not set correctly")
	}
	// Default IsProtectedPath should return false
	if svc.IsProtectedPath("/any/path") {
		t.Error("default IsProtectedPath should return false")
	}
	// Default ResolveAuthorAndSeriesNames should return empty for nil author/series
	book := &database.Book{Title: "Test"}
	author, series := svc.ResolveAuthorAndSeriesNames(book)
	if author != "" || series != "" {
		t.Errorf("expected empty defaults, got author=%q series=%q", author, series)
	}
	// With populated author/series
	book2 := &database.Book{
		Title:  "Test",
		Author: &database.Author{Name: "Auth"},
		Series: &database.Series{Name: "Ser"},
	}
	author2, series2 := svc.ResolveAuthorAndSeriesNames(book2)
	if author2 != "Auth" || series2 != "Ser" {
		t.Errorf("expected Auth/Ser, got %q/%q", author2, series2)
	}
}

// ---------------------------------------------------------------------------
// NewRenameService
// ---------------------------------------------------------------------------

func TestNewRenameService(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewRenameService(mockStore)
	if svc == nil {
		t.Fatal("NewRenameService returned nil")
	}
	if svc.db != mockStore {
		t.Error("db not set correctly")
	}
	// Default IsProtectedPath
	if svc.IsProtectedPath("/any") {
		t.Error("default IsProtectedPath should return false")
	}
	// Default FilterUnchangedTags returns input
	tags := map[string]interface{}{"title": "X"}
	filtered := svc.FilterUnchangedTags("/file", tags)
	if len(filtered) != 1 {
		t.Errorf("default FilterUnchangedTags should return input, got %d entries", len(filtered))
	}
	// Default ComputeITunesPath returns empty
	if p := svc.ComputeITunesPath("/file"); p != "" {
		t.Errorf("default ComputeITunesPath should return empty, got %q", p)
	}
}

// ---------------------------------------------------------------------------
// BuildTagMetadata
// ---------------------------------------------------------------------------

func TestBuildTagMetadata(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewRenameService(mockStore)

	t.Run("basic metadata", func(t *testing.T) {
		book := &database.Book{Title: "My Book"}
		meta := svc.BuildTagMetadata(book, "Author Name", "Narrator Name")

		if meta["title"] != "My Book" {
			t.Errorf("title = %v", meta["title"])
		}
		if meta["album"] != "My Book" {
			t.Errorf("album = %v", meta["album"])
		}
		if meta["genre"] != "Audiobook" {
			t.Errorf("genre = %v", meta["genre"])
		}
		if meta["artist"] != "Author Name" {
			t.Errorf("artist = %v", meta["artist"])
		}
		if meta["album_artist"] != "Narrator Name" {
			t.Errorf("album_artist = %v", meta["album_artist"])
		}
		if meta["composer"] != "Narrator Name" {
			t.Errorf("composer = %v", meta["composer"])
		}
	})

	t.Run("empty author and narrator omitted", func(t *testing.T) {
		book := &database.Book{Title: "Solo"}
		meta := svc.BuildTagMetadata(book, "", "")

		if _, ok := meta["artist"]; ok {
			t.Error("artist should not be set for empty author")
		}
		if _, ok := meta["album_artist"]; ok {
			t.Error("album_artist should not be set for empty narrator")
		}
		if _, ok := meta["composer"]; ok {
			t.Error("composer should not be set for empty narrator")
		}
	})

	t.Run("audiobook release year preferred", func(t *testing.T) {
		releaseYear := 2022
		printYear := 1995
		book := &database.Book{
			Title:                "Book",
			AudiobookReleaseYear: &releaseYear,
			PrintYear:            &printYear,
		}
		meta := svc.BuildTagMetadata(book, "", "")
		if meta["year"] != "2022" {
			t.Errorf("year = %v, want 2022", meta["year"])
		}
	})

	t.Run("falls back to print year", func(t *testing.T) {
		printYear := 1995
		book := &database.Book{
			Title:     "Book",
			PrintYear: &printYear,
		}
		meta := svc.BuildTagMetadata(book, "", "")
		if meta["year"] != "1995" {
			t.Errorf("year = %v, want 1995", meta["year"])
		}
	})

	t.Run("no year when both nil", func(t *testing.T) {
		book := &database.Book{Title: "Book"}
		meta := svc.BuildTagMetadata(book, "", "")
		if _, ok := meta["year"]; ok {
			t.Error("year should not be set when both years are nil")
		}
	})
}

// ---------------------------------------------------------------------------
// computeTagChanges
// ---------------------------------------------------------------------------

func TestComputeTagChanges(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewRenameService(mockStore)

	t.Run("all fields populated", func(t *testing.T) {
		releaseYear := 2020
		book := &database.Book{
			Title:                "Book",
			AudiobookReleaseYear: &releaseYear,
		}
		changes := svc.computeTagChanges(book, "Author", "Narrator")

		fieldMap := make(map[string]string)
		for _, c := range changes {
			fieldMap[c.Field] = c.Proposed
		}

		if fieldMap["title"] != "Book" {
			t.Errorf("title = %q", fieldMap["title"])
		}
		if fieldMap["album"] != "Book" {
			t.Errorf("album = %q", fieldMap["album"])
		}
		if fieldMap["artist"] != "Author" {
			t.Errorf("artist = %q", fieldMap["artist"])
		}
		if fieldMap["album_artist"] != "Narrator" {
			t.Errorf("album_artist = %q", fieldMap["album_artist"])
		}
		if fieldMap["genre"] != "Audiobook" {
			t.Errorf("genre = %q", fieldMap["genre"])
		}
		if fieldMap["year"] != "2020" {
			t.Errorf("year = %q", fieldMap["year"])
		}
	})

	t.Run("empty title produces no title change", func(t *testing.T) {
		book := &database.Book{Title: ""}
		changes := svc.computeTagChanges(book, "", "")

		for _, c := range changes {
			if c.Field == "title" {
				t.Error("should not have title change for empty title")
			}
		}
	})
}

// ---------------------------------------------------------------------------
// resolveNarratorNames
// ---------------------------------------------------------------------------

func TestResolveNarratorNames(t *testing.T) {
	t.Run("uses book narrators from DB", func(t *testing.T) {
		mockStore := mocks.NewMockStore(t)
		svc := NewRenameService(mockStore)

		mockStore.On("GetBookNarrators", "book-1").Return([]database.BookNarrator{
			{BookID: "book-1", NarratorID: 1},
			{BookID: "book-1", NarratorID: 2},
		}, nil)
		mockStore.On("GetNarratorByID", 1).Return(&database.Narrator{ID: 1, Name: "Alice"}, nil)
		mockStore.On("GetNarratorByID", 2).Return(&database.Narrator{ID: 2, Name: "Bob"}, nil)

		book := &database.Book{ID: "book-1"}
		result := svc.resolveNarratorNames("book-1", book)
		if result != "Alice & Bob" {
			t.Errorf("expected 'Alice & Bob', got %q", result)
		}
	})

	t.Run("falls back to book.Narrator field", func(t *testing.T) {
		mockStore := mocks.NewMockStore(t)
		svc := NewRenameService(mockStore)

		mockStore.On("GetBookNarrators", "book-2").Return([]database.BookNarrator{}, nil)

		narrator := "Charlie"
		book := &database.Book{ID: "book-2", Narrator: &narrator}
		result := svc.resolveNarratorNames("book-2", book)
		if result != "Charlie" {
			t.Errorf("expected 'Charlie', got %q", result)
		}
	})

	t.Run("empty when no narrators", func(t *testing.T) {
		mockStore := mocks.NewMockStore(t)
		svc := NewRenameService(mockStore)

		mockStore.On("GetBookNarrators", "book-3").Return([]database.BookNarrator{}, nil)

		book := &database.Book{ID: "book-3"}
		result := svc.resolveNarratorNames("book-3", book)
		if result != "" {
			t.Errorf("expected empty, got %q", result)
		}
	})
}

// ---------------------------------------------------------------------------
// isDirectoryPath
// ---------------------------------------------------------------------------

func TestIsDirectoryPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{"empty path", "", false},
		{"m4b file", "/path/to/book.m4b", false},
		{"mp3 file", "/path/to/book.mp3", false},
		{"flac file", "/path/to/book.flac", false},
		{"m4a file", "/path/to/book.M4A", false},                     // case insensitive
		{"non-audio extension", "/path/to/book.txt", false},          // will try stat
		{"no extension (nonexistent)", "/path/to/some_folder", true}, // no ext, doesn't exist
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isDirectoryPath(tt.path)
			if got != tt.want {
				t.Errorf("isDirectoryPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}

	t.Run("actual directory returns true", func(t *testing.T) {
		dir := t.TempDir()
		if !isDirectoryPath(dir) {
			t.Errorf("isDirectoryPath(%q) = false for actual directory", dir)
		}
	})

	t.Run("actual file returns false", func(t *testing.T) {
		tmpDir := t.TempDir()
		f := filepath.Join(tmpDir, "test.txt")
		os.WriteFile(f, []byte("x"), 0644)
		if isDirectoryPath(f) {
			t.Errorf("isDirectoryPath(%q) = true for actual file", f)
		}
	})
}

// ---------------------------------------------------------------------------
// PreviewOrganize (error path)
// ---------------------------------------------------------------------------

func TestPreviewOrganize_BookNotFound(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewPreviewService(mockStore)

	mockStore.On("GetBookByID", "nonexistent").Return(nil, fmt.Errorf("not found"))

	_, err := svc.PreviewOrganize("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent book")
	}
}

// ---------------------------------------------------------------------------
// PreviewRename (error path)
// ---------------------------------------------------------------------------

func TestPreviewRename_BookNotFound(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewRenameService(mockStore)

	mockStore.On("GetBookByID", "gone").Return(nil, fmt.Errorf("not found"))

	_, err := svc.PreviewRename("gone")
	if err == nil {
		t.Fatal("expected error for nonexistent book")
	}
}

// ---------------------------------------------------------------------------
// stringPtr / stringOrDefault helpers
// ---------------------------------------------------------------------------

func TestStringHelpers(t *testing.T) {
	t.Run("stringPtr", func(t *testing.T) {
		p := stringPtr("hello")
		if *p != "hello" {
			t.Errorf("expected 'hello', got %q", *p)
		}
	})

	t.Run("stringOrDefault nil", func(t *testing.T) {
		if got := stringOrDefault(nil, "default"); got != "default" {
			t.Errorf("expected 'default', got %q", got)
		}
	})

	t.Run("stringOrDefault non-nil", func(t *testing.T) {
		s := "value"
		if got := stringOrDefault(&s, "default"); got != "value" {
			t.Errorf("expected 'value', got %q", got)
		}
	})
}

// ---------------------------------------------------------------------------
// GenerateTargetPath / GenerateTargetDirPath public API
// ---------------------------------------------------------------------------

func TestGenerateTargetPath_Public(t *testing.T) {
	tmpDir := t.TempDir()
	org := &Organizer{
		config: &config.Config{
			RootDir:             tmpDir,
			FolderNamingPattern: "{author}",
			FileNamingPattern:   "{title}",
		},
	}
	book := &database.Book{
		Title:    "Foundation",
		FilePath: "/src/foundation.m4b",
		Author:   &database.Author{Name: "Asimov"},
	}

	path, err := org.GenerateTargetPath(book)
	if err != nil {
		t.Fatalf("GenerateTargetPath: %v", err)
	}
	expected := filepath.Join(tmpDir, "Asimov", "Foundation.m4b")
	if path != expected {
		t.Errorf("expected %q, got %q", expected, path)
	}
}

func TestGenerateTargetDirPath(t *testing.T) {
	tmpDir := t.TempDir()
	org := &Organizer{
		config: &config.Config{
			RootDir:             tmpDir,
			FolderNamingPattern: "{author}/{title}",
		},
	}
	book := &database.Book{
		Title:  "Foundation",
		Author: &database.Author{Name: "Asimov"},
	}

	path, err := org.GenerateTargetDirPath(book)
	if err != nil {
		t.Fatalf("GenerateTargetDirPath: %v", err)
	}
	expected := filepath.Join(tmpDir, "Asimov", "Foundation")
	if path != expected {
		t.Errorf("expected %q, got %q", expected, path)
	}
}

// ---------------------------------------------------------------------------
// MoveBookFile
// ---------------------------------------------------------------------------

func TestMoveBookFile_SamePath(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	err := MoveBookFile(mockStore, "book-1", "/same/path.m4b", "/same/path.m4b", nil)
	if err != nil {
		t.Errorf("expected nil for same path, got %v", err)
	}
}

func TestMoveBookFile_SourceMissing(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	err := MoveBookFile(mockStore, "book-1", "/no/such/file.m4b", "/dst/file.m4b", nil)
	if err == nil {
		t.Fatal("expected error for missing source")
	}
}

func TestMoveBookFile_DestExists(t *testing.T) {
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "src.m4b")
	dst := filepath.Join(tmpDir, "dst.m4b")
	os.WriteFile(src, []byte("a"), 0644)
	os.WriteFile(dst, []byte("b"), 0644)

	mockStore := mocks.NewMockStore(t)
	err := MoveBookFile(mockStore, "book-1", src, dst, nil)
	if err == nil {
		t.Fatal("expected error when destination exists")
	}
}

func TestMoveBookFile_Success(t *testing.T) {
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "src.m4b")
	dst := filepath.Join(tmpDir, "out", "dst.m4b")
	os.WriteFile(src, []byte("content"), 0644)

	mockStore := mocks.NewMockStore(t)
	// With extraUpdates=nil, MoveBookFile hydrates the full row before the
	// write so it never total-wipes the record (Author/Series/...). The write
	// must carry the new FilePath on the hydrated row.
	mockStore.On("GetBookByID", "book-1").
		Return(&database.Book{ID: "book-1", Title: "Kept Title"}, nil)
	mockStore.On("UpdateBook", "book-1", mock.AnythingOfType("*database.Book")).
		Run(func(args mock.Arguments) {
			book := args.Get(1).(*database.Book)
			if book.FilePath != dst {
				t.Errorf("expected FilePath = %q, got %q", dst, book.FilePath)
			}
			if book.Title != "Kept Title" {
				t.Errorf("hydrated row not written: Title = %q, want %q", book.Title, "Kept Title")
			}
		}).
		Return(&database.Book{}, nil)

	err := MoveBookFile(mockStore, "book-1", src, dst, nil)
	if err != nil {
		t.Fatalf("MoveBookFile: %v", err)
	}
	// Verify file moved
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("destination not created: %v", err)
	}
}

func TestMoveBookFile_DBUpdateFails_Rollback(t *testing.T) {
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "src.m4b")
	dst := filepath.Join(tmpDir, "out", "dst.m4b")
	os.WriteFile(src, []byte("content"), 0644)

	mockStore := mocks.NewMockStore(t)
	mockStore.On("GetBookByID", "book-1").
		Return(&database.Book{ID: "book-1"}, nil)
	mockStore.On("UpdateBook", "book-1", mock.AnythingOfType("*database.Book")).Return(nil, fmt.Errorf("db error"))

	err := MoveBookFile(mockStore, "book-1", src, dst, nil)
	if err == nil {
		t.Fatal("expected error from DB failure")
	}
	// Source should be restored
	if _, err := os.Stat(src); err != nil {
		t.Errorf("source not restored after rollback: %v", err)
	}
}

func TestMoveBookFile_WithExtraUpdates(t *testing.T) {
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "src.m4b")
	dst := filepath.Join(tmpDir, "out", "dst.m4b")
	os.WriteFile(src, []byte("content"), 0644)

	mockStore := mocks.NewMockStore(t)
	mockStore.On("UpdateBook", "book-1", mock.AnythingOfType("*database.Book")).
		Run(func(args mock.Arguments) {
			book := args.Get(1).(*database.Book)
			if book.FilePath != dst {
				t.Errorf("expected FilePath = %q, got %q", dst, book.FilePath)
			}
			if book.Title != "Updated Title" {
				t.Errorf("expected Title = 'Updated Title', got %q", book.Title)
			}
		}).
		Return(&database.Book{}, nil)

	extra := &database.Book{Title: "Updated Title"}
	err := MoveBookFile(mockStore, "book-1", src, dst, extra)
	if err != nil {
		t.Fatalf("MoveBookFile: %v", err)
	}
}

// ---------------------------------------------------------------------------
// EnsureUnderRoot
// ---------------------------------------------------------------------------

func TestEnsureUnderRoot(t *testing.T) {
	t.Run("valid path", func(t *testing.T) {
		if err := ensureUnderRoot("/lib/books/author/title.m4b", "/lib/books"); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("path traversal blocked", func(t *testing.T) {
		if err := ensureUnderRoot("/lib/books/../../../etc/passwd", "/lib/books"); err == nil {
			t.Error("expected error for path traversal")
		}
	})

	t.Run("path equals root", func(t *testing.T) {
		if err := ensureUnderRoot("/lib/books", "/lib/books"); err != nil {
			t.Errorf("unexpected error for path == root: %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// RenameService.moveFile
// ---------------------------------------------------------------------------

func TestMoveFile(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewRenameService(mockStore)

	t.Run("same filesystem rename", func(t *testing.T) {
		tmpDir := t.TempDir()
		src := filepath.Join(tmpDir, "src.m4b")
		dst := filepath.Join(tmpDir, "dst.m4b")
		os.WriteFile(src, []byte("data"), 0644)

		err := svc.moveFile(src, dst)
		if err != nil {
			t.Fatalf("moveFile: %v", err)
		}
		if _, err := os.Stat(dst); err != nil {
			t.Errorf("dest not found: %v", err)
		}
		if _, err := os.Stat(src); !os.IsNotExist(err) {
			t.Error("source should not exist")
		}
	})

	t.Run("creates destination directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		src := filepath.Join(tmpDir, "src.m4b")
		dst := filepath.Join(tmpDir, "sub", "dir", "dst.m4b")
		os.WriteFile(src, []byte("data"), 0644)

		err := svc.moveFile(src, dst)
		if err != nil {
			t.Fatalf("moveFile: %v", err)
		}
		if _, err := os.Stat(dst); err != nil {
			t.Errorf("dest not found: %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// RenameService.hardlinkOrCopy
// ---------------------------------------------------------------------------

func TestHardlinkOrCopy(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewRenameService(mockStore)

	t.Run("creates file at destination", func(t *testing.T) {
		tmpDir := t.TempDir()
		src := filepath.Join(tmpDir, "src.m4b")
		dst := filepath.Join(tmpDir, "sub", "dst.m4b")
		os.WriteFile(src, []byte("content"), 0644)

		err := svc.hardlinkOrCopy(src, dst)
		if err != nil {
			t.Fatalf("hardlinkOrCopy: %v", err)
		}
		// Verify destination exists with correct content
		data, err := os.ReadFile(dst)
		if err != nil {
			t.Fatalf("read dst: %v", err)
		}
		if string(data) != "content" {
			t.Errorf("content = %q, want 'content'", data)
		}
		// Source should still exist (never deleted)
		if _, err := os.Stat(src); err != nil {
			t.Errorf("source should still exist: %v", err)
		}
	})

	t.Run("source not found", func(t *testing.T) {
		tmpDir := t.TempDir()
		err := svc.hardlinkOrCopy("/no/such/file.m4b", filepath.Join(tmpDir, "dst.m4b"))
		if err == nil {
			t.Fatal("expected error for missing source")
		}
	})
}

// ---------------------------------------------------------------------------
// RenameService.copyAndDelete
// ---------------------------------------------------------------------------

func TestCopyAndDelete(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewRenameService(mockStore)

	t.Run("copies and removes source", func(t *testing.T) {
		tmpDir := t.TempDir()
		src := filepath.Join(tmpDir, "src.m4b")
		dst := filepath.Join(tmpDir, "dst.m4b")
		os.WriteFile(src, []byte("audio data"), 0644)

		err := svc.copyAndDelete(src, dst)
		if err != nil {
			t.Fatalf("copyAndDelete: %v", err)
		}
		data, err := os.ReadFile(dst)
		if err != nil {
			t.Fatalf("read dst: %v", err)
		}
		if string(data) != "audio data" {
			t.Errorf("content = %q", data)
		}
		if _, err := os.Stat(src); !os.IsNotExist(err) {
			t.Error("source should be deleted after copy")
		}
	})

	t.Run("source not found", func(t *testing.T) {
		tmpDir := t.TempDir()
		err := svc.copyAndDelete("/no/such.m4b", filepath.Join(tmpDir, "dst.m4b"))
		if err == nil {
			t.Fatal("expected error for missing source")
		}
	})
}

// ---------------------------------------------------------------------------
// NewService and setters
// ---------------------------------------------------------------------------

func TestNewService(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewService(mockStore)
	if svc == nil {
		t.Fatal("NewService returned nil")
	}
	// Verify defaults
	if p := svc.DiscoverITunesLibraryPath(); p != "" {
		t.Errorf("default DiscoverITunesLibraryPath should return empty, got %q", p)
	}
	if p := svc.ComputeITunesPath("/file"); p != "" {
		t.Errorf("default ComputeITunesPath should return empty, got %q", p)
	}
	r, err := svc.FetchMetadataForBook(context.Background(), "any")
	if r != nil || err != nil {
		t.Errorf("default FetchMetadataForBook should return nil/nil, got %v/%v", r, err)
	}
}

func TestServiceSetters(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewService(mockStore)

	t.Run("SetOrganizeHooks", func(t *testing.T) {
		hooks := &testOrganizeHooks{}
		svc.SetOrganizeHooks(hooks)
		if svc.organizeHooks == nil {
			t.Error("hooks should be set")
		}
	})

	t.Run("SetWriteBackBatcher", func(t *testing.T) {
		// Just verify it doesn't panic
		svc.SetWriteBackBatcher(nil)
	})

	t.Run("ScanEnqueuer", func(t *testing.T) {
		// Verify ScanEnqueuer can be set without panic
		svc.ScanEnqueuer = nil
	})

	t.Run("newOrganizer with hooks", func(t *testing.T) {
		hooks := &testOrganizeHooks{}
		svc.SetOrganizeHooks(hooks)
		org := svc.newOrganizer()
		if org == nil {
			t.Fatal("newOrganizer returned nil")
		}
		if org.hooks == nil {
			t.Error("hooks should be propagated to organizer")
		}
	})

	t.Run("newOrganizer without hooks", func(t *testing.T) {
		svc.SetOrganizeHooks(nil)
		org := svc.newOrganizer()
		if org == nil {
			t.Fatal("newOrganizer returned nil")
		}
	})
}

// ---------------------------------------------------------------------------
// FileRenameEntry / FilePipelineResult / RelocateRequest types
// ---------------------------------------------------------------------------

func TestFileRenameEntry_Fields(t *testing.T) {
	entry := FileRenameEntry{
		SegmentID:  "seg-1",
		SourcePath: "/old/file.m4b",
		TargetPath: "/new/file.m4b",
	}
	if entry.SegmentID != "seg-1" || entry.SourcePath != "/old/file.m4b" || entry.TargetPath != "/new/file.m4b" {
		t.Error("fields not set correctly")
	}
}

func TestPreviewStepTypes(t *testing.T) {
	step := PreviewStep{
		Action:      "copy",
		Description: "Copy file",
		From:        "/src",
		To:          "/dst",
	}
	if step.Action != "copy" {
		t.Error("action not set")
	}

	resp := PreviewResponse{
		NeedsCopy:   true,
		NeedsRename: false,
	}
	if !resp.NeedsCopy {
		t.Error("NeedsCopy should be true")
	}
}

// ---------------------------------------------------------------------------
// PreviewRename (success path)
// ---------------------------------------------------------------------------

func TestPreviewRename_Success(t *testing.T) {
	tmpDir := t.TempDir()
	config.AppConfig = config.Config{
		RootDir:             tmpDir,
		FolderNamingPattern: "{author}",
		FileNamingPattern:   "{title}",
	}

	mockStore := mocks.NewMockStore(t)
	svc := NewRenameService(mockStore)

	book := &database.Book{
		ID:       "book-1",
		Title:    "Foundation",
		FilePath: "/old/foundation.m4b",
		Author:   &database.Author{Name: "Asimov"},
	}
	mockStore.On("GetBookByID", "book-1").Return(book, nil)
	mockStore.On("GetBookNarrators", "book-1").Return([]database.BookNarrator{}, nil)

	preview, err := svc.PreviewRename("book-1")
	if err != nil {
		t.Fatalf("PreviewRename: %v", err)
	}
	if preview.BookID != "book-1" {
		t.Errorf("BookID = %q", preview.BookID)
	}
	if preview.CurrentPath != "/old/foundation.m4b" {
		t.Errorf("CurrentPath = %q", preview.CurrentPath)
	}
	expected := filepath.Join(tmpDir, "Asimov", "Foundation.m4b")
	if preview.ProposedPath != expected {
		t.Errorf("ProposedPath = %q, want %q", preview.ProposedPath, expected)
	}
	if len(preview.TagChanges) == 0 {
		t.Error("expected tag changes")
	}
}

// ---------------------------------------------------------------------------
// OrganizeBookDirectory
// ---------------------------------------------------------------------------

func TestOrganizeBookDirectory_NilBook_Unit(t *testing.T) {
	org := &Organizer{config: &config.Config{}}
	_, _, err := org.OrganizeBookDirectory(nil, []string{"/a.m4b"})
	if err == nil {
		t.Fatal("expected error for nil book")
	}
}

func TestOrganizeBookDirectory_EmptySegments_Unit(t *testing.T) {
	org := &Organizer{config: &config.Config{}}
	book := &database.Book{Title: "Test", Author: &database.Author{Name: "A"}}
	_, _, err := org.OrganizeBookDirectory(book, nil)
	if err == nil {
		t.Fatal("expected error for empty segments")
	}
}

// ---------------------------------------------------------------------------
// cleanupTempFiles
// ---------------------------------------------------------------------------

func TestCleanupTempFiles_NilConfig(t *testing.T) {
	org := &Organizer{config: nil}
	err := org.cleanupTempFiles()
	if err != nil {
		t.Errorf("expected nil error for nil config, got %v", err)
	}
}

func TestCleanupTempFiles_EmptyRootDir(t *testing.T) {
	org := &Organizer{config: &config.Config{RootDir: ""}}
	err := org.cleanupTempFiles()
	if err != nil {
		t.Errorf("expected nil error for empty rootdir, got %v", err)
	}
}

func TestCleanupTempFiles_RemovesTmpFiles(t *testing.T) {
	tmpDir := t.TempDir()
	// Create a .tmp file
	tmpFile := filepath.Join(tmpDir, "leftover.tmp")
	os.WriteFile(tmpFile, []byte("x"), 0644)
	normalFile := filepath.Join(tmpDir, "keep.m4b")
	os.WriteFile(normalFile, []byte("y"), 0644)

	org := &Organizer{config: &config.Config{RootDir: tmpDir}}
	err := org.cleanupTempFiles()
	if err != nil {
		t.Fatalf("cleanupTempFiles: %v", err)
	}
	// .tmp should be removed
	if _, err := os.Stat(tmpFile); !os.IsNotExist(err) {
		t.Error("tmp file should be removed")
	}
	// normal file should remain
	if _, err := os.Stat(normalFile); err != nil {
		t.Error("normal file should remain")
	}
}

// ---------------------------------------------------------------------------
// organizeFile (low-level)
// ---------------------------------------------------------------------------

func TestOrganizeFile_UnknownStrategy(t *testing.T) {
	org := &Organizer{config: &config.Config{OrganizationStrategy: "banana"}}
	_, err := org.organizeFile("/src", "/dst")
	if err == nil {
		t.Fatal("expected error for unknown strategy")
	}
}

// ---------------------------------------------------------------------------
// OrganizeBookDirectory with copy strategy
// ---------------------------------------------------------------------------

func TestOrganizeBookDirectory_CopyStrategy(t *testing.T) {
	tmpDir := t.TempDir()
	srcDir := filepath.Join(tmpDir, "source")
	os.MkdirAll(srcDir, 0755)

	// Create source files
	f1 := filepath.Join(srcDir, "ch01.m4b")
	f2 := filepath.Join(srcDir, "ch02.m4b")
	os.WriteFile(f1, []byte("chapter1"), 0644)
	os.WriteFile(f2, []byte("chapter2"), 0644)

	dstDir := filepath.Join(tmpDir, "library")
	org := &Organizer{
		config: &config.Config{
			RootDir:              dstDir,
			FolderNamingPattern:  "{author}/{title}",
			OrganizationStrategy: "copy",
		},
	}

	book := &database.Book{
		Title:  "Test Book",
		Author: &database.Author{Name: "Author"},
	}

	targetDir, pathMap, err := org.OrganizeBookDirectory(book, []string{f1, f2})
	if err != nil {
		t.Fatalf("OrganizeBookDirectory: %v", err)
	}
	if targetDir == "" {
		t.Fatal("expected non-empty target dir")
	}
	if len(pathMap) != 2 {
		t.Errorf("expected 2 path mappings, got %d", len(pathMap))
	}
	// Check files were copied
	for _, dstPath := range pathMap {
		if _, err := os.Stat(dstPath); err != nil {
			t.Errorf("destination file not found: %v", err)
		}
	}
}

// ---------------------------------------------------------------------------
// OrganizeBookDirectory idempotent (already at target)
// ---------------------------------------------------------------------------

func TestOrganizeBookDirectory_AlreadyAtTarget(t *testing.T) {
	tmpDir := t.TempDir()
	// Create the target directory structure
	targetDir := filepath.Join(tmpDir, "Author", "Title")
	os.MkdirAll(targetDir, 0755)
	f1 := filepath.Join(targetDir, "ch01.m4b")
	os.WriteFile(f1, []byte("data"), 0644)

	org := &Organizer{
		config: &config.Config{
			RootDir:              tmpDir,
			FolderNamingPattern:  "{author}/{title}",
			OrganizationStrategy: "copy",
		},
	}

	book := &database.Book{
		Title:  "Title",
		Author: &database.Author{Name: "Author"},
	}

	dir, pathMap, err := org.OrganizeBookDirectory(book, []string{f1})
	if err != nil {
		t.Fatalf("OrganizeBookDirectory: %v", err)
	}
	if dir == "" {
		t.Fatal("expected non-empty target dir")
	}
	// Source and dest should be the same
	if pathMap[f1] != f1 {
		t.Errorf("expected same path mapping for already-at-target file, got %q -> %q", f1, pathMap[f1])
	}
}

// ---------------------------------------------------------------------------
// GenerateTargetDirPath error
// ---------------------------------------------------------------------------

func TestGenerateTargetDirPath_Error(t *testing.T) {
	org := &Organizer{
		config: &config.Config{
			RootDir:             "/tmp",
			FolderNamingPattern: "{unknown_field}",
		},
	}
	book := &database.Book{Title: "Test"}
	_, err := org.GenerateTargetDirPath(book)
	if err == nil {
		t.Fatal("expected error for bad folder pattern")
	}
}

// ---------------------------------------------------------------------------
// FormatPath edge cases
// ---------------------------------------------------------------------------

func TestFormatPath_TrackFormatSpec(t *testing.T) {
	// Test {track:03d} format spec in FormatPath
	vars := FormatVars{
		Author:      "Auth",
		Title:       "Book",
		Track:       5,
		TotalTracks: 100,
		TrackTitle:  "Ch5",
		Ext:         "mp3",
	}
	got := FormatPath("{author}/{track:03d} - {track_title}.{ext}", vars)
	if got != "Auth/005 - Ch5.mp3" {
		t.Errorf("FormatPath with format spec = %q", got)
	}
}

// ---------------------------------------------------------------------------
// PreviewOrganize (success path with steps)
// ---------------------------------------------------------------------------

func TestPreviewOrganize_Success(t *testing.T) {
	tmpDir := t.TempDir()
	config.AppConfig = config.Config{
		RootDir:              tmpDir,
		FolderNamingPattern:  "{author}",
		FileNamingPattern:    "{title}",
		OrganizationStrategy: "copy",
	}

	mockStore := mocks.NewMockStore(t)
	svc := NewPreviewService(mockStore)

	book := &database.Book{
		ID:       "book-1",
		Title:    "Foundation",
		FilePath: "/external/foundation.m4b",
		Author:   &database.Author{Name: "Asimov"},
	}
	mockStore.On("GetBookByID", "book-1").Return(book, nil)
	mockStore.On("GetBookFiles", "book-1").Return([]database.BookFile{
		{ID: "bf1", BookID: "book-1", FilePath: "/external/foundation.m4b", Format: "m4b"},
	}, nil)
	mockStore.On("GetBookNarrators", "book-1").Return([]database.BookNarrator{}, nil)

	preview, err := svc.PreviewOrganize("book-1")
	if err != nil {
		t.Fatalf("PreviewOrganize: %v", err)
	}
	if preview == nil {
		t.Fatal("expected non-nil preview")
	}
	if !preview.NeedsCopy {
		t.Error("expected NeedsCopy = true for external file")
	}
	if preview.CurrentPath != "/external/foundation.m4b" {
		t.Errorf("CurrentPath = %q", preview.CurrentPath)
	}
	if preview.BookFileCount != 1 {
		t.Errorf("BookFileCount = %d", preview.BookFileCount)
	}
	// Should have copy + write_tags steps at minimum
	foundCopy := false
	foundTags := false
	for _, step := range preview.Steps {
		if step.Action == "copy" {
			foundCopy = true
		}
		if step.Action == "write_tags" {
			foundTags = true
		}
	}
	if !foundCopy {
		t.Error("expected copy step")
	}
	if !foundTags {
		t.Error("expected write_tags step")
	}
}

func TestPreviewOrganize_AlreadyAtTarget(t *testing.T) {
	tmpDir := t.TempDir()
	// Set up config
	config.AppConfig = config.Config{
		RootDir:              tmpDir,
		FolderNamingPattern:  "{author}",
		FileNamingPattern:    "{title}",
		OrganizationStrategy: "copy",
	}

	// Create the file at its target location
	targetDir := filepath.Join(tmpDir, "Asimov")
	os.MkdirAll(targetDir, 0755)
	targetPath := filepath.Join(targetDir, "Foundation.m4b")
	os.WriteFile(targetPath, []byte("audio"), 0644)

	mockStore := mocks.NewMockStore(t)
	svc := NewPreviewService(mockStore)

	book := &database.Book{
		ID:       "book-1",
		Title:    "Foundation",
		FilePath: targetPath,
		Author:   &database.Author{Name: "Asimov"},
	}
	mockStore.On("GetBookByID", "book-1").Return(book, nil)
	mockStore.On("GetBookFiles", "book-1").Return([]database.BookFile{
		{ID: "bf1", BookID: "book-1", FilePath: targetPath},
	}, nil)
	mockStore.On("GetBookNarrators", "book-1").Return([]database.BookNarrator{}, nil)

	preview, err := svc.PreviewOrganize("book-1")
	if err != nil {
		t.Fatalf("PreviewOrganize: %v", err)
	}
	if preview.NeedsCopy {
		t.Error("should not need copy when already at target")
	}
	if preview.NeedsRename {
		t.Error("should not need rename when already at target")
	}
}

func TestPreviewOrganize_InRootNeedsRename(t *testing.T) {
	tmpDir := t.TempDir()
	config.AppConfig = config.Config{
		RootDir:              tmpDir,
		FolderNamingPattern:  "{author}",
		FileNamingPattern:    "{title}",
		OrganizationStrategy: "copy",
	}

	// File is in root but with wrong name
	wrongPath := filepath.Join(tmpDir, "WrongName.m4b")
	os.WriteFile(wrongPath, []byte("audio"), 0644)

	mockStore := mocks.NewMockStore(t)
	svc := NewPreviewService(mockStore)

	book := &database.Book{
		ID:       "book-2",
		Title:    "Foundation",
		FilePath: wrongPath,
		Author:   &database.Author{Name: "Asimov"},
	}
	mockStore.On("GetBookByID", "book-2").Return(book, nil)
	mockStore.On("GetBookFiles", "book-2").Return([]database.BookFile{
		{ID: "bf1", BookID: "book-2", FilePath: wrongPath},
	}, nil)
	mockStore.On("GetBookNarrators", "book-2").Return([]database.BookNarrator{}, nil)

	preview, err := svc.PreviewOrganize("book-2")
	if err != nil {
		t.Fatalf("PreviewOrganize: %v", err)
	}
	if preview.NeedsCopy {
		t.Error("should not need copy when in root dir")
	}
	if !preview.NeedsRename {
		t.Error("should need rename when path doesn't match")
	}
	foundRename := false
	for _, step := range preview.Steps {
		if step.Action == "rename" {
			foundRename = true
		}
	}
	if !foundRename {
		t.Error("expected rename step")
	}
}

func TestPreviewOrganize_WithCoverURL(t *testing.T) {
	tmpDir := t.TempDir()
	config.AppConfig = config.Config{
		RootDir:             tmpDir,
		FolderNamingPattern: "{author}",
		FileNamingPattern:   "{title}",
	}

	targetDir := filepath.Join(tmpDir, "Auth")
	os.MkdirAll(targetDir, 0755)
	targetPath := filepath.Join(targetDir, "Book.m4b")
	os.WriteFile(targetPath, []byte("x"), 0644)

	mockStore := mocks.NewMockStore(t)
	svc := NewPreviewService(mockStore)

	coverURL := "https://example.com/cover.jpg"
	book := &database.Book{
		ID:       "book-3",
		Title:    "Book",
		FilePath: targetPath,
		Author:   &database.Author{Name: "Auth"},
		CoverURL: &coverURL,
	}
	mockStore.On("GetBookByID", "book-3").Return(book, nil)
	mockStore.On("GetBookFiles", "book-3").Return([]database.BookFile{}, nil)
	mockStore.On("GetBookNarrators", "book-3").Return([]database.BookNarrator{}, nil)

	preview, err := svc.PreviewOrganize("book-3")
	if err != nil {
		t.Fatalf("PreviewOrganize: %v", err)
	}
	foundCover := false
	for _, step := range preview.Steps {
		if step.Action == "embed_cover" {
			foundCover = true
			if step.CoverURL != coverURL {
				t.Errorf("CoverURL = %q, want %q", step.CoverURL, coverURL)
			}
		}
	}
	if !foundCover {
		t.Error("expected embed_cover step")
	}
}

// ---------------------------------------------------------------------------
// PreviewOrganize with protected path
// ---------------------------------------------------------------------------

func TestPreviewOrganize_ProtectedPath(t *testing.T) {
	tmpDir := t.TempDir()
	config.AppConfig = config.Config{
		RootDir:             tmpDir,
		FolderNamingPattern: "{author}",
		FileNamingPattern:   "{title}",
	}

	mockStore := mocks.NewMockStore(t)
	svc := NewPreviewService(mockStore)
	svc.IsProtectedPath = func(path string) bool { return true }

	book := &database.Book{
		ID:       "book-p",
		Title:    "Protected",
		FilePath: "/itunes/protected.m4b",
		Author:   &database.Author{Name: "Auth"},
	}
	mockStore.On("GetBookByID", "book-p").Return(book, nil)
	mockStore.On("GetBookFiles", "book-p").Return([]database.BookFile{
		{ID: "bf1", BookID: "book-p", FilePath: "/itunes/protected.m4b"},
	}, nil)
	mockStore.On("GetBookNarrators", "book-p").Return([]database.BookNarrator{}, nil)

	preview, err := svc.PreviewOrganize("book-p")
	if err != nil {
		t.Fatalf("PreviewOrganize: %v", err)
	}
	if !preview.IsProtected {
		t.Error("expected IsProtected = true")
	}
	// Should have a warning step
	foundWarning := false
	for _, step := range preview.Steps {
		if step.Action == "warning" {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Error("expected warning step for protected path")
	}
}

// ---------------------------------------------------------------------------
// PreviewOrganize with multi-file protected directory
// ---------------------------------------------------------------------------

func TestPreviewOrganize_MultiFileProtected(t *testing.T) {
	tmpDir := t.TempDir()
	config.AppConfig = config.Config{
		RootDir:             tmpDir,
		FolderNamingPattern: "{author}/{title}",
		FileNamingPattern:   "{title}",
	}

	mockStore := mocks.NewMockStore(t)
	svc := NewPreviewService(mockStore)
	svc.IsProtectedPath = func(path string) bool { return true }

	book := &database.Book{
		ID:       "book-m",
		Title:    "MultiFile",
		FilePath: "/itunes/Author",
		Author:   &database.Author{Name: "Author"},
	}
	mockStore.On("GetBookByID", "book-m").Return(book, nil)
	mockStore.On("GetBookFiles", "book-m").Return([]database.BookFile{
		{ID: "bf1", BookID: "book-m", FilePath: "/itunes/Author/ch1.m4b"},
		{ID: "bf2", BookID: "book-m", FilePath: "/itunes/Author/ch2.m4b"},
	}, nil)
	mockStore.On("GetBookNarrators", "book-m").Return([]database.BookNarrator{}, nil)

	preview, err := svc.PreviewOrganize("book-m")
	if err != nil {
		t.Fatalf("PreviewOrganize: %v", err)
	}
	if !preview.HasBookFiles {
		t.Error("expected HasBookFiles = true")
	}
	if preview.BookFileCount != 2 {
		t.Errorf("BookFileCount = %d, want 2", preview.BookFileCount)
	}
	// Should have flat_itunes_directory warning
	foundFlatWarning := false
	for _, step := range preview.Steps {
		if step.Warning == "flat_itunes_directory" {
			foundFlatWarning = true
		}
	}
	if !foundFlatWarning {
		t.Error("expected flat_itunes_directory warning")
	}
}

// ---------------------------------------------------------------------------
// NewRenameService defaults coverage
// ---------------------------------------------------------------------------

func TestNewRenameService_DefaultResolve(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewRenameService(mockStore)

	book := &database.Book{
		Title:  "Test",
		Author: &database.Author{Name: "A"},
		Series: &database.Series{Name: "S"},
	}
	a, s := svc.ResolveAuthorAndSeriesNames(book)
	if a != "A" || s != "S" {
		t.Errorf("got %q/%q", a, s)
	}
}

// ---------------------------------------------------------------------------
// organizeFile with various strategies
// ---------------------------------------------------------------------------

func TestOrganizeFile_CopyStrategy(t *testing.T) {
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "src.m4b")
	dst := filepath.Join(tmpDir, "dst.m4b")
	os.WriteFile(src, []byte("data"), 0644)

	org := &Organizer{config: &config.Config{OrganizationStrategy: "copy"}}
	method, err := org.organizeFile(src, dst)
	if err != nil {
		t.Fatalf("organizeFile copy: %v", err)
	}
	if method != "copy" {
		t.Errorf("method = %q, want copy", method)
	}
}

func TestOrganizeFile_HardlinkStrategy(t *testing.T) {
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "src.m4b")
	dst := filepath.Join(tmpDir, "dst.m4b")
	os.WriteFile(src, []byte("data"), 0644)

	org := &Organizer{config: &config.Config{OrganizationStrategy: "hardlink"}}
	method, err := org.organizeFile(src, dst)
	if err != nil {
		t.Skipf("hardlink not supported: %v", err)
	}
	if method != "hardlink" {
		t.Errorf("method = %q, want hardlink", method)
	}
}

func TestOrganizeFile_SymlinkStrategy(t *testing.T) {
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "src.m4b")
	dst := filepath.Join(tmpDir, "dst.m4b")
	os.WriteFile(src, []byte("data"), 0644)

	org := &Organizer{config: &config.Config{OrganizationStrategy: "symlink"}}
	method, err := org.organizeFile(src, dst)
	if err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	if method != "symlink" {
		t.Errorf("method = %q, want symlink", method)
	}
}

func TestOrganizeFile_AutoStrategy(t *testing.T) {
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "src.m4b")
	dst := filepath.Join(tmpDir, "dst.m4b")
	os.WriteFile(src, []byte("data"), 0644)

	org := &Organizer{config: &config.Config{OrganizationStrategy: "auto"}}
	method, err := org.organizeFile(src, dst)
	if err != nil {
		t.Fatalf("organizeFile auto: %v", err)
	}
	// Auto will try reflink -> hardlink -> copy; should succeed with one of them
	if method != "reflink" && method != "hardlink" && method != "copy" {
		t.Errorf("method = %q, want reflink/hardlink/copy", method)
	}
}

// ---------------------------------------------------------------------------
// cleanupEmptyParents
// ---------------------------------------------------------------------------

func TestCleanupEmptyParents(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewService(mockStore)

	tmpDir := t.TempDir()
	// Create nested empty dirs
	nested := filepath.Join(tmpDir, "a", "b", "c")
	os.MkdirAll(nested, 0755)

	// Create a no-op logger
	noopLog := &noopLogger{}

	svc.cleanupEmptyParents(nested, tmpDir, noopLog)

	// All empty dirs should be removed
	if _, err := os.Stat(filepath.Join(tmpDir, "a")); !os.IsNotExist(err) {
		t.Error("expected empty parent dirs to be removed")
	}
}

func TestCleanupEmptyParents_NonEmpty(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewService(mockStore)

	tmpDir := t.TempDir()
	nested := filepath.Join(tmpDir, "a", "b")
	os.MkdirAll(nested, 0755)
	// Put a file in "a" so it's not empty
	os.WriteFile(filepath.Join(tmpDir, "a", "file.txt"), []byte("x"), 0644)

	noopLog := &noopLogger{}
	svc.cleanupEmptyParents(nested, tmpDir, noopLog)

	// "b" should be removed but "a" should remain (has file)
	if _, err := os.Stat(filepath.Join(tmpDir, "a")); err != nil {
		t.Error("non-empty dir should remain")
	}
	if _, err := os.Stat(nested); !os.IsNotExist(err) {
		t.Error("empty nested dir should be removed")
	}
}

// noopLogger implements logger.Logger for tests
type noopLogger struct{}

func (l *noopLogger) Trace(string, ...any)              {}
func (l *noopLogger) Debug(string, ...any)              {}
func (l *noopLogger) Info(string, ...any)               {}
func (l *noopLogger) Warn(string, ...any)               {}
func (l *noopLogger) Error(string, ...any)              {}
func (l *noopLogger) UpdateProgress(int, int, string)   {}
func (l *noopLogger) RecordChange(change logger.Change) {}
func (l *noopLogger) ChangeCounters() map[string]int    { return nil }
func (l *noopLogger) IsCanceled() bool                  { return false }
func (l *noopLogger) With(string) logger.Logger         { return l }

// ---------------------------------------------------------------------------
// bookNeedsReOrganize
// ---------------------------------------------------------------------------

func TestBookNeedsReOrganize_FileAtCorrectPath(t *testing.T) {
	tmpDir := t.TempDir()
	config.AppConfig = config.Config{
		RootDir:             tmpDir,
		FolderNamingPattern: "{author}",
		FileNamingPattern:   "{title}",
	}

	mockStore := mocks.NewMockStore(t)
	svc := NewService(mockStore)
	noopLog := &noopLogger{}

	// Book already at correct path
	correctPath := filepath.Join(tmpDir, "Asimov", "Foundation.m4b")
	book := &database.Book{
		Title:    "Foundation",
		FilePath: correctPath,
		Author:   &database.Author{Name: "Asimov"},
	}

	needs, err := svc.bookNeedsReOrganize(book, noopLog)
	if err != nil {
		t.Fatalf("bookNeedsReOrganize: %v", err)
	}
	if needs {
		t.Error("should not need re-organize when at correct path")
	}
}

func TestBookNeedsReOrganize_FileAtWrongPath(t *testing.T) {
	tmpDir := t.TempDir()
	config.AppConfig = config.Config{
		RootDir:             tmpDir,
		FolderNamingPattern: "{author}",
		FileNamingPattern:   "{title}",
	}

	mockStore := mocks.NewMockStore(t)
	svc := NewService(mockStore)
	noopLog := &noopLogger{}

	book := &database.Book{
		Title:    "Foundation",
		FilePath: filepath.Join(tmpDir, "wrong", "path.m4b"),
		Author:   &database.Author{Name: "Asimov"},
	}

	needs, err := svc.bookNeedsReOrganize(book, noopLog)
	if err != nil {
		t.Fatalf("bookNeedsReOrganize: %v", err)
	}
	if !needs {
		t.Error("should need re-organize when at wrong path")
	}
}

func TestBookNeedsReOrganize_DirectoryBook(t *testing.T) {
	tmpDir := t.TempDir()
	config.AppConfig = config.Config{
		RootDir:             tmpDir,
		FolderNamingPattern: "{author}/{title}",
	}

	mockStore := mocks.NewMockStore(t)
	svc := NewService(mockStore)
	noopLog := &noopLogger{}

	// Directory book (no audio extension)
	correctDir := filepath.Join(tmpDir, "Asimov", "Foundation")
	book := &database.Book{
		Title:    "Foundation",
		FilePath: correctDir,
		Author:   &database.Author{Name: "Asimov"},
	}

	needs, err := svc.bookNeedsReOrganize(book, noopLog)
	if err != nil {
		t.Fatalf("bookNeedsReOrganize: %v", err)
	}
	if needs {
		t.Error("should not need re-organize when dir at correct path")
	}
}

// ---------------------------------------------------------------------------
// OrganizeBook with directory source
// ---------------------------------------------------------------------------

func TestOrganizeBook_DirectorySourceError(t *testing.T) {
	tmpDir := t.TempDir()
	dirPath := filepath.Join(tmpDir, "bookdir")
	os.MkdirAll(dirPath, 0755)

	org := &Organizer{config: &config.Config{
		RootDir:              tmpDir,
		FolderNamingPattern:  "{title}",
		FileNamingPattern:    "{title}",
		OrganizationStrategy: "copy",
	}}
	book := &database.Book{
		Title:    "Test",
		FilePath: dirPath,
	}

	_, _, err := org.OrganizeBook(book)
	if err == nil {
		t.Fatal("expected error for directory source")
	}
}

// ---------------------------------------------------------------------------
// SetHooks
// ---------------------------------------------------------------------------

func TestSetHooks(t *testing.T) {
	org := &Organizer{config: &config.Config{}}

	if org.hooks != nil {
		t.Error("hooks should start nil")
	}

	hooks := &testOrganizeHooks{}
	org.SetHooks(hooks)
	if org.hooks == nil {
		t.Error("hooks should be set")
	}

	org.SetHooks(nil)
	if org.hooks != nil {
		t.Error("hooks should be nil after unsetting")
	}
}
