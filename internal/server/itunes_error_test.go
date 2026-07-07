// file: internal/server/itunes_error_test.go
// version: 1.2.0
// guid: b2c3d4e5-f6a7-8901-2345-678901abcdef
// last-edited: 2026-07-07

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	"github.com/falkcorp/audiobook-organizer/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestITunesImport_CorruptXML(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	defer cleanup() // registered first → runs second (LIFO)

	xmlPath := filepath.Join(env.TempDir, "corrupt.xml")
	require.NoError(t, os.WriteFile(xmlPath, []byte("this is not valid XML at all <broken"), 0644))

	server := NewServer(env.Store)
	if server.opRegistry != nil {
		server.opRegistry.Start(context.Background())
		// registered after defer cleanup() → runs first (LIFO), ensuring registry
		// shuts down before store.Close() to avoid pebble: closed panics.
		defer func() { _ = server.opRegistry.Shutdown(context.Background()) }()
	}
	body := fmt.Sprintf(`{"library_path":"%s","import_mode":"import","skip_duplicates":false}`, xmlPath)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/itunes/import", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusAccepted, w.Code) // async, so it accepts

	// Wait for operation to complete (should fail)
	var respBody struct {
		Data handlers.ITunesImportResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &respBody))
	resp := respBody.Data
	testutil.WaitForOp(t, env.Store, resp.OperationID, 15*time.Second)

	// Verify operation failed
	op, err := env.Store.GetOperationByID(resp.OperationID)
	require.NoError(t, err)
	assert.Equal(t, "failed", op.Status)
}

func TestITunesImport_NonexistentFile(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	server := NewServer(env.Store)
	body := `{"library_path":"/nonexistent/path/library.xml","import_mode":"import","skip_duplicates":false}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/itunes/import", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestITunesImport_EmptyXML(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	defer cleanup() // registered first → runs second (LIFO)

	// Valid plist but no tracks
	xmlPath := filepath.Join(env.TempDir, "empty.xml")
	testutil.GenerateITunesXML(t, []testutil.ITunesTestTrack{}, xmlPath)

	server := NewServer(env.Store)
	if server.opRegistry != nil {
		server.opRegistry.Start(context.Background())
		// registered after defer cleanup() → runs first (LIFO), ensuring registry
		// shuts down before store.Close() to avoid pebble: closed panics.
		defer func() { _ = server.opRegistry.Shutdown(context.Background()) }()
	}
	body := fmt.Sprintf(`{"library_path":"%s","import_mode":"import","skip_duplicates":false}`, xmlPath)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/itunes/import", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusAccepted, w.Code)

	var respBody struct {
		Data handlers.ITunesImportResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &respBody))
	resp := respBody.Data
	testutil.WaitForOp(t, env.Store, resp.OperationID, 15*time.Second)

	// Should complete with 0 books
	books, err := env.Store.GetAllBooksCore(100, 0)
	require.NoError(t, err)
	assert.Len(t, books, 0)
}

func TestITunesImport_MissingFilesPartial(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	// Only one file exists, two are missing
	existingPath := env.CreateFakeAudiobook(env.ImportDir, "Existing Book.m4b")

	xmlPath := filepath.Join(env.TempDir, "partial.xml")
	testutil.GenerateITunesXML(t, []testutil.ITunesTestTrack{
		{TrackID: 1, PersistentID: "EXIST001", Name: "Existing Book",
			Artist: "Author", Genre: "Audiobook", Kind: "Audiobook",
			FilePath: existingPath, TotalTime: 10000},
		{TrackID: 2, PersistentID: "MISS0001", Name: "Missing Book 1",
			Artist: "Author", Genre: "Audiobook", Kind: "Audiobook",
			FilePath: "/nonexistent/missing1.m4b", TotalTime: 20000},
		{TrackID: 3, PersistentID: "MISS0002", Name: "Missing Book 2",
			Artist: "Author", Genre: "Audiobook", Kind: "Audiobook",
			FilePath: "/nonexistent/missing2.m4b", TotalTime: 30000},
	}, xmlPath)

	server := NewServer(env.Store)
	if server.opRegistry != nil {
		server.opRegistry.Start(context.Background())
		// registered after defer cleanup() → runs first (LIFO), ensuring registry
		// shuts down before store.Close() to avoid pebble: closed panics.
		defer func() { _ = server.opRegistry.Shutdown(context.Background()) }()
	}
	body := fmt.Sprintf(`{"library_path":"%s","import_mode":"import","skip_duplicates":false}`, xmlPath)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/itunes/import", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusAccepted, w.Code)

	var respBody struct {
		Data handlers.ITunesImportResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &respBody))
	resp := respBody.Data
	testutil.WaitForOp(t, env.Store, resp.OperationID, 15*time.Second)

	// Should have imported the existing book, failed on the missing ones
	books, err := env.Store.GetAllBooksCore(100, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, len(books), "should import 1 existing book, skip 2 missing")
	assert.Equal(t, "Existing Book", books[0].Title)
}

func TestITunesImport_InvalidMode(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	server := NewServer(env.Store)
	body := `{"library_path":"/tmp/fake.xml","import_mode":"invalid_mode","skip_duplicates":false}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/itunes/import", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code) // binding validation fails
}

func TestITunesImport_MissingRequiredFields(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	server := NewServer(env.Store)

	tests := []struct {
		name string
		body string
	}{
		{"no library_path", `{"import_mode":"import"}`},
		{"no import_mode", `{"library_path":"/tmp/fake.xml"}`},
		{"empty body", `{}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/itunes/import", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			server.router.ServeHTTP(w, req)
			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	}
}

func TestITunesValidate_NonexistentFile(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	server := NewServer(env.Store)
	body := `{"library_path":"/nonexistent/library.xml"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/itunes/validate", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestITunesValidate_CorruptXML(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	xmlPath := filepath.Join(env.TempDir, "corrupt.xml")
	require.NoError(t, os.WriteFile(xmlPath, []byte("not xml"), 0644))

	server := NewServer(env.Store)
	body := fmt.Sprintf(`{"library_path":"%s"}`, xmlPath)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/itunes/validate", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestITunesWriteBack_NonexistentBook(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	xmlPath := filepath.Join(env.TempDir, "Library.xml")
	testutil.GenerateITunesXML(t, []testutil.ITunesTestTrack{}, xmlPath)

	server := NewServer(env.Store)
	body := fmt.Sprintf(`{"library_path":"%s","audiobook_ids":["nonexistent-id"],"create_backup":false}`, xmlPath)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/itunes/write-back", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	// Should return 400 or 500 depending on whether book is found
	assert.NotEqual(t, http.StatusOK, w.Code)
}

func TestITunesWriteBack_NoITunesPersistentID(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	// Create book without iTunes persistent ID
	book := &database.Book{
		Title:    "Non-iTunes Book",
		FilePath: "/fake/path.m4b",
		Format:   "m4b",
	}
	created, err := env.Store.CreateBook(book)
	require.NoError(t, err)

	xmlPath := filepath.Join(env.TempDir, "Library.xml")
	testutil.GenerateITunesXML(t, []testutil.ITunesTestTrack{}, xmlPath)

	server := NewServer(env.Store)
	body := fmt.Sprintf(`{"library_path":"%s","audiobook_ids":["%s"],"create_backup":false}`, xmlPath, created.ID)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/itunes/write-back", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code) // no audiobooks with iTunes persistent IDs
}

func TestITunesImport_RealTestLibrary(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	// Use the existing test_library.xml (4 tracks: 2 audiobooks, 1 music, 1 spoken word)
	root := testutil.FindRepoRoot(t)
	xmlPath := filepath.Join(root, "internal", "itunes", "testdata", "test_library.xml")
	if _, err := os.Stat(xmlPath); os.IsNotExist(err) {
		t.Skip("test_library.xml not available")
	}

	// Create fake files matching the paths in test_library.xml
	// The paths are: file://localhost/Users/testuser/Music/iTunes/...
	// These won't exist, so books with missing files will be skipped during import

	server := NewServer(env.Store)
	if server.opRegistry != nil {
		server.opRegistry.Start(context.Background())
		// registered after defer cleanup() → runs first (LIFO), ensuring registry
		// shuts down before store.Close() to avoid pebble: closed panics.
		defer func() { _ = server.opRegistry.Shutdown(context.Background()) }()
	}
	body := fmt.Sprintf(`{"library_path":"%s","import_mode":"import","skip_duplicates":false}`, xmlPath)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/itunes/import", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusAccepted, w.Code)

	var respBody struct {
		Data handlers.ITunesImportResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &respBody))
	resp := respBody.Data
	testutil.WaitForOp(t, env.Store, resp.OperationID, 15*time.Second)

	// Operation should complete (files don't exist, so tracks will fail with "file does not exist")
	op, err := env.Store.GetOperationByID(resp.OperationID)
	require.NoError(t, err)
	assert.Equal(t, "completed", op.Status, "import should complete even with missing files")
}
