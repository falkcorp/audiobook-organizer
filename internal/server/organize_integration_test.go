// file: internal/server/organize_integration_test.go
// version: 1.1.0
// guid: b8c9d0e1-f2a3-4567-bcde-890123456f01
// last-edited: 2026-07-07

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOrganizeService_ViaHTTP(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	// Create a book in the DB with a file outside RootDir
	srcPath := env.CopyFixture("test_sample.m4b", env.ImportDir, "Book.m4b")
	author, err := env.Store.CreateAuthor("Test Author")
	require.NoError(t, err)
	book := &database.Book{
		Title:    "Test Book",
		FilePath: srcPath,
		Format:   "m4b",
		AuthorID: &author.ID,
	}
	created, err := env.Store.CreateBook(book)
	require.NoError(t, err)

	// Create book_files so organize can find files to copy
	require.NoError(t, env.Store.CreateBookFile(&database.BookFile{
		ID:       "bf-test-1",
		BookID:   created.ID,
		FilePath: srcPath,
		Format:   "m4b",
	}))

	// Trigger organize via HTTP. NewServer constructs the registry but
	// doesn't Start its worker pool (Container.Start does that during
	// Server.Start, which we don't call here). Start it explicitly so
	// the enqueued op actually runs.
	server := NewServer(env.Store)
	if server.opRegistry != nil {
		server.opRegistry.Start(context.Background())
		// registered after defer cleanup() → runs first (LIFO) to avoid pebble: closed panics.
		defer func() { _ = server.opRegistry.Shutdown(context.Background()) }()
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/operations/organize", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusAccepted, w.Code)

	// Wait for operation
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	opID, ok := resp["id"].(string)
	if !ok {
		opID, ok = resp["op_id"].(string)
	}
	require.True(t, ok, "response should contain id or op_id, got: %v", resp)
	testutil.WaitForOp(t, env.Store, opID, 15*time.Second)

	// Verify book was organized — now creates a new version record
	books, err := env.Store.GetAllBooksCore(100, 0)
	require.NoError(t, err)
	require.Len(t, books, 2, "should have original + organized version")

	// Find the organized version (in library dir) and original
	var organized, original *database.BookCore
	for i := range books {
		if strings.HasPrefix(books[i].FilePath, env.RootDir) {
			organized = &books[i]
		} else {
			original = &books[i]
		}
	}
	require.NotNil(t, organized, "should have an organized version in library dir")
	require.NotNil(t, original, "should have the original book")

	// Organized version is primary
	assert.True(t, organized.IsPrimaryVersion != nil && *organized.IsPrimaryVersion, "organized version should be primary")
	assert.True(t, original.IsPrimaryVersion != nil && !*original.IsPrimaryVersion, "original should not be primary")

	// Both share a version group
	assert.NotNil(t, organized.VersionGroupID)
	assert.NotNil(t, original.VersionGroupID)
	assert.Equal(t, *organized.VersionGroupID, *original.VersionGroupID, "should share version group")

	// Verify file exists at organized location
	_, err = os.Stat(organized.FilePath)
	assert.NoError(t, err, "organized file should exist")

	// Verify original file still exists
	_, err = os.Stat(original.FilePath)
	assert.NoError(t, err, "original file should still exist")
}
