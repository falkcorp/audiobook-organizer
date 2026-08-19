// file: internal/server/quarantine_service_test.go
// version: 1.1.0

package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

func TestQuarantineBook_MovesFileAndUpdatesDB(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()

	store := srv.storeForWiring()
	require.NotNil(t, store)

	// Create a real file in the library dir
	root := config.AppConfig.RootDir
	src := filepath.Join(root, "Author", "Book", "book.m4b")
	require.NoError(t, os.MkdirAll(filepath.Dir(src), 0755))
	require.NoError(t, os.WriteFile(src, []byte("fake audio"), 0644))

	book, err := store.CreateBook(&database.Book{
		Title:    "Book",
		FilePath: src,
		Format:   "m4b",
	})
	require.NoError(t, err)

	require.NoError(t, srv.quarantineSvc.QuarantineBook(book.ID, "taglib failed"))

	// File should be gone from original location
	_, err = os.Stat(src)
	require.True(t, os.IsNotExist(err), "original file should be removed")

	// File should be in .failed/
	expected := filepath.Join(root, ".failed", "Unknown Author", "Book", "book.m4b")
	_, err = os.Stat(expected)
	require.NoError(t, err, "file should exist in .failed/")

	// DB should be updated
	updated, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.Equal(t, expected, updated.FilePath)
	require.NotNil(t, updated.QuarantineReason)
	require.Equal(t, "taglib failed", *updated.QuarantineReason)
	require.NotNil(t, updated.QuarantinedAt)

	// Path history should have quarantine entry
	history, err := store.GetBookPathHistory(book.ID)
	require.NoError(t, err)
	var found bool
	for _, h := range history {
		if h.ChangeType == "quarantine" {
			found = true
			require.Equal(t, src, h.OldPath)
			require.Equal(t, expected, h.NewPath)
		}
	}
	require.True(t, found, "quarantine path history entry not found")
}

func TestUnquarantineBook_MovesFileBack(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()

	store := srv.storeForWiring()
	root := config.AppConfig.RootDir

	// Set up a file already in .failed/
	quarPath := filepath.Join(root, ".failed", "Author", "Book", "book.m4b")
	require.NoError(t, os.MkdirAll(filepath.Dir(quarPath), 0755))
	require.NoError(t, os.WriteFile(quarPath, []byte("fake audio"), 0644))

	reason := "taglib failed"
	book, err := store.CreateBook(&database.Book{
		Title:            "Book",
		FilePath:         quarPath,
		Format:           "m4b",
		QuarantineReason: &reason,
	})
	require.NoError(t, err)

	// Manually set QuarantinedAt via UpdateBook
	book, err = store.GetBookByID(book.ID)
	require.NoError(t, err)
	now := time.Now()
	book.QuarantinedAt = &now
	_, err = store.UpdateBook(book.ID, book)
	require.NoError(t, err)

	// Seed path history with original path
	origPath := filepath.Join(root, "Author", "Book", "book.m4b")
	_ = store.RecordPathChange(&database.BookPathChange{
		BookID:     book.ID,
		OldPath:    origPath,
		NewPath:    quarPath,
		ChangeType: "quarantine",
	})

	require.NoError(t, srv.quarantineSvc.UnquarantineBook(book.ID))

	// File should be back at original path
	require.NoError(t, os.MkdirAll(filepath.Dir(origPath), 0755))
	_, err = os.Stat(origPath)
	require.NoError(t, err, "file should be restored to original path")

	updated, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.Equal(t, origPath, updated.FilePath)
	require.Nil(t, updated.QuarantineReason)
	require.Nil(t, updated.QuarantinedAt)
}
