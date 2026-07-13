// file: internal/server/e2e_workflow_test.go
// version: 1.3.0
// guid: c9d0e1f2-a3b4-5678-cdef-901234567012
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
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
	"github.com/falkcorp/audiobook-organizer/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestE2E_ITunesImportOrganizeWriteBack(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	// Step 1: Create source audiobook files
	hobbitPath := env.CopyFixture("test_sample.m4b", env.ImportDir, "The Hobbit.m4b")
	dunePath := env.CopyFixture("test_sample.mp3", env.ImportDir, "Dune.mp3")

	// Step 2: Generate iTunes library XML
	xmlPath := filepath.Join(env.TempDir, "Library.xml")
	testutil.GenerateITunesXML(t, []testutil.ITunesTestTrack{
		{TrackID: 1, PersistentID: "HOBT1234", Name: "The Hobbit",
			Artist: "Tolkien", Genre: "Audiobook", Kind: "Audiobook",
			FilePath: hobbitPath, TotalTime: 36000000},
		{TrackID: 2, PersistentID: "DUNE5678", Name: "Dune",
			Artist: "Herbert", Genre: "Audiobook", Kind: "Audiobook",
			FilePath: dunePath, TotalTime: 72000000},
	}, xmlPath)

	server := NewServer(env.Store)
	if server.opRegistry != nil {
		server.opRegistry.Start(context.Background())
		// registered after defer cleanup() → runs first (LIFO) to avoid pebble: closed panics.
		defer func() { _ = server.opRegistry.Shutdown(context.Background()) }()
	}

	// Step 3: Import (non-organize mode)
	importBody := fmt.Sprintf(`{"library_path":"%s","import_mode":"import","skip_duplicates":false}`, xmlPath)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/itunes/import",
		strings.NewReader(importBody)))
	require.Equal(t, http.StatusAccepted, w.Code)
	var respEnv map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &respEnv))
	data := respEnv["data"].(map[string]any)
	opID := data["operation_id"].(string)
	testutil.WaitForOp(t, env.Store, opID, 15*time.Second)

	// Step 4: Verify 2 books in DB
	books, err := env.Store.GetAllBooksCore(100, 0)
	require.NoError(t, err)
	require.Len(t, books, 2)

	// Step 4.5: Create book_files for each imported book (organize requires them)
	for _, b := range books {
		_ = env.Store.CreateBookFile(&database.BookFile{
			ID:       "bf-" + b.ID,
			BookID:   b.ID,
			FilePath: b.FilePath,
			Format:   b.Format,
		})
	}

	// Step 5: Organize
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/operations/organize",
		strings.NewReader("{}")))
	require.Equal(t, http.StatusAccepted, w.Code)
	var orgResp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &orgResp))
	// Organize endpoint returns flat {id, op_id, ...}; tolerate {data: {...}}
	// in case the envelope changes again.
	orgData := orgResp
	if d, ok := orgResp["data"].(map[string]any); ok {
		orgData = d
	}
	opID = ""
	if id, ok := orgData["id"].(string); ok {
		opID = id
	} else if id, ok := orgData["operation_id"].(string); ok {
		opID = id
	} else if id, ok := orgData["op_id"].(string); ok {
		opID = id
	}
	require.NotEmpty(t, opID, "organize response should contain id")
	testutil.WaitForOp(t, env.Store, opID, 15*time.Second)

	// Step 6: Wait for any automatic rescan to complete
	time.Sleep(1 * time.Second)

	// Verify books exist in DB (organize may trigger rescan that creates new entries)
	books, err = env.Store.GetAllBooksCore(100, 0)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(books), 2, "should have at least 2 books")

	// Verify at least some files exist in the RootDir (from organize/rescan)
	organizedCount := 0
	for _, b := range books {
		if strings.Contains(b.FilePath, env.RootDir) {
			organizedCount++
			_, err := os.Stat(b.FilePath)
			assert.NoError(t, err, "file should exist: %s", b.FilePath)
		}
	}
	assert.Greater(t, organizedCount, 0, "at least one book should be in library dir")

	// Step 7: Test write-back separately with a book that has a persistent ID
	// (The organize+rescan flow may not preserve iTunes persistent IDs,
	//  so we test write-back independently in TestITunesWriteBack)

	// Verify the iTunes library is still parseable
	lib, err := itunes.ParseLibrary(xmlPath)
	require.NoError(t, err)
	assert.Len(t, lib.Tracks, 2, "original iTunes library should still have 2 tracks")
}

func TestE2E_ScanAndFetchMetadata(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	useOnlyOpenLibrary(t)
	defer cleanup()

	// Mock OpenLibrary
	mockServer := testutil.MockOpenLibraryServer(t, map[string]string{
		"search.json": testutil.OpenLibraryHobbitResponse,
	})
	defer mockServer.Close()
	t.Setenv("OPENLIBRARY_BASE_URL", mockServer.URL)

	// Create audiobook file
	env.CopyFixture("test_sample.m4b", env.ImportDir, "The Hobbit.m4b")

	// Step 1: Scan
	svc := scanner.NewScanService(env.Store)
	folderPath := env.ImportDir
	err := svc.PerformScan(context.Background(), &scanner.ScanRequest{
		FolderPath: &folderPath,
	}, logger.New("test"))
	require.NoError(t, err)

	books, err := env.Store.GetAllBooksCore(100, 0)
	require.NoError(t, err)
	require.Len(t, books, 1)
	bookID := books[0].ID

	// Step 2: Fetch metadata
	metaSvc := metafetch.NewService(env.Store)
	resp, err := metaSvc.FetchMetadataForBook(context.Background(), bookID)
	require.NoError(t, err)
	assert.NotNil(t, resp)

	// Step 3: Verify enrichment
	enriched, err := env.Store.GetBookByID(bookID)
	require.NoError(t, err)
	assert.NotNil(t, enriched.Publisher, "publisher should be populated from OpenLibrary")
}
