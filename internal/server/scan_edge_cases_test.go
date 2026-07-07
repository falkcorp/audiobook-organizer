// file: internal/server/scan_edge_cases_test.go
// version: 1.4.0
// guid: c3d4e5f6-a7b8-9012-3456-789012abcdef
// last-edited: 2026-07-07

package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
	"github.com/falkcorp/audiobook-organizer/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScanService_EmptyDirectory(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	emptyDir := filepath.Join(env.TempDir, "empty")
	require.NoError(t, os.MkdirAll(emptyDir, 0755))

	svc := scanner.NewScanService(env.Store)
	folderPath := emptyDir
	err := svc.PerformScan(context.Background(), &scanner.ScanRequest{
		FolderPath: &folderPath,
	}, logger.New("test"))
	require.NoError(t, err)

	books, err := env.Store.GetAllBooksCore(100, 0)
	require.NoError(t, err)
	assert.Len(t, books, 0, "empty directory should produce no books")
}

func TestScanService_DeepNestedDirectories(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	// Create deeply nested directory structure
	deepPath := filepath.Join(env.ImportDir, "Level1", "Level2", "Level3", "Level4")
	env.CopyFixture("test_sample.m4b", deepPath, "Deep Book.m4b")

	svc := scanner.NewScanService(env.Store)
	folderPath := env.ImportDir
	err := svc.PerformScan(context.Background(), &scanner.ScanRequest{
		FolderPath: &folderPath,
	}, logger.New("test"))
	require.NoError(t, err)

	books, err := env.Store.GetAllBooksCore(100, 0)
	require.NoError(t, err)
	assert.Len(t, books, 1, "should find book in deeply nested directory")
}

func TestScanService_SpecialCharsInFilenames(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	// Files with special characters (spaces, parentheses, hyphens, apostrophes)
	specialNames := []string{
		"Book With Spaces.m4b",
		"Book (Special Edition).m4b",
		"Author's Book - Part 1.mp3",
	}

	for _, name := range specialNames {
		env.CreateFakeAudiobook(env.ImportDir, name)
	}

	svc := scanner.NewScanService(env.Store)
	folderPath := env.ImportDir
	err := svc.PerformScan(context.Background(), &scanner.ScanRequest{
		FolderPath: &folderPath,
	}, logger.New("test"))
	require.NoError(t, err)

	books, err := env.Store.GetAllBooksCore(100, 0)
	require.NoError(t, err)
	assert.Len(t, books, 3, "should handle special characters in filenames")
}

func TestScanService_UnsupportedFileExtensions(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	// Mix of supported and unsupported files
	env.CopyFixture("test_sample.m4b", env.ImportDir, "Good Book.m4b")
	env.CreateFakeAudiobook(env.ImportDir, "textfile.txt")
	env.CreateFakeAudiobook(env.ImportDir, "image.jpg")
	env.CreateFakeAudiobook(env.ImportDir, "document.pdf")

	svc := scanner.NewScanService(env.Store)
	folderPath := env.ImportDir
	err := svc.PerformScan(context.Background(), &scanner.ScanRequest{
		FolderPath: &folderPath,
	}, logger.New("test"))
	require.NoError(t, err)

	books, err := env.Store.GetAllBooksCore(100, 0)
	require.NoError(t, err)
	assert.Len(t, books, 1, "should only scan supported extensions")
}

func TestScanService_RescanUpdatesExistingBooks(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	env.CopyFixture("test_sample.m4b", env.ImportDir, "MyBook.m4b")

	svc := scanner.NewScanService(env.Store)
	folderPath := env.ImportDir

	// First scan
	err := svc.PerformScan(context.Background(), &scanner.ScanRequest{
		FolderPath: &folderPath,
	}, logger.New("test"))
	require.NoError(t, err)

	books1, _ := env.Store.GetAllBooksCore(100, 0)
	require.Len(t, books1, 1)

	// Second scan (should not create duplicate)
	err = svc.PerformScan(context.Background(), &scanner.ScanRequest{
		FolderPath: &folderPath,
	}, logger.New("test"))
	require.NoError(t, err)

	books2, _ := env.Store.GetAllBooksCore(100, 0)
	assert.Len(t, books2, 1, "rescan should not create duplicate books")
}

func TestScanService_OrphanBooks_FileDeleted(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	// Create a book in DB pointing to a file that no longer exists
	book := &database.Book{
		Title:    "Deleted Book",
		FilePath: filepath.Join(env.ImportDir, "deleted.m4b"),
		Format:   "m4b",
	}
	_, err := env.Store.CreateBook(book)
	require.NoError(t, err)

	// Organize should skip books with missing files
	config.AppConfig.AutoOrganize = false
	svc := NewOrganizeService(env.Store)
	err = svc.PerformOrganize(context.Background(), &OrganizeRequest{}, logger.New("test"))
	require.NoError(t, err)

	// Book should still be in DB (not deleted, just skipped)
	books, _ := env.Store.GetAllBooksCore(100, 0)
	assert.Len(t, books, 1, "orphan book should still exist in DB")
}

func TestScanService_NonexistentScanFolder(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	svc := scanner.NewScanService(env.Store)
	folderPath := "/nonexistent/scan/path"
	err := svc.PerformScan(context.Background(), &scanner.ScanRequest{
		FolderPath: &folderPath,
	}, logger.New("test"))
	// Should complete without error (logs warning about missing folder)
	require.NoError(t, err)

	books, _ := env.Store.GetAllBooksCore(100, 0)
	assert.Len(t, books, 0)
}

func TestScanService_MultiChapterAudiobook(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	// Simulate multi-chapter audiobook structure
	bookDir := filepath.Join(env.ImportDir, "Author Name", "Book Title")
	for i := 1; i <= 5; i++ {
		env.CreateFakeAudiobook(bookDir, fmt.Sprintf("Chapter %02d.mp3", i))
	}

	svc := scanner.NewScanService(env.Store)
	folderPath := env.ImportDir
	err := svc.PerformScan(context.Background(), &scanner.ScanRequest{
		FolderPath: &folderPath,
	}, logger.New("test"))
	require.NoError(t, err)

	books, err := env.Store.GetAllBooksCore(100, 0)
	require.NoError(t, err)
	// Scanner groups all files in the same directory into one multi-chapter book.
	assert.Equal(t, 1, len(books), "all chapter files under one directory should be one book")
}

func TestScanService_RealLibrivoxFiles(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	root := testutil.FindRepoRoot(t)
	librivoxDir := filepath.Join(root, "testdata", "audio", "librivox", "mobydick2_2511_librivox")
	if _, err := os.Stat(librivoxDir); os.IsNotExist(err) {
		t.Skip("librivox test fixtures not available")
	}

	// Scan the smallest librivox directory (mobydick2 ~45MB, 6 files)
	svc := scanner.NewScanService(env.Store)
	err := svc.PerformScan(context.Background(), &scanner.ScanRequest{
		FolderPath: &librivoxDir,
	}, logger.New("test"))
	require.NoError(t, err)

	books, err := env.Store.GetAllBooksCore(100, 0)
	require.NoError(t, err)
	// Scanner groups MP3s in the same directory as one album-book with segments
	assert.GreaterOrEqual(t, len(books), 1, "should find at least 1 book from 6 MP3 chapter files")

	// Verify metadata was extracted from real files
	for _, book := range books {
		assert.NotEmpty(t, book.Title, "book should have a title")
		assert.NotEmpty(t, book.Format, "book should have a format")
		// Real librivox files have "Herman Melville" as artist
		if book.AuthorID != nil {
			author, err := env.Store.GetAuthorByID(*book.AuthorID)
			if err == nil && author != nil {
				assert.NotEmpty(t, author.Name, "author name should be extracted")
			}
		}
	}
}

func TestScanService_LongFilePaths(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	// Create a path approaching filesystem limits
	longAuthor := strings.Repeat("A", 50)
	longTitle := strings.Repeat("B", 50)
	longDir := filepath.Join(env.ImportDir, longAuthor, longTitle)
	env.CreateFakeAudiobook(longDir, strings.Repeat("C", 50)+".m4b")

	svc := scanner.NewScanService(env.Store)
	folderPath := env.ImportDir
	err := svc.PerformScan(context.Background(), &scanner.ScanRequest{
		FolderPath: &folderPath,
	}, logger.New("test"))
	require.NoError(t, err)

	books, _ := env.Store.GetAllBooksCore(100, 0)
	assert.Len(t, books, 1, "should handle long file paths")
}
