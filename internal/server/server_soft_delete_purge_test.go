// file: internal/server/server_soft_delete_purge_test.go
// version: 1.2.0
// guid: 4a3b2c1d-0e9f-8a7b-6c5d-4e3f2a1b0c9d
// last-edited: 2026-10-05

package server

import (
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

	filePath := filepath.Join(t.TempDir(), "purge.m4b")
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

	filePath := filepath.Join(t.TempDir(), "auto-purge.m4b")
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

	server.runAutoPurgeSoftDeleted("")

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
