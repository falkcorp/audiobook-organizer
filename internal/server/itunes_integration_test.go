// file: internal/server/itunes_integration_test.go
// version: 1.1.0
// guid: e5f6a7b8-c9d0-1234-efab-567890123cde
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

func TestITunesImport_FullWorkflow(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	// Create fake audiobook files
	hobbitPath := env.CreateFakeAudiobook(env.ImportDir, "The Hobbit.m4b")
	dunePath := env.CreateFakeAudiobook(env.ImportDir, "Dune.mp3")
	artOfWarPath := env.CreateFakeAudiobook(env.ImportDir, "The Art of War.m4b")

	// Generate iTunes XML with 4 tracks: 3 audiobooks + 1 music
	xmlPath := filepath.Join(env.TempDir, "iTunes Library.xml")
	testutil.GenerateITunesXML(t, []testutil.ITunesTestTrack{
		{TrackID: 100, PersistentID: "ABCD1234", Name: "The Hobbit",
			Artist: "J.R.R. Tolkien", AlbumArtist: "Rob Inglis",
			Album: "Middle-earth, Book 1", Genre: "Audiobook", Kind: "Audiobook",
			Year: 1997, FilePath: hobbitPath, TotalTime: 36000000, Comments: "Unabridged"},
		{TrackID: 200, PersistentID: "WXYZ9876", Name: "Dune",
			Artist: "Frank Herbert", Album: "Dune Chronicles",
			Genre: "Audiobooks", Kind: "MPEG audio file",
			Year: 1965, FilePath: dunePath, TotalTime: 72000000},
		{TrackID: 300, PersistentID: "ROCK1234", Name: "Bohemian Rhapsody",
			Artist: "Queen", Genre: "Rock", Kind: "MPEG audio file",
			Year: 1975, FilePath: "/nonexistent/queen.mp3", TotalTime: 355000},
		{TrackID: 400, PersistentID: "SPKN4567", Name: "The Art of War",
			Artist: "Sun Tzu", Genre: "Philosophy", Kind: "Spoken Word",
			Year: 0, FilePath: artOfWarPath, TotalTime: 7200000},
	}, xmlPath)

	// Import via HTTP handler
	server := NewServer(env.Store)
	if server.opRegistry != nil {
		server.opRegistry.Start(context.Background())
		// registered after defer cleanup() → runs first (LIFO) to avoid pebble: closed panics.
		defer func() { _ = server.opRegistry.Shutdown(context.Background()) }()
	}
	body := fmt.Sprintf(`{"library_path":"%s","import_mode":"import","skip_duplicates":true}`, xmlPath)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/itunes/import", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusAccepted, w.Code)

	// Wait for async import
	var respEnv map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &respEnv))
	data := respEnv["data"].(map[string]any)
	opID := data["operation_id"].(string)
	require.NotEmpty(t, opID)
	testutil.WaitForOp(t, env.Store, opID, 15*time.Second)

	// Verify books in database
	books, err := env.Store.GetAllBooksCore(100, 0)
	require.NoError(t, err)
	// At least the Hobbit and Art of War should be imported (they have audiobook-like genres/kinds)
	assert.GreaterOrEqual(t, len(books), 2, "should have imported at least 2 audiobooks")

	// Verify Hobbit book was imported with correct fields
	var hobbitBook *hobbitBookResult
	for _, b := range books {
		if b.Title == "The Hobbit" {
			hobbitBook = &hobbitBookResult{book: b}
			break
		}
	}
	if hobbitBook != nil {
		assert.Equal(t, hobbitPath, hobbitBook.book.FilePath)
		assert.NotNil(t, hobbitBook.book.ITunesPersistentID)
		assert.Equal(t, "ABCD1234", *hobbitBook.book.ITunesPersistentID)
		assert.NotNil(t, hobbitBook.book.Duration)
		assert.Equal(t, 36000, *hobbitBook.book.Duration) // TotalTime/1000

		// Verify author was created
		if hobbitBook.book.AuthorID != nil {
			author, err := env.Store.GetAuthorByID(*hobbitBook.book.AuthorID)
			require.NoError(t, err)
			assert.Equal(t, "J.R.R. Tolkien", author.Name)
		}
	}
}

type hobbitBookResult struct {
	book database.BookCore
}

func TestITunesImport_OrganizeMode(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	bookPath := env.CopyFixture("test_sample.m4b", env.ImportDir, "The Hobbit.m4b")

	xmlPath := filepath.Join(env.TempDir, "iTunes Library.xml")
	testutil.GenerateITunesXML(t, []testutil.ITunesTestTrack{
		{TrackID: 100, PersistentID: "ABCD1234", Name: "The Hobbit",
			Artist: "J.R.R. Tolkien", Genre: "Audiobook", Kind: "Audiobook",
			FilePath: bookPath, TotalTime: 100000},
	}, xmlPath)

	server := NewServer(env.Store)
	if server.opRegistry != nil {
		server.opRegistry.Start(context.Background())
		// registered after defer cleanup() → runs first (LIFO) to avoid pebble: closed panics.
		defer func() { _ = server.opRegistry.Shutdown(context.Background()) }()
	}
	body := fmt.Sprintf(`{"library_path":"%s","import_mode":"organize","skip_duplicates":false}`, xmlPath)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/itunes/import", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusAccepted, w.Code)

	var respEnv map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &respEnv))
	data := respEnv["data"].(map[string]any)
	opID := data["operation_id"].(string)
	testutil.WaitForOp(t, env.Store, opID, 15*time.Second)

	// Verify book was imported and organized
	books, err := env.Store.GetAllBooksCore(100, 0)
	require.NoError(t, err)
	require.Len(t, books, 1)

	book := books[0]
	assert.Contains(t, book.FilePath, env.RootDir, "book should be organized to library dir")

	// Verify file exists at organized location
	_, err = os.Stat(book.FilePath)
	assert.NoError(t, err)

	// Verify original still exists (copy strategy)
	_, err = os.Stat(bookPath)
	assert.NoError(t, err)
}

func TestITunesImport_SkipDuplicates(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	bookPath := env.CreateFakeAudiobook(env.ImportDir, "Dune.m4b")
	xmlPath := filepath.Join(env.TempDir, "iTunes Library.xml")
	testutil.GenerateITunesXML(t, []testutil.ITunesTestTrack{
		{TrackID: 200, PersistentID: "WXYZ9876", Name: "Dune",
			Artist: "Frank Herbert", Genre: "Audiobook", Kind: "Audiobook",
			FilePath: bookPath, TotalTime: 50000},
	}, xmlPath)

	server := NewServer(env.Store)
	if server.opRegistry != nil {
		server.opRegistry.Start(context.Background())
		// registered after defer cleanup() → runs first (LIFO) to avoid pebble: closed panics.
		defer func() { _ = server.opRegistry.Shutdown(context.Background()) }()
	}
	importOnce := func() int {
		body := fmt.Sprintf(`{"library_path":"%s","import_mode":"import","skip_duplicates":true}`, xmlPath)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/itunes/import", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		var wrapper struct {
			Data handlers.ITunesImportResponse `json:"data"`
		}
		json.Unmarshal(w.Body.Bytes(), &wrapper)
		testutil.WaitForOp(t, env.Store, wrapper.Data.OperationID, 15*time.Second)
		books, _ := env.Store.GetAllBooksCore(100, 0)
		return len(books)
	}

	count1 := importOnce()
	assert.Equal(t, 1, count1)

	count2 := importOnce()
	assert.Equal(t, 1, count2, "should NOT have created a duplicate")
}

func TestITunesWriteBack(t *testing.T) {
	// XML write-back has been removed. The write-back endpoint now only supports
	// ITL binary write-back. When ITL is not configured, it returns 400.
	env, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	// Create a book in DB that was imported from iTunes
	origPath := env.CreateFakeAudiobook(env.ImportDir, "The Hobbit.m4b")
	newPath := filepath.Join(env.RootDir, "Tolkien", "The Hobbit", "The Hobbit.m4b")
	testutil.CopyFile(t, origPath, newPath)

	persistentID := "ABCD1234EFGH5678"
	book := &database.Book{
		Title:              "The Hobbit",
		FilePath:           newPath,
		Format:             "m4b",
		ITunesPersistentID: &persistentID,
	}
	created, err := env.Store.CreateBook(book)
	require.NoError(t, err)

	// Execute write-back via HTTP — ITL is not configured in test, so should return 400
	server := NewServer(env.Store)
	if server.opRegistry != nil {
		server.opRegistry.Start(context.Background())
		// registered after defer cleanup() → runs first (LIFO) to avoid pebble: closed panics.
		defer func() { _ = server.opRegistry.Shutdown(context.Background()) }()
	}
	body := fmt.Sprintf(`{"audiobook_ids":["%s"]}`, created.ID)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/itunes/write-back", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	// Without ITL configured, endpoint returns 400
	assert.Equal(t, http.StatusBadRequest, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	errMsg, _ := resp["error"].(string)
	assert.Contains(t, errMsg, "ITL write-back is not enabled")
}

func TestITunesValidate_Endpoint(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	bookPath := env.CreateFakeAudiobook(env.ImportDir, "Test Book.m4b")
	xmlPath := filepath.Join(env.TempDir, "Library.xml")
	testutil.GenerateITunesXML(t, []testutil.ITunesTestTrack{
		{TrackID: 1, PersistentID: "TEST1234", Name: "Test Book",
			Artist: "Author", Genre: "Audiobook", Kind: "Audiobook",
			FilePath: bookPath, TotalTime: 10000},
		{TrackID: 2, PersistentID: "MISS5678", Name: "Missing Book",
			Artist: "Author", Genre: "Audiobook", Kind: "Audiobook",
			FilePath: "/nonexistent/missing.m4b", TotalTime: 20000},
	}, xmlPath)

	server := NewServer(env.Store)
	if server.opRegistry != nil {
		server.opRegistry.Start(context.Background())
		// registered after defer cleanup() → runs first (LIFO) to avoid pebble: closed panics.
		defer func() { _ = server.opRegistry.Shutdown(context.Background()) }()
	}
	body := fmt.Sprintf(`{"library_path":"%s"}`, xmlPath)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/itunes/validate", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var wrapper struct {
		Data handlers.ITunesValidateResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &wrapper))
	resp := wrapper.Data
	assert.Equal(t, 2, resp.AudiobookTracks)
	assert.Equal(t, 1, resp.FilesFound)
	assert.Equal(t, 1, resp.FilesMissing)
}
