// file: internal/server/metadata_history_test.go
// version: 1.0.1
// guid: 5e6f7a8b-9c0d-1e2f-3a4b-5c6d7e8f9a0b
// last-edited: 2026-09-02

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetadataStateService_RecordsChangeOnSetOverride(t *testing.T) {
	var recorded *database.MetadataChangeRecord

	mockDB := &database.MockStore{
		GetMetadataFieldStatesFunc: func(bookID string) ([]database.MetadataFieldState, error) {
			return []database.MetadataFieldState{}, nil
		},
		UpsertMetadataFieldStateFunc: func(state *database.MetadataFieldState) error {
			return nil
		},
		RecordMetadataChangeFunc: func(record *database.MetadataChangeRecord) error {
			recorded = record
			return nil
		},
		GetUserPreferenceFunc: func(key string) (*database.UserPreference, error) {
			return nil, nil
		},
	}

	service := metafetch.NewMetadataStateService(mockDB)
	err := service.SetOverride("book1", "title", "New Title", false)
	require.NoError(t, err)

	require.NotNil(t, recorded, "RecordMetadataChange should have been called")
	assert.Equal(t, "book1", recorded.BookID)
	assert.Equal(t, "title", recorded.Field)
	assert.Equal(t, "override", recorded.ChangeType)
	assert.Equal(t, "manual", recorded.Source)
}

func TestMetadataStateService_RecordsChangeOnClearOverride(t *testing.T) {
	var recorded *database.MetadataChangeRecord
	overrideVal := `"Override Title"`

	mockDB := &database.MockStore{
		GetMetadataFieldStatesFunc: func(bookID string) ([]database.MetadataFieldState, error) {
			return []database.MetadataFieldState{
				{
					BookID:        "book1",
					Field:         "title",
					OverrideValue: &overrideVal,
					UpdatedAt:     time.Now(),
				},
			}, nil
		},
		DeleteMetadataFieldStateFunc: func(bookID, field string) error {
			return nil
		},
		RecordMetadataChangeFunc: func(record *database.MetadataChangeRecord) error {
			recorded = record
			return nil
		},
		GetUserPreferenceFunc: func(key string) (*database.UserPreference, error) {
			return nil, nil
		},
	}

	service := metafetch.NewMetadataStateService(mockDB)
	err := service.ClearOverride("book1", "title")
	require.NoError(t, err)

	require.NotNil(t, recorded, "RecordMetadataChange should have been called")
	assert.Equal(t, "book1", recorded.BookID)
	assert.Equal(t, "title", recorded.Field)
	assert.Equal(t, "clear", recorded.ChangeType)
	assert.Equal(t, "manual", recorded.Source)
}

func TestMetadataStateService_RecordsChangeOnFetch(t *testing.T) {
	var recorded *database.MetadataChangeRecord

	mockDB := &database.MockStore{
		GetMetadataFieldStatesFunc: func(bookID string) ([]database.MetadataFieldState, error) {
			return []database.MetadataFieldState{}, nil
		},
		UpsertMetadataFieldStateFunc: func(state *database.MetadataFieldState) error {
			return nil
		},
		RecordMetadataChangeFunc: func(record *database.MetadataChangeRecord) error {
			recorded = record
			return nil
		},
		GetUserPreferenceFunc: func(key string) (*database.UserPreference, error) {
			return nil, nil
		},
	}

	service := metafetch.NewMetadataStateService(mockDB)
	err := service.UpdateFetchedMetadata("book1", map[string]any{"title": "Fetched Title"})
	require.NoError(t, err)

	require.NotNil(t, recorded, "RecordMetadataChange should have been called")
	assert.Equal(t, "book1", recorded.BookID)
	assert.Equal(t, "title", recorded.Field)
	assert.Equal(t, "fetched", recorded.ChangeType)
}

func TestUndoMetadataChange_Handler(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Create a book first
	book := &database.Book{
		Title:    "Test Book",
		FilePath: "/tmp/test.m4b",
	}
	createdBook, err := database.GetGlobalStore().CreateBook(book)
	require.NoError(t, err)

	// Record a change to undo
	prev := `"Old Title"`
	next := `"New Title"`
	record := &database.MetadataChangeRecord{
		BookID:        createdBook.ID,
		Field:         "title",
		PreviousValue: &prev,
		NewValue:      &next,
		ChangeType:    "override",
		Source:        "manual",
		ChangedAt:     time.Now(),
	}
	err = database.GetGlobalStore().RecordMetadataChange(record)
	require.NoError(t, err)

	// POST undo
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/audiobooks/"+createdBook.ID+"/metadata-history/title/undo", nil)
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var wrapper struct {
		Data map[string]any `json:"data"`
	}
	err = json.Unmarshal(w.Body.Bytes(), &wrapper)
	require.NoError(t, err)
	assert.Equal(t, "undo applied", wrapper.Data["message"])
	assert.Equal(t, "title", wrapper.Data["field"])
}

func TestUndoMetadataChange_NoHistory(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	// Create a book with no change history
	book := &database.Book{
		Title:    "Test Book",
		FilePath: "/tmp/test2.m4b",
	}
	createdBook, err := database.GetGlobalStore().CreateBook(book)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/audiobooks/"+createdBook.ID+"/metadata-history/title/undo", nil)
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)

	var resp map[string]any
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	errMsg, _ := resp["error"].(string)
	assert.Contains(t, errMsg, "not found")
}

func TestGetBookMetadataHistory_Handler(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	book := &database.Book{
		Title:    "History Book",
		FilePath: "/tmp/history.m4b",
	}
	createdBook, err := database.GetGlobalStore().CreateBook(book)
	require.NoError(t, err)

	// Record a couple of changes
	now := time.Now()
	for i, field := range []string{"title", "author_name"} {
		val := `"value` + field + `"`
		record := &database.MetadataChangeRecord{
			BookID:     createdBook.ID,
			Field:      field,
			NewValue:   &val,
			ChangeType: "fetched",
			ChangedAt:  now.Add(time.Duration(i) * time.Second),
		}
		err := database.GetGlobalStore().RecordMetadataChange(record)
		require.NoError(t, err)
	}

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/audiobooks/"+createdBook.ID+"/metadata-history", nil)
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var wrapper struct {
		Data struct {
			Items []database.MetadataChangeRecord `json:"items"`
			Count int                             `json:"count"`
		} `json:"data"`
	}
	err = json.Unmarshal(w.Body.Bytes(), &wrapper)
	require.NoError(t, err)
	resp := wrapper.Data
	assert.Equal(t, 2, resp.Count)
	assert.Len(t, resp.Items, 2)
}

func TestGetFieldMetadataHistory_Handler(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	book := &database.Book{
		Title:    "Field History Book",
		FilePath: "/tmp/field-history.m4b",
	}
	createdBook, err := database.GetGlobalStore().CreateBook(book)
	require.NoError(t, err)

	// Record changes to two different fields
	now := time.Now()
	for i, field := range []string{"title", "title", "author_name"} {
		val := `"v` + string(rune('0'+i)) + `"`
		record := &database.MetadataChangeRecord{
			BookID:     createdBook.ID,
			Field:      field,
			NewValue:   &val,
			ChangeType: "override",
			ChangedAt:  now.Add(time.Duration(i) * time.Second),
		}
		err := database.GetGlobalStore().RecordMetadataChange(record)
		require.NoError(t, err)
	}

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/audiobooks/"+createdBook.ID+"/metadata-history/title", nil)
	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var wrapper struct {
		Data struct {
			Items []database.MetadataChangeRecord `json:"items"`
			Count int                             `json:"count"`
		} `json:"data"`
	}
	err = json.Unmarshal(w.Body.Bytes(), &wrapper)
	require.NoError(t, err)
	resp := wrapper.Data
	assert.Equal(t, 2, resp.Count)
	for _, item := range resp.Items {
		assert.Equal(t, "title", item.Field)
	}
}
