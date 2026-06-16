// file: internal/server/organize_service_regression_test.go
// version: 1.1.0
// guid: a1b2c3d4-e5f6-7890-abcd-organize-regr

package server

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Regression: organize must not mark books as organized when no files copied
// (Bug: 9,366 books falsely marked organized because organizeDirectoryBook
// returned success even when all source files were missing)
// ---------------------------------------------------------------------------

func TestOrganizeDirectoryBook_AllSourceFilesMissing(t *testing.T) {
	rootDir := t.TempDir()
	importDir := t.TempDir()
	// Don't create any actual source files — they're all "missing"

	mockDB := &database.MockStore{
		GetBookFilesFunc: func(bookID string) ([]database.BookFile, error) {
			return []database.BookFile{
				{ID: "f1", BookID: bookID, FilePath: filepath.Join(importDir, "ch01.mp3")},
				{ID: "f2", BookID: bookID, FilePath: filepath.Join(importDir, "ch02.mp3")},
			}, nil
		},
	}

	cfg := &config.Config{
		RootDir:              rootDir,
		OrganizationStrategy: "copy",
		FolderNamingPattern:  "{author}/{title}",
		FileNamingPattern:    "{title}",
	}
	org := organizer.NewOrganizer(cfg)
	svc := NewOrganizeService(mockDB)
	testLog := logger.New("test")

	book := &database.Book{
		ID:       "book-1",
		Title:    "Ghost Book",
		FilePath: importDir,
		Format:   "mp3",
		Author:   &database.Author{Name: "Ghost Author"},
	}

	_, err := svc.OrganizeDirectoryBook(org, book, testLog)
	assert.Error(t, err, "should fail when all source files are missing")
	assert.Contains(t, err.Error(), "all source files missing")
}

func TestOrganizeDirectoryBook_NoBookFiles(t *testing.T) {
	rootDir := t.TempDir()
	mockDB := &database.MockStore{
		GetBookFilesFunc: func(bookID string) ([]database.BookFile, error) {
			return nil, nil // no book_files at all
		},
	}

	cfg := &config.Config{
		RootDir:              rootDir,
		OrganizationStrategy: "copy",
		FolderNamingPattern:  "{author}/{title}",
		FileNamingPattern:    "{title}",
	}
	org := organizer.NewOrganizer(cfg)
	svc := NewOrganizeService(mockDB)
	testLog := logger.New("test")

	book := &database.Book{
		ID:       "book-2",
		Title:    "Empty Book",
		FilePath: "/some/dir",
		Format:   "mp3",
		Author:   &database.Author{Name: "No Files Author"},
	}

	_, err := svc.OrganizeDirectoryBook(org, book, testLog)
	assert.Error(t, err, "should fail with no book_files")
	assert.Contains(t, err.Error(), "no segments tracked")
}

func TestOrganizeDirectoryBook_AllBookFilesMarkedMissing(t *testing.T) {
	rootDir := t.TempDir()
	mockDB := &database.MockStore{
		GetBookFilesFunc: func(bookID string) ([]database.BookFile, error) {
			return []database.BookFile{
				{ID: "f1", BookID: bookID, FilePath: "/some/ch01.mp3", Missing: true},
				{ID: "f2", BookID: bookID, FilePath: "/some/ch02.mp3", Missing: true},
			}, nil
		},
	}

	cfg := &config.Config{
		RootDir:              rootDir,
		OrganizationStrategy: "copy",
		FolderNamingPattern:  "{author}/{title}",
		FileNamingPattern:    "{title}",
	}
	org := organizer.NewOrganizer(cfg)
	svc := NewOrganizeService(mockDB)
	testLog := logger.New("test")

	book := &database.Book{
		ID:       "book-3",
		Title:    "All Missing",
		FilePath: "/some",
		Format:   "mp3",
		Author:   &database.Author{Name: "Missing Author"},
	}

	_, err := svc.OrganizeDirectoryBook(org, book, testLog)
	assert.Error(t, err, "should fail when all book_files are marked missing")
	assert.Contains(t, err.Error(), "marked missing")
}

func TestOrganizeDirectoryBook_SuccessWithRealFiles(t *testing.T) {
	rootDir := t.TempDir()
	importDir := t.TempDir()

	// Create actual source files
	require.NoError(t, os.WriteFile(filepath.Join(importDir, "ch01.mp3"), []byte("audio-data-1"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(importDir, "ch02.mp3"), []byte("audio-data-2"), 0644))

	mockDB := &database.MockStore{
		GetBookFilesFunc: func(bookID string) ([]database.BookFile, error) {
			return []database.BookFile{
				{ID: "f1", BookID: bookID, FilePath: filepath.Join(importDir, "ch01.mp3")},
				{ID: "f2", BookID: bookID, FilePath: filepath.Join(importDir, "ch02.mp3")},
			}, nil
		},
	}

	cfg := &config.Config{
		RootDir:              rootDir,
		OrganizationStrategy: "copy",
		FolderNamingPattern:  "{author}/{title}",
		FileNamingPattern:    "{title}",
	}
	org := organizer.NewOrganizer(cfg)
	svc := NewOrganizeService(mockDB)
	testLog := logger.New("test")

	book := &database.Book{
		ID:       "book-4",
		Title:    "Real Book",
		FilePath: importDir,
		Format:   "mp3",
		Author:   &database.Author{Name: "Real Author"},
	}

	targetDir, err := svc.OrganizeDirectoryBook(org, book, testLog)
	require.NoError(t, err)
	assert.NotEmpty(t, targetDir)
	assert.DirExists(t, targetDir)

	// Verify files actually exist in target
	entries, _ := os.ReadDir(targetDir)
	assert.Equal(t, 2, len(entries), "both files should be copied to target")
}

func TestOrganizeDirectoryBook_PartialMissing(t *testing.T) {
	rootDir := t.TempDir()
	importDir := t.TempDir()

	// Only create one of two source files
	require.NoError(t, os.WriteFile(filepath.Join(importDir, "ch01.mp3"), []byte("audio-data-1"), 0644))
	// ch02.mp3 intentionally NOT created

	mockDB := &database.MockStore{
		GetBookFilesFunc: func(bookID string) ([]database.BookFile, error) {
			return []database.BookFile{
				{ID: "f1", BookID: bookID, FilePath: filepath.Join(importDir, "ch01.mp3")},
				{ID: "f2", BookID: bookID, FilePath: filepath.Join(importDir, "ch02.mp3")}, // missing
			}, nil
		},
	}

	cfg := &config.Config{
		RootDir:              rootDir,
		OrganizationStrategy: "copy",
		FolderNamingPattern:  "{author}/{title}",
		FileNamingPattern:    "{title}",
	}
	org := organizer.NewOrganizer(cfg)
	svc := NewOrganizeService(mockDB)
	testLog := logger.New("test")

	book := &database.Book{
		ID:       "book-5",
		Title:    "Partial Book",
		FilePath: importDir,
		Format:   "mp3",
		Author:   &database.Author{Name: "Partial Author"},
	}

	// Should succeed with partial files (at least one copied)
	targetDir, err := svc.OrganizeDirectoryBook(org, book, testLog)
	require.NoError(t, err)
	assert.NotEmpty(t, targetDir)

	// Only one file should exist in target
	entries, _ := os.ReadDir(targetDir)
	assert.Equal(t, 1, len(entries), "only the existing file should be copied")
}

// ---------------------------------------------------------------------------
// Regression: createOrganizedVersion must recompute itunes_path
// (Bug: organized copies inherited stale W:/itunes/... path from original)
// ---------------------------------------------------------------------------

func TestCreateOrganizedVersion_RecomputesITunesPath(t *testing.T) {
	rootDir := t.TempDir()

	// Set up path mappings so computeITunesPath works
	oldMappings := config.AppConfig.ITunes.PathMappings
	config.AppConfig.ITunes.PathMappings = []config.ITunesPathMap{
		{From: "W:/audiobook-organizer", To: rootDir},
	}
	defer func() { config.AppConfig.ITunes.PathMappings = oldMappings }()

	var createdFiles []database.BookFile
	var mu sync.Mutex
	isPrimary := false
	mockDB := &database.MockStore{
		GetBookFilesFunc: func(bookID string) ([]database.BookFile, error) {
			return []database.BookFile{
				{
					ID:                 "bf1",
					BookID:             "original-book",
					FilePath:           "/mnt/bigdata/books/itunes/iTunes Media/Audiobooks/Author/book.m4b",
					ITunesPath:         "file://localhost/W:/itunes/iTunes%20Media/Audiobooks/Author/book.m4b",
					ITunesPersistentID: "DEADBEEF01020304",
				},
			}, nil
		},
		CreateBookFunc: func(book *database.Book) (*database.Book, error) {
			return book, nil
		},
		UpdateBookFunc: func(id string, book *database.Book) (*database.Book, error) {
			return book, nil
		},
		MarkNeedsRescanFunc: func(bookID string) error { return nil },
		CreateBookFileFunc: func(file *database.BookFile) error {
			mu.Lock()
			createdFiles = append(createdFiles, *file)
			mu.Unlock()
			return nil
		},
	}

	cfg := &config.Config{
		RootDir:              rootDir,
		OrganizationStrategy: "copy",
		FolderNamingPattern:  "{author}/{title}",
		FileNamingPattern:    "{title}",
	}
	org := organizer.NewOrganizer(cfg)
	svc := NewOrganizeService(mockDB)
	testLog := logger.New("test")

	organizedPath := filepath.Join(rootDir, "Author", "Book Title")
	require.NoError(t, os.MkdirAll(organizedPath, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(organizedPath, "book.m4b"), []byte("data"), 0644))

	book := &database.Book{
		ID:               "original-book",
		Title:            "Book Title",
		FilePath:         "/mnt/bigdata/books/itunes/iTunes Media/Audiobooks/Author",
		Format:           "m4b",
		IsPrimaryVersion: &isPrimary,
		Author:           &database.Author{Name: "Author"},
	}

	created, err := svc.CreateOrganizedVersion(org, book, organizedPath, true, "op-1", testLog)
	require.NoError(t, err)
	assert.NotNil(t, created)

	// The critical assertion: created book_files should NOT have the old iTunes path
	require.Len(t, createdFiles, 1)
	assert.NotContains(t, createdFiles[0].ITunesPath, "itunes/iTunes%20Media",
		"organized copy should NOT keep the old iTunes path")
	// Should have the new audiobook-organizer path
	if createdFiles[0].ITunesPath != "" {
		assert.Contains(t, createdFiles[0].ITunesPath, "audiobook-organizer",
			"organized copy should have an audiobook-organizer path")
	}
}

// ---------------------------------------------------------------------------
// Regression: filter must skip soft-deleted books
// (Bug: organize was scanning 37K books including soft-deleted)
// ---------------------------------------------------------------------------

func TestFilterBooksNeedingOrganization_SkipsSoftDeleted(t *testing.T) {
	deleted := true
	notDeleted := false

	mockDB := &database.MockStore{
		GetBookFilesFunc: func(bookID string) ([]database.BookFile, error) {
			return []database.BookFile{
				{ID: "f1", BookID: bookID, FilePath: "/some/file.m4b"},
			}, nil
		},
	}
	svc := NewOrganizeService(mockDB)
	testLog := logger.New("test")

	books := []database.Book{
		{ID: "1", Title: "Active Book", FilePath: "/import/book1.m4b", MarkedForDeletion: &notDeleted},
		{ID: "2", Title: "Deleted Book", FilePath: "/import/book2.m4b", MarkedForDeletion: &deleted},
		{ID: "3", Title: "Also Deleted", FilePath: "/import/book3.m4b", MarkedForDeletion: &deleted},
		{ID: "4", Title: "Another Active", FilePath: "/import/book4.m4b", MarkedForDeletion: &notDeleted},
	}

	needsOrganize, _ := svc.FilterBooksNeedingOrganization(books, testLog)

	// Should only have the non-deleted books
	ids := make(map[string]bool)
	for _, b := range needsOrganize {
		ids[b.ID] = true
	}
	assert.True(t, ids["1"], "active book 1 should be included")
	assert.True(t, ids["4"], "active book 4 should be included")
	assert.False(t, ids["2"], "deleted book 2 should be excluded")
	assert.False(t, ids["3"], "deleted book 3 should be excluded")
}

// ---------------------------------------------------------------------------
// Regression: filter must skip non-primary versions that have a primary
// (Bug: organize was counting every version, leading to 24K+ books to organize)
// ---------------------------------------------------------------------------

func TestFilterBooksNeedingOrganization_SkipsNonPrimaryWithPrimary(t *testing.T) {
	isPrimary := true
	isNotPrimary := false
	vgID := "vg-123"

	mockDB := &database.MockStore{
		GetBooksByVersionGroupFunc: func(groupID string) ([]database.Book, error) {
			return []database.Book{
				{ID: "organized-1", IsPrimaryVersion: &isPrimary, VersionGroupID: &vgID},
				{ID: "original-1", IsPrimaryVersion: &isNotPrimary, VersionGroupID: &vgID},
			}, nil
		},
		GetBookFilesFunc: func(bookID string) ([]database.BookFile, error) {
			return []database.BookFile{
				{ID: "f1", BookID: bookID, FilePath: "/some/file.m4b"},
			}, nil
		},
	}
	svc := NewOrganizeService(mockDB)
	testLog := logger.New("test")

	books := []database.Book{
		{ID: "original-1", Title: "Has Primary", FilePath: "/import/book.m4b",
			IsPrimaryVersion: &isNotPrimary, VersionGroupID: &vgID},
	}

	needsOrganize, _ := svc.FilterBooksNeedingOrganization(books, testLog)
	assert.Empty(t, needsOrganize, "non-primary book with existing primary should be skipped")
}

func TestFilterBooksNeedingOrganization_AllowsNonPrimaryWithoutPrimary(t *testing.T) {
	isNotPrimary := false
	vgID := "vg-orphan"

	mockDB := &database.MockStore{
		GetBooksByVersionGroupFunc: func(groupID string) ([]database.Book, error) {
			// Only one non-primary book, no primary exists
			return []database.Book{
				{ID: "orphan-1", IsPrimaryVersion: &isNotPrimary, VersionGroupID: &vgID},
			}, nil
		},
		GetBookFilesFunc: func(bookID string) ([]database.BookFile, error) {
			return []database.BookFile{
				{ID: "f1", BookID: bookID, FilePath: "/some/file.m4b"},
			}, nil
		},
	}
	svc := NewOrganizeService(mockDB)
	testLog := logger.New("test")

	books := []database.Book{
		{ID: "orphan-1", Title: "No Primary", FilePath: "/import/orphan.m4b",
			IsPrimaryVersion: &isNotPrimary, VersionGroupID: &vgID},
	}

	needsOrganize, _ := svc.FilterBooksNeedingOrganization(books, testLog)
	assert.Len(t, needsOrganize, 1, "non-primary without existing primary should be allowed")
}

// ---------------------------------------------------------------------------
// Regression: filter must skip books with empty FilePath
// ---------------------------------------------------------------------------

func TestFilterBooksNeedingOrganization_SkipsEmptyFilePath(t *testing.T) {
	mockDB := &database.MockStore{}
	svc := NewOrganizeService(mockDB)
	testLog := logger.New("test")

	books := []database.Book{
		{ID: "1", Title: "No Path", FilePath: ""},
	}

	needsOrganize, _ := svc.FilterBooksNeedingOrganization(books, testLog)
	assert.Empty(t, needsOrganize, "book with empty FilePath should be skipped")
}

// ---------------------------------------------------------------------------
// Regression: filter must skip books with zero active (non-missing) book_files
// ---------------------------------------------------------------------------

func TestFilterBooksNeedingOrganization_SkipsAllMissingBookFiles(t *testing.T) {
	mockDB := &database.MockStore{
		GetBookFilesFunc: func(bookID string) ([]database.BookFile, error) {
			return []database.BookFile{
				{ID: "f1", BookID: bookID, FilePath: "/some/ch01.mp3", Missing: true},
				{ID: "f2", BookID: bookID, FilePath: "/some/ch02.mp3", Missing: true},
			}, nil
		},
	}
	svc := NewOrganizeService(mockDB)
	testLog := logger.New("test")

	books := []database.Book{
		{ID: "1", Title: "All Missing Files", FilePath: "/import/book"},
	}

	needsOrganize, _ := svc.FilterBooksNeedingOrganization(books, testLog)
	assert.Empty(t, needsOrganize, "book with all missing book_files should be skipped")
}

// ---------------------------------------------------------------------------
// New: createOrganizedVersion must copy all book_files, not just first
// ---------------------------------------------------------------------------

func TestCreateOrganizedVersion_CopiesAllBookFiles(t *testing.T) {
	rootDir := t.TempDir()
	config.AppConfig.ITunes.PathMappings = nil // no mappings needed for this test

	var createdFiles []database.BookFile
	var mu sync.Mutex
	isPrimary := false

	mockDB := &database.MockStore{
		GetBookFilesFunc: func(bookID string) ([]database.BookFile, error) {
			return []database.BookFile{
				{ID: "bf1", BookID: bookID, FilePath: "/import/Author/ch01.mp3", ITunesPersistentID: "PID1"},
				{ID: "bf2", BookID: bookID, FilePath: "/import/Author/ch02.mp3", ITunesPersistentID: "PID2"},
				{ID: "bf3", BookID: bookID, FilePath: "/import/Author/ch03.mp3", ITunesPersistentID: "PID3"},
			}, nil
		},
		CreateBookFunc: func(book *database.Book) (*database.Book, error) {
			return book, nil
		},
		UpdateBookFunc: func(id string, book *database.Book) (*database.Book, error) {
			return book, nil
		},
		MarkNeedsRescanFunc: func(bookID string) error { return nil },
		CreateBookFileFunc: func(file *database.BookFile) error {
			mu.Lock()
			createdFiles = append(createdFiles, *file)
			mu.Unlock()
			return nil
		},
	}

	cfg := &config.Config{
		RootDir:              rootDir,
		OrganizationStrategy: "copy",
		FolderNamingPattern:  "{author}/{title}",
		FileNamingPattern:    "{title}",
	}
	org := organizer.NewOrganizer(cfg)
	svc := NewOrganizeService(mockDB)
	testLog := logger.New("test")

	targetDir := filepath.Join(rootDir, "Author", "Book")
	require.NoError(t, os.MkdirAll(targetDir, 0755))

	book := &database.Book{
		ID:               "original",
		Title:            "Book",
		FilePath:         "/import/Author",
		Format:           "mp3",
		IsPrimaryVersion: &isPrimary,
		Author:           &database.Author{Name: "Author"},
	}

	_, err := svc.CreateOrganizedVersion(org, book, targetDir, true, "op-1", testLog)
	require.NoError(t, err)

	assert.Len(t, createdFiles, 3, "all 3 book_files should be copied to organized version")

	// All should have updated file paths in the target dir
	for _, bf := range createdFiles {
		assert.Contains(t, bf.FilePath, rootDir,
			"book_file path should point to organized directory")
		assert.NotEqual(t, "original", bf.BookID,
			"book_file should have new book ID, not original")
	}
}

// ---------------------------------------------------------------------------
// New: createOrganizedVersion sets correct library states
// ---------------------------------------------------------------------------

func TestCreateOrganizedVersion_SetsCorrectStates(t *testing.T) {
	rootDir := t.TempDir()
	config.AppConfig.ITunes.PathMappings = nil

	isPrimary := false
	var updatedOriginal *database.Book

	mockDB := &database.MockStore{
		GetBookFilesFunc: func(bookID string) ([]database.BookFile, error) { return nil, nil },
		CreateBookFunc: func(book *database.Book) (*database.Book, error) {
			return book, nil
		},
		UpdateBookFunc: func(id string, book *database.Book) (*database.Book, error) {
			if id == "original" {
				updatedOriginal = book
			}
			return book, nil
		},
		MarkNeedsRescanFunc: func(bookID string) error { return nil },
		CreateBookFileFunc:  func(file *database.BookFile) error { return nil },
	}

	cfg := &config.Config{
		RootDir:              rootDir,
		OrganizationStrategy: "copy",
		FolderNamingPattern:  "{author}/{title}",
		FileNamingPattern:    "{title}",
	}
	org := organizer.NewOrganizer(cfg)
	svc := NewOrganizeService(mockDB)
	testLog := logger.New("test")

	book := &database.Book{
		ID:               "original",
		Title:            "Test",
		FilePath:         "/import/test.m4b",
		Format:           "m4b",
		IsPrimaryVersion: &isPrimary,
		Author:           &database.Author{Name: "Author"},
	}

	created, err := svc.CreateOrganizedVersion(org, book, filepath.Join(rootDir, "test.m4b"), false, "op-1", testLog)
	require.NoError(t, err)

	// New organized copy should be primary
	assert.NotNil(t, created.IsPrimaryVersion)
	assert.True(t, *created.IsPrimaryVersion)
	assert.Equal(t, "organized", *created.LibraryState)

	// Original should be updated to non-primary organized_source
	require.NotNil(t, updatedOriginal)
	assert.False(t, *updatedOriginal.IsPrimaryVersion)
	assert.Equal(t, "organized_source", *updatedOriginal.LibraryState)

	// Both should share a version group
	assert.NotNil(t, created.VersionGroupID)
	assert.NotNil(t, updatedOriginal.VersionGroupID)
	assert.Equal(t, *created.VersionGroupID, *updatedOriginal.VersionGroupID)
}
