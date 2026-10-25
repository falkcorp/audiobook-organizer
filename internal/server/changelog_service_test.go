// file: internal/server/changelog_service_test.go
// version: 1.0.2
// guid: 3dcbb4a3-7fa5-425f-8247-5f96f81f2f22
// last-edited: 2026-10-25

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readEveryChange is the changelog predicate of a caller that may read every
// journal row (GetBookChangelog refuses them all for a nil predicate).
func readEveryChange(*database.OperationChange) bool { return true }

func TestChangelogService_GetBookChangelog_Empty(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	svc := server.changelogService
	require.NotNil(t, svc)

	entries, err := svc.GetBookChangelog("nonexistent-book-id", readEveryChange)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestChangelogService_GetBookChangelog_NilDB(t *testing.T) {
	svc := activity.NewChangelogService(nil)
	_, err := svc.GetBookChangelog("some-id", nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "database not initialized")
}

func TestChangelogService_WithPathHistory(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	store := database.GetGlobalStore()
	book, err := store.CreateBook(&database.Book{
		Title:    "Changelog Test",
		FilePath: "/tmp/changelog-test.m4b",
	})
	require.NoError(t, err)
	require.NotNil(t, book)

	// Record a path change
	err = store.RecordPathChange(&database.BookPathChange{
		BookID:     book.ID,
		OldPath:    "/old/path.m4b",
		NewPath:    "/new/path.m4b",
		ChangeType: "rename",
	})
	require.NoError(t, err)

	entries, err := server.changelogService.GetBookChangelog(book.ID, readEveryChange)
	require.NoError(t, err)
	// CreateBook records an implicit import entry, so the manual rename
	// is one of several entries. Find the specific one we recorded.
	var renameEntry *activity.ChangeLogEntry
	for i, e := range entries {
		if e.Type == "rename" && e.Details != nil && e.Details["change_type"] == "rename" {
			renameEntry = &entries[i]
			break
		}
	}
	require.NotNil(t, renameEntry, "expected a user-recorded rename entry among %v", entries)
	assert.Contains(t, renameEntry.Summary, "/old/path.m4b")
	assert.Contains(t, renameEntry.Summary, "/new/path.m4b")
}

func TestChangelogEndpoint_Returns200(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	store := database.GetGlobalStore()
	book, err := store.CreateBook(&database.Book{
		Title:    "Changelog Endpoint Test",
		FilePath: "/tmp/changelog-endpoint-test.m4b",
	})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/audiobooks/"+book.ID+"/changelog", nil)
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var wrapper struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	err = json.Unmarshal(w.Body.Bytes(), &wrapper)
	require.NoError(t, err)
	assert.Contains(t, wrapper.Data, "entries")
}

func TestChangelogEndpoint_WithEntries(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	store := database.GetGlobalStore()
	book, err := store.CreateBook(&database.Book{
		Title:    "Changelog With Entries",
		FilePath: "/tmp/changelog-entries.m4b",
	})
	require.NoError(t, err)

	// Add a path change
	err = store.RecordPathChange(&database.BookPathChange{
		BookID:     book.ID,
		OldPath:    "/a.m4b",
		NewPath:    "/b.m4b",
		ChangeType: "rename",
	})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/audiobooks/"+book.ID+"/changelog", nil)
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var wrapper struct {
		Data struct {
			Entries []activity.ChangeLogEntry `json:"entries"`
		} `json:"data"`
	}
	err = json.Unmarshal(w.Body.Bytes(), &wrapper)
	require.NoError(t, err)
	// CreateBook itself records an import path entry, so expect the rename
	// to be one of several entries — not necessarily the only one.
	var hasRename bool
	for _, e := range wrapper.Data.Entries {
		if e.Type == "rename" && e.Details != nil && e.Details["change_type"] == "rename" {
			hasRename = true
			break
		}
	}
	assert.True(t, hasRename, "expected a rename change_type entry among %v", wrapper.Data.Entries)
}

func TestDerefStrDisplay(t *testing.T) {
	s := "hello"
	assert.Equal(t, "hello", activity.DerefStrDisplay(&s))
	assert.Equal(t, "<nil>", activity.DerefStrDisplay(nil))
}
