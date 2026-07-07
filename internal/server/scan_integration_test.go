// file: internal/server/scan_integration_test.go
// version: 1.3.0
// guid: f6a7b8c9-d0e1-2345-fabc-678901234def
// last-edited: 2026-07-07

package server

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
	"github.com/falkcorp/audiobook-organizer/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScanService_ScanWithRealFiles(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	// Create directory structure with audiobook files
	env.CopyFixture("test_sample.m4b", filepath.Join(env.ImportDir, "Tolkien", "The Hobbit"), "The Hobbit.m4b")
	env.CopyFixture("test_sample.mp3", filepath.Join(env.ImportDir, "Herbert", "Dune"), "Dune.mp3")
	env.CopyFixture("test_sample.flac", filepath.Join(env.ImportDir, "Asimov", "Foundation"), "Foundation.flac")

	svc := scanner.NewScanService(env.Store)
	folderPath := env.ImportDir
	err := svc.PerformScan(context.Background(), &scanner.ScanRequest{
		FolderPath: &folderPath,
	}, logger.New("test"))
	require.NoError(t, err)

	books, err := env.Store.GetAllBooksCore(100, 0)
	require.NoError(t, err)
	assert.Len(t, books, 3, "should find 3 audiobook files")

	for _, b := range books {
		assert.NotEmpty(t, b.FilePath)
		assert.NotEmpty(t, b.Format)
	}
}

func TestScanService_AutoOrganize(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	config.AppConfig.AutoOrganize = true

	env.CopyFixture("test_sample.m4b", env.ImportDir, "The Hobbit.m4b")

	svc := scanner.NewScanService(env.Store)
	// Wire up the AutoOrganizeFn like the server does
	svc.AutoOrganizeFn = func(ctx context.Context, books []scanner.Book, l logger.Logger) {
		if len(books) == 0 {
			return
		}
		if !config.AppConfig.AutoOrganize || config.AppConfig.RootDir == "" {
			return
		}
		org := organizer.NewOrganizer(&config.AppConfig)
		for i := range books {
			if l.IsCanceled() {
				break
			}
			dbBook, err := env.Store.GetBookByFilePath(books[i].FilePath)
			if err != nil || dbBook == nil {
				continue
			}
			newPath, _, err := org.OrganizeBook(dbBook)
			if err != nil {
				l.Warn("Organize failed for %s: %v", dbBook.Title, err)
				continue
			}
			if newPath != dbBook.FilePath {
				dbBook.FilePath = newPath
				scanner.ApplyOrganizedFileMetadata(dbBook, newPath)
				if _, err := env.Store.UpdateBook(dbBook.ID, dbBook); err != nil {
					l.Error("Failed to update path for %s: %v", dbBook.Title, err)
				}
			}
		}
	}

	folderPath := env.ImportDir
	err := svc.PerformScan(context.Background(), &scanner.ScanRequest{
		FolderPath: &folderPath,
	}, logger.New("test"))
	require.NoError(t, err)

	books, err := env.Store.GetAllBooksCore(100, 0)
	require.NoError(t, err)
	require.Len(t, books, 1)

	book := books[0]
	assert.Contains(t, book.FilePath, env.RootDir, "book should be in library dir")
}

func TestScanService_MultipleFolders(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	dir1 := filepath.Join(env.TempDir, "import1")
	dir2 := filepath.Join(env.TempDir, "import2")

	env.CopyFixture("test_sample.m4b", dir1, "Book1.m4b")
	env.CopyFixture("test_sample.mp3", dir2, "Book2.mp3")

	// Register both as import paths
	_, err := env.Store.CreateImportPath(dir1, "Import 1")
	require.NoError(t, err)
	_, err = env.Store.CreateImportPath(dir2, "Import 2")
	require.NoError(t, err)

	svc := scanner.NewScanService(env.Store)
	forceUpdate := true
	err = svc.PerformScan(context.Background(), &scanner.ScanRequest{
		ForceUpdate: &forceUpdate,
	}, logger.New("test"))
	require.NoError(t, err)

	books, err := env.Store.GetAllBooksCore(100, 0)
	require.NoError(t, err)
	assert.Len(t, books, 2, "should find books from both import dirs")
}
