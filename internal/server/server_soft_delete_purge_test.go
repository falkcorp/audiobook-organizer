// file: internal/server/server_soft_delete_purge_test.go
// version: 1.3.1
// guid: 4a3b2c1d-0e9f-8a7b-6c5d-4e3f2a1b0c9d
// last-edited: 2026-10-11

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSoftDeleteAndPurge_WithFileDeletion(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// The purge deletes only inside the library root (purgePathInsideRoot,
	// 2026-10-06), so the file lives under RootDir.
	filePath := filepath.Join(config.AppConfig.RootDir, "purge.m4b")
	require.NoError(t, os.WriteFile(filePath, []byte("audio"), 0o644))
	book, err := database.GetGlobalStore().CreateBook(&database.Book{Title: "Purge Me", FilePath: filePath, Format: "m4b"})
	require.NoError(t, err)

	// Soft-delete via API.
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/audiobooks/"+book.ID+"?soft_delete=true", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// Purge with file deletion.
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/audiobooks/purge-soft-deleted?delete_files=true", nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	_, err = os.Stat(filePath)
	assert.Error(t, err)
}

func TestRunAutoPurgeSoftDeleted_DeletesOldEntries(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// AppConfig is fully restored by defer cleanup() from setupTestServer.
	config.AppConfig.PurgeSoftDeletedAfterDays = 1
	config.AppConfig.PurgeSoftDeletedDeleteFiles = true

	// The purge deletes only inside the library root (purgePathInsideRoot,
	// 2026-10-06), so the file lives under RootDir.
	filePath := filepath.Join(config.AppConfig.RootDir, "auto-purge.m4b")
	require.NoError(t, os.WriteFile(filePath, []byte("audio"), 0o644))
	book, err := database.GetGlobalStore().CreateBook(&database.Book{Title: "Auto Purge", FilePath: filePath, Format: "m4b"})
	require.NoError(t, err)

	// Mark as soft-deleted older than cutoff.
	marked := true
	deletedAt := time.Now().AddDate(0, 0, -2)
	book.MarkedForDeletion = &marked
	book.MarkedForDeletionAt = &deletedAt
	book.LibraryState = new("deleted")
	_, err = database.GetGlobalStore().UpdateBook(book.ID, book)
	require.NoError(t, err)

	server.runAutoPurgeSoftDeleted(t.Context(), "")

	// Book removed.
	fetched, err := database.GetGlobalStore().GetBookByID(book.ID)
	require.NoError(t, err)
	assert.Nil(t, fetched)

	// File removed.
	_, err = os.Stat(filePath)
	assert.Error(t, err)
}

// POST /audiobooks/:id/discard-progress-and-purge is wired, and refuses a
// book that is not in the trash: 409, the book and the user's progress stay.
func TestDiscardProgressAndPurge_RouteRefusesLiveBook(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	store := database.GetGlobalStore()
	book, err := store.CreateBook(&database.Book{Title: "Still Listening", Format: "m4b"})
	require.NoError(t, err)
	u, err := store.CreateUser("reader", "reader@example.com", "argon2id", "x", []string{"user"}, "active")
	require.NoError(t, err)
	require.NoError(t, store.SetUserBookState(&database.UserBookState{
		UserID: u.ID, BookID: book.ID, Status: database.UserBookStatusInProgress, ProgressPct: 30, LastActivityAt: time.Now(),
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/"+book.ID+"/discard-progress-and-purge", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())

	fetched, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.NotNil(t, fetched)
	st, err := store.GetUserBookState(u.ID, book.ID)
	require.NoError(t, err)
	require.Equal(t, 30, st.ProgressPct)
}

// seedOldSoftDeletedBook creates a book under the library root, soft-deleted
// two days ago, with no book_file rows (so the purge may take it).
func seedOldSoftDeletedBook(t *testing.T, name string) *database.Book {
	t.Helper()
	filePath := filepath.Join(config.AppConfig.RootDir, name+".m4b")
	require.NoError(t, os.WriteFile(filePath, []byte("audio"), 0o644))
	book, err := database.GetGlobalStore().CreateBook(&database.Book{Title: name, FilePath: filePath, Format: "m4b"})
	require.NoError(t, err)
	marked := true
	deletedAt := time.Now().AddDate(0, 0, -2)
	book.MarkedForDeletion = &marked
	book.MarkedForDeletionAt = &deletedAt
	book.LibraryState = new("deleted")
	_, err = database.GetGlobalStore().UpdateBook(book.ID, book)
	require.NoError(t, err)
	return book
}

// The merged maintenance.purge-deleted op hands its own context to the purge,
// so the def's Timeout and a cancel stop it. Before 04-P4b the server copy
// passed context.Background() and neither could reach PurgeSoftDeletedBooks.
func TestRunAutoPurgeSoftDeleted_ContextCancelPropagates(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	config.AppConfig.PurgeSoftDeletedAfterDays = 1
	config.AppConfig.PurgeSoftDeletedDeleteFiles = true
	books := []*database.Book{
		seedOldSoftDeletedBook(t, "cancel-a"),
		seedOldSoftDeletedBook(t, "cancel-b"),
		seedOldSoftDeletedBook(t, "cancel-c"),
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	server.runAutoPurgeSoftDeleted(ctx, "")

	for _, b := range books {
		got, err := database.GetGlobalStore().GetBookByID(b.ID)
		require.NoError(t, err)
		assert.NotNil(t, got, "book %q must survive a purge whose context was already cancelled", b.Title)
		_, statErr := os.Stat(b.FilePath)
		assert.NoError(t, statErr, "file of %q must not be deleted after cancel", b.Title)
	}
}

// PurgeSoftDeletedAfterDays <= 0 is the op's safety valve: it returns before
// touching the audiobook service, so nothing is purged however old.
func TestRunAutoPurgeSoftDeleted_DisabledPurgesNothing(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	config.AppConfig.PurgeSoftDeletedAfterDays = 0
	config.AppConfig.PurgeSoftDeletedDeleteFiles = true
	book := seedOldSoftDeletedBook(t, "disabled-purge")

	server.runAutoPurgeSoftDeleted(t.Context(), "")

	got, err := database.GetGlobalStore().GetBookByID(book.ID)
	require.NoError(t, err)
	assert.NotNil(t, got)
	_, statErr := os.Stat(book.FilePath)
	assert.NoError(t, statErr)
}
