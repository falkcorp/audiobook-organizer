// file: internal/server/server_narrators_fieldstates_test.go
// version: 2.1.0
// guid: a1b2c3d4-e5f6-7890-abcd-narrator0test
// last-edited: 2026-10-18

// NOTE(fable5 T022): SQLiteStore type assertions replaced with Store interface calls.

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListNarrators(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Empty list first
	req := httptest.NewRequest(http.MethodGet, "/api/v1/narrators", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var wrapper struct {
		Data []database.Narrator `json:"data"`
	}
	err := json.Unmarshal(w.Body.Bytes(), &wrapper)
	require.NoError(t, err)
	assert.Empty(t, wrapper.Data)

	// Add a narrator and re-check
	store := database.GetGlobalStore()
	_, err = store.CreateNarrator("Morgan Freeman")
	require.NoError(t, err)
	_, err = store.CreateNarrator("Stephen Fry")
	require.NoError(t, err)

	req = httptest.NewRequest(http.MethodGet, "/api/v1/narrators", nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	err = json.Unmarshal(w.Body.Bytes(), &wrapper)
	require.NoError(t, err)
	assert.Len(t, wrapper.Data, 2)
}

func TestCountNarrators(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Zero count
	req := httptest.NewRequest(http.MethodGet, "/api/v1/narrators/count", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	resp = resp["data"].(map[string]any)
	assert.Equal(t, float64(0), resp["count"])

	// Add narrators
	store := database.GetGlobalStore()
	_, err = store.CreateNarrator("Narrator A")
	require.NoError(t, err)
	_, err = store.CreateNarrator("Narrator B")
	require.NoError(t, err)

	req = httptest.NewRequest(http.MethodGet, "/api/v1/narrators/count", nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	resp = resp["data"].(map[string]any)
	assert.Equal(t, float64(2), resp["count"])
}

func TestGetAudiobookFieldStates(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Create a book
	book, err := database.GetGlobalStore().CreateBook(&database.Book{
		Title:    "Field States Test",
		FilePath: "/tmp/field-states.m4b",
	})
	require.NoError(t, err)

	// Get field states — should return empty map for new book
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks/"+book.ID+"/field-states", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	resp = resp["data"].(map[string]any)
	assert.Contains(t, resp, "field_states")

	// field_states should be a map (possibly empty)
	fieldStates, ok := resp["field_states"].(map[string]any)
	require.True(t, ok, "field_states should be a map")
	assert.NotNil(t, fieldStates)
}
