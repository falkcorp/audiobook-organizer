// file: internal/server/server_update_audiobook_more_test.go
// version: 1.0.0
// guid: 9c8b7a6d-5e4f-3a2b-1c0d-9e8f7a6b5c4d
// last-edited: 2026-01-24

package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/metastate"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdateAudiobook_EmptyBody_Returns400(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	tempFile := filepath.Join(t.TempDir(), "book-empty-body.m4b")
	require.NoError(t, os.WriteFile(tempFile, []byte("audio"), 0o644))

	created, err := database.GetGlobalStore().CreateBook(&database.Book{Title: "T", FilePath: tempFile, Format: "m4b"})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/audiobooks/%s", created.ID), nil)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestUpdateAudiobook_InvalidJSON_Returns400(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	tempFile := filepath.Join(t.TempDir(), "book-bad-json.m4b")
	require.NoError(t, os.WriteFile(tempFile, []byte("audio"), 0o644))

	created, err := database.GetGlobalStore().CreateBook(&database.Book{Title: "T", FilePath: tempFile, Format: "m4b"})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/audiobooks/%s", created.ID), bytes.NewBufferString("{"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestUpdateAudiobook_CreatesAuthorSeries_AndUpdatesOverrideState(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	tempFile := filepath.Join(t.TempDir(), "book-update.m4b")
	require.NoError(t, os.WriteFile(tempFile, []byte("audio"), 0o644))

	created, err := database.GetGlobalStore().CreateBook(&database.Book{Title: "Original", FilePath: tempFile, Format: "m4b"})
	require.NoError(t, err)

	payload := map[string]interface{}{
		"author_name":            "Author X",
		"series_name":            "Series Y",
		"narrator":               "Narrator Z",
		"publisher":              "Pub",
		"language":               "en",
		"audiobook_release_year": 2024,
		"isbn10":                 "1234567890",
		"isbn13":                 "9999999999999",
		"overrides": map[string]map[string]interface{}{
			"title": {
				"value":         "Override Title",
				"locked":        true,
				"fetched_value": "Fetched Title",
			},
		},
		"unlock_overrides": []string{"publisher"},
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/audiobooks/%s", created.ID), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var wrapper struct {
		Data database.Book `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &wrapper))
	updated := wrapper.Data
	require.NotNil(t, updated.AuthorID)
	require.NotNil(t, updated.SeriesID)
	assert.Equal(t, "Override Title", updated.Title)

	states, err := database.GetGlobalStore().GetMetadataFieldStates(created.ID)
	require.NoError(t, err)
	stateByField := map[string]database.MetadataFieldState{}
	for _, st := range states {
		stateByField[st.Field] = st
	}

	// Debug: print all fields
	t.Logf("Retrieved %d metadata state entries:", len(states))
	for _, st := range states {
		t.Logf("  Field: %s, OverrideLocked: %v, OverrideValue: %v", st.Field, st.OverrideLocked, st.OverrideValue)
	}

	// Override via payload.Overrides.
	stTitle, ok := stateByField["title"]
	require.True(t, ok, "title state not found")
	assert.True(t, stTitle.OverrideLocked)
	require.NotNil(t, stTitle.OverrideValue)
	assert.Equal(t, "Override Title", metastate.Decode(stTitle.OverrideValue))
	require.NotNil(t, stTitle.FetchedValue)
	assert.Equal(t, "Fetched Title", metastate.Decode(stTitle.FetchedValue))

	// Auto-created from raw payload for fields with no explicit override.
	stIsbn13, ok := stateByField["isbn13"]
	require.True(t, ok, "isbn13 state not found")
	assert.True(t, stIsbn13.OverrideLocked, "isbn13 OverrideLocked should be true")
	require.NotNil(t, stIsbn13.OverrideValue)
	assert.Equal(t, "9999999999999", metastate.Decode(stIsbn13.OverrideValue))

	// Publisher should be unlocked via UnlockOverrides.
	stPublisher, ok := stateByField["publisher"]
	require.True(t, ok)
	assert.False(t, stPublisher.OverrideLocked)

	// Cover the Clear override path.
	clearBody := bytes.NewBufferString(`{"overrides":{"language":{"clear":true}}}`)
	req = httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/audiobooks/%s", created.ID), clearBody)
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}
