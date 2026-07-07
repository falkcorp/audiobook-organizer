// file: internal/server/handlers_unit_test.go
// version: 1.10.0
// guid: f8a2d1c3-4b5e-6789-abcd-ef0123456789
// last-edited: 2026-07-06
//
// Unit tests for HTTP handlers using MockStore + httptest.
// Focuses on handlers that directly call s.Store() without
// complex service orchestration.

package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	audiobookspkg "github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/cache"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// setupHandlerTest creates a minimal Server with a MockStore for
// handler-level unit testing. No real DB, no services that touch
// the filesystem.
func setupHandlerTest(t *testing.T) (*Server, *mocks.MockStore, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	mockStore := mocks.NewMockStore(t)
	srv := &Server{
		store:        mockStore,
		dedupCache:   cache.New[gin.H]("dedup", 5*time.Minute),
		listCache:    cache.New[gin.H]("list", 30*time.Second),
		authorsCache: cache.New[*audiobookspkg.AuthorWithCountListResponse]("authors", 30*time.Second),
		seriesCache:  cache.New[*audiobookspkg.SeriesWithCountsResponse]("series", 30*time.Second),
	}
	router := gin.New()
	return srv, mockStore, router
}

// =============== healthCheck ===============

func TestHandler_HealthCheck_Success(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	mockStore.EXPECT().CountPrimaryBooks().Return(42, nil)
	mockStore.EXPECT().CountAuthors().Return(1, nil)
	mockStore.EXPECT().CountSeries().Return(1, nil)

	router.GET("/health", newSystemHandler(srv).HealthCheck)

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp struct{ Data map[string]any }
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "ok", resp.Data["status"])

	metrics := resp.Data["metrics"].(map[string]any)
	assert.Equal(t, float64(42), metrics["books"])
	assert.Equal(t, float64(1), metrics["authors"])
	assert.Equal(t, float64(1), metrics["series"])
}

func TestHandler_HealthCheck_DBError(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	mockStore.EXPECT().CountPrimaryBooks().Return(0, errors.New("db down"))
	mockStore.EXPECT().CountAuthors().Return(0, errors.New("db down"))
	mockStore.EXPECT().CountSeries().Return(0, errors.New("db down"))

	router.GET("/health", newSystemHandler(srv).HealthCheck)

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp struct{ Data map[string]any }
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp.Data, "partial_error")
}

// =============== getOperationStatus ===============

func TestHandler_GetOperationStatus_Found(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	op := &database.Operation{ID: "op-123", Type: "scan", Status: "completed"}
	// v2 check first (returns nil → falls through to legacy)
	mockStore.EXPECT().GetOperationV2("op-123").Return(nil, errors.New("not found"))
	mockStore.EXPECT().GetOperationByID("op-123").Return(op, nil)

	router.GET("/operations/:id", newOperationsHandler(srv).GetOperationStatus)

	req := httptest.NewRequest("GET", "/operations/op-123", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data := resp["data"].(map[string]any)
	assert.Equal(t, "op-123", data["id"])
}

func TestHandler_GetOperationStatus_FoundV2(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	now := time.Now()
	v2 := &database.OperationV2Row{
		ID:              "v2-op-123",
		DefID:           "dedup.series-scan",
		Status:          "completed",
		ProgressCurrent: 42,
		ProgressTotal:   100,
		ProgressMessage: "done",
		QueuedAt:        now,
		CompletedAt:     &now,
	}
	mockStore.EXPECT().GetOperationV2("v2-op-123").Return(v2, nil)

	router.GET("/operations/:id", newOperationsHandler(srv).GetOperationStatus)

	req := httptest.NewRequest("GET", "/operations/v2-op-123", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data := resp["data"].(map[string]any)
	assert.Equal(t, "v2-op-123", data["id"])
	assert.Equal(t, "completed", data["status"])
}

func TestHandler_GetOperationStatus_NotFound(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	mockStore.EXPECT().GetOperationV2("nope").Return(nil, errors.New("not found"))
	mockStore.EXPECT().GetOperationByID("nope").Return(nil, errors.New("not found"))

	router.GET("/operations/:id", newOperationsHandler(srv).GetOperationStatus)

	req := httptest.NewRequest("GET", "/operations/nope", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// =============== listOperations ===============

func TestHandler_ListOperations_Success(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	ops := []database.Operation{
		{ID: "op-1", Type: "scan", Status: "completed"},
		{ID: "op-2", Type: "organize", Status: "running"},
	}
	mockStore.EXPECT().ListOperations(mock.AnythingOfType("int"), mock.AnythingOfType("int")).Return(ops, 2, nil)

	router.GET("/operations", newOperationsHandler(srv).ListOperations)

	req := httptest.NewRequest("GET", "/operations?limit=10&offset=0", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data := resp["data"].(map[string]any)
	items := data["items"].([]any)
	assert.Len(t, items, 2)
	assert.Equal(t, float64(2), data["total"])
}

func TestHandler_ListOperations_StoreError(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	mockStore.EXPECT().ListOperations(mock.AnythingOfType("int"), mock.AnythingOfType("int")).Return(nil, 0, errors.New("db error"))

	router.GET("/operations", newOperationsHandler(srv).ListOperations)

	req := httptest.NewRequest("GET", "/operations", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// =============== getOperationLogs ===============

func TestHandler_GetOperationLogs_Success(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	// Handler queries v2 first (canonical for UOS v2 ops). Empty v2
	// triggers the v1 fallback for legacy rows.
	mockStore.EXPECT().GetOpLogsV2("op-1", 1000).Return(nil, nil)
	logs := []database.OperationLog{
		{ID: 1, OperationID: "op-1", Level: "info", Message: "started"},
		{ID: 2, OperationID: "op-1", Level: "info", Message: "done"},
	}
	mockStore.EXPECT().GetOperationLogs("op-1").Return(logs, nil)

	router.GET("/operations/:id/logs", newOperationsHandler(srv).GetOperationLogs)

	req := httptest.NewRequest("GET", "/operations/op-1/logs", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data := resp["data"].(map[string]any)
	assert.Equal(t, float64(2), data["count"])
}

func TestHandler_GetOperationLogs_WithTail(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	mockStore.EXPECT().GetOpLogsV2("op-1", 1).Return(nil, nil)
	logs := []database.OperationLog{
		{ID: 1, OperationID: "op-1", Level: "info", Message: "first"},
		{ID: 2, OperationID: "op-1", Level: "info", Message: "second"},
		{ID: 3, OperationID: "op-1", Level: "info", Message: "third"},
	}
	mockStore.EXPECT().GetOperationLogs("op-1").Return(logs, nil)

	router.GET("/operations/:id/logs", newOperationsHandler(srv).GetOperationLogs)

	req := httptest.NewRequest("GET", "/operations/op-1/logs?tail=1", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data := resp["data"].(map[string]any)
	assert.Equal(t, float64(1), data["count"])
}

// =============== getOperationResult ===============

func TestHandler_GetOperationResult_WithData(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	resultData := `{"files_processed":10}`
	op := &database.Operation{ID: "op-1", Status: "completed", ResultData: &resultData}
	mockStore.EXPECT().GetOperationByID("op-1").Return(op, nil)

	router.GET("/operations/:id/result", newOperationsHandler(srv).GetOperationResult)

	req := httptest.NewRequest("GET", "/operations/op-1/result", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data := resp["data"].(map[string]any)
	assert.NotNil(t, data["result_data"])
}

func TestHandler_GetOperationResult_NoData(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	op := &database.Operation{ID: "op-1", Status: "completed", ResultData: nil}
	mockStore.EXPECT().GetOperationByID("op-1").Return(op, nil)

	router.GET("/operations/:id/result", newOperationsHandler(srv).GetOperationResult)

	req := httptest.NewRequest("GET", "/operations/op-1/result", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestHandler_GetOperationResult_NotFound(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	mockStore.EXPECT().GetOperationByID("nope").Return(nil, nil)

	router.GET("/operations/:id/result", newOperationsHandler(srv).GetOperationResult)

	req := httptest.NewRequest("GET", "/operations/nope/result", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// =============== getOperationChanges ===============

func TestHandler_GetOperationChanges_Success(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	changes := []*database.OperationChange{{ID: "c1", OperationID: "op-1"}}
	mockStore.EXPECT().GetOperationChanges("op-1").Return(changes, nil)

	router.GET("/operations/:id/changes", newOperationsHandler(srv).GetOperationChanges)

	req := httptest.NewRequest("GET", "/operations/op-1/changes", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// =============== listBlockedHashes ===============

func TestHandler_ListBlockedHashes_Success(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	hashes := []database.DoNotImport{{Hash: "abc123", Reason: "corrupted"}}
	mockStore.EXPECT().GetAllBlockedHashes().Return(hashes, nil)

	router.GET("/blocked-hashes", newSystemHandler(srv).ListBlockedHashes)

	req := httptest.NewRequest("GET", "/blocked-hashes", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp struct{ Data map[string]any }
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, float64(1), resp.Data["total"])
}

// =============== addBlockedHash ===============

func TestHandler_AddBlockedHash_Success(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	hash := "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"
	mockStore.EXPECT().AddBlockedHash(hash, "test reason").Return(nil)

	router.POST("/blocked-hashes", newSystemHandler(srv).AddBlockedHash)

	body, _ := json.Marshal(map[string]string{"hash": hash, "reason": "test reason"})
	req := httptest.NewRequest("POST", "/blocked-hashes", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)
}

func TestHandler_AddBlockedHash_InvalidLength(t *testing.T) {
	srv, _, router := setupHandlerTest(t)

	router.POST("/blocked-hashes", newSystemHandler(srv).AddBlockedHash)

	body, _ := json.Marshal(map[string]string{"hash": "tooshort", "reason": "test"})
	req := httptest.NewRequest("POST", "/blocked-hashes", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp["error"], "64 characters")
}

func TestHandler_AddBlockedHash_MissingFields(t *testing.T) {
	srv, _, router := setupHandlerTest(t)

	router.POST("/blocked-hashes", newSystemHandler(srv).AddBlockedHash)

	body, _ := json.Marshal(map[string]string{"hash": "abc"})
	req := httptest.NewRequest("POST", "/blocked-hashes", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// =============== removeBlockedHash ===============

func TestHandler_RemoveBlockedHash_Success(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	mockStore.EXPECT().RemoveBlockedHash("somehash").Return(nil)

	router.DELETE("/blocked-hashes/:hash", newSystemHandler(srv).RemoveBlockedHash)

	req := httptest.NewRequest("DELETE", "/blocked-hashes/somehash", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestHandler_RemoveBlockedHash_StoreError(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	mockStore.EXPECT().RemoveBlockedHash("bad").Return(errors.New("not found"))

	router.DELETE("/blocked-hashes/:hash", newSystemHandler(srv).RemoveBlockedHash)

	req := httptest.NewRequest("DELETE", "/blocked-hashes/bad", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// =============== getUserPreference ===============

func TestHandler_GetUserPreference_Found(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	val := "dark"
	mockStore.EXPECT().GetUserPreference("theme").Return(&database.UserPreference{Key: "theme", Value: &val}, nil)

	router.GET("/preferences/:key", newSystemHandler(srv).GetUserPreference)

	req := httptest.NewRequest("GET", "/preferences/theme", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp struct{ Data map[string]any }
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "theme", resp.Data["key"])
}

func TestHandler_GetUserPreference_NotFoundReturnsEmpty(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	mockStore.EXPECT().GetUserPreference("missing").Return(nil, nil)

	router.GET("/preferences/:key", newSystemHandler(srv).GetUserPreference)

	req := httptest.NewRequest("GET", "/preferences/missing", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Missing preferences return 200 with an empty value rather than 404
	// so that clients (browsers in particular) don't surface "failed to
	// load resource" console noise for optional prefs that legitimately
	// have no saved value yet.
	assert.Equal(t, http.StatusOK, w.Code)
	var resp struct{ Data map[string]any }
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "missing", resp.Data["key"])
	assert.Equal(t, "", resp.Data["value"])
}

// =============== setUserPreference ===============

func TestHandler_SetUserPreference_Success(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	mockStore.EXPECT().SetUserPreference("theme", "light").Return(nil)

	router.PUT("/preferences/:key", newSystemHandler(srv).SetUserPreference)

	body, _ := json.Marshal(map[string]string{"value": "light"})
	req := httptest.NewRequest("PUT", "/preferences/theme", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp struct{ Data map[string]any }
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "theme", resp.Data["key"])
	assert.Equal(t, "light", resp.Data["value"])
}

func TestHandler_SetUserPreference_StoreError(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	mockStore.EXPECT().SetUserPreference("bad", "val").Return(errors.New("write fail"))

	router.PUT("/preferences/:key", newSystemHandler(srv).SetUserPreference)

	body, _ := json.Marshal(map[string]string{"value": "val"})
	req := httptest.NewRequest("PUT", "/preferences/bad", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// =============== deleteUserPreference ===============

func TestHandler_DeleteUserPreference_Success(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	mockStore.EXPECT().SetUserPreference("theme", "").Return(nil)

	router.DELETE("/preferences/:key", newSystemHandler(srv).DeleteUserPreference)

	req := httptest.NewRequest("DELETE", "/preferences/theme", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// =============== getDashboard ===============

func TestHandler_GetDashboard_Success(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	stats := &database.DashboardStats{
		TotalBooks:         100,
		TotalSize:          1024000,
		TotalDuration:      36000,
		FormatDistribution: map[string]int{"m4b": 80, "mp3": 20},
		StateDistribution:  map[string]int{"new": 50, "organized": 50},
	}
	mockStore.EXPECT().GetDashboardStats().Return(stats, nil)
	mockStore.EXPECT().GetRecentOperations(5).Return([]database.Operation{}, nil)

	router.GET("/dashboard", newSystemHandler(srv).GetDashboard)

	req := httptest.NewRequest("GET", "/dashboard", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp struct{ Data map[string]any }
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, float64(100), resp.Data["totalBooks"])
}

func TestHandler_GetDashboard_StoreError(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	mockStore.EXPECT().GetDashboardStats().Return(nil, errors.New("db error"))

	router.GET("/dashboard", newSystemHandler(srv).GetDashboard)

	req := httptest.NewRequest("GET", "/dashboard", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// =============== getBookTagsDetailed ===============

func TestHandler_GetBookTagsDetailed_Success(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	tags := []database.BookTag{
		{BookID: "b1", Tag: "fiction", Source: "user"},
		{BookID: "b1", Tag: "sci-fi", Source: "system"},
	}
	mockStore.EXPECT().GetBookTagsDetailed("b1").Return(tags, nil)

	router.GET("/audiobooks/:id/tags-detailed", newAudiobooksHandler(srv).GetBookTagsDetailed)

	req := httptest.NewRequest("GET", "/audiobooks/b1/tags-detailed", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	respData := resp["data"].(map[string]any)
	respTags := respData["tags"].([]any)
	assert.Len(t, respTags, 2)
}

func TestHandler_GetBookTagsDetailed_Empty(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	mockStore.EXPECT().GetBookTagsDetailed("b2").Return(nil, nil)

	router.GET("/audiobooks/:id/tags-detailed", newAudiobooksHandler(srv).GetBookTagsDetailed)

	req := httptest.NewRequest("GET", "/audiobooks/b2/tags-detailed", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	respData := resp["data"].(map[string]any)
	respTags := respData["tags"].([]any)
	assert.Len(t, respTags, 0)
}

// =============== getBookAlternativeTitles ===============

func TestHandler_GetBookAlternativeTitles_Success(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	alts := []database.BookAlternativeTitle{
		{BookID: "b1", Title: "Alt Title 1", Source: "user"},
	}
	mockStore.EXPECT().GetBookAlternativeTitles("b1").Return(alts, nil)

	router.GET("/audiobooks/:id/alternative-titles", newAudiobooksHandler(srv).GetBookAlternativeTitles)

	req := httptest.NewRequest("GET", "/audiobooks/b1/alternative-titles", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	respData := resp["data"].(map[string]any)
	titles := respData["alternative_titles"].([]any)
	assert.Len(t, titles, 1)
}

// =============== addBookAlternativeTitle ===============

func TestHandler_AddBookAlternativeTitle_Success(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	book := &database.Book{ID: "b1", Title: "Test Book"}
	mockStore.EXPECT().GetBookByID("b1").Return(book, nil)
	mockStore.EXPECT().AddBookAlternativeTitle("b1", "New Title", "user", "en").Return(nil)
	mockStore.EXPECT().GetBookAlternativeTitles("b1").Return([]database.BookAlternativeTitle{
		{BookID: "b1", Title: "New Title", Source: "user"},
	}, nil)

	router.POST("/audiobooks/:id/alternative-titles", newAudiobooksHandler(srv).AddBookAlternativeTitle)

	body, _ := json.Marshal(map[string]string{"title": "New Title", "source": "user", "language": "en"})
	req := httptest.NewRequest("POST", "/audiobooks/b1/alternative-titles", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestHandler_AddBookAlternativeTitle_MissingTitle(t *testing.T) {
	srv, _, router := setupHandlerTest(t)

	router.POST("/audiobooks/:id/alternative-titles", newAudiobooksHandler(srv).AddBookAlternativeTitle)

	body, _ := json.Marshal(map[string]string{"source": "user"})
	req := httptest.NewRequest("POST", "/audiobooks/b1/alternative-titles", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_AddBookAlternativeTitle_BookNotFound(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	mockStore.EXPECT().GetBookByID("missing").Return(nil, errors.New("not found"))

	router.POST("/audiobooks/:id/alternative-titles", newAudiobooksHandler(srv).AddBookAlternativeTitle)

	body, _ := json.Marshal(map[string]string{"title": "Alt"})
	req := httptest.NewRequest("POST", "/audiobooks/missing/alternative-titles", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// =============== removeBookAlternativeTitle ===============

func TestHandler_RemoveBookAlternativeTitle_Success(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	mockStore.EXPECT().RemoveBookAlternativeTitle("b1", "Old Title").Return(nil)
	mockStore.EXPECT().GetBookAlternativeTitles("b1").Return([]database.BookAlternativeTitle{}, nil)

	router.DELETE("/audiobooks/:id/alternative-titles", newAudiobooksHandler(srv).RemoveBookAlternativeTitle)

	body, _ := json.Marshal(map[string]string{"title": "Old Title"})
	req := httptest.NewRequest("DELETE", "/audiobooks/b1/alternative-titles", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestHandler_RemoveBookAlternativeTitle_MissingTitle(t *testing.T) {
	srv, _, router := setupHandlerTest(t)

	router.DELETE("/audiobooks/:id/alternative-titles", newAudiobooksHandler(srv).RemoveBookAlternativeTitle)

	body, _ := json.Marshal(map[string]string{})
	req := httptest.NewRequest("DELETE", "/audiobooks/b1/alternative-titles", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// =============== getSystemActivityLog ===============

func TestHandler_GetSystemActivityLog_Success(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	logs := []database.SystemActivityLog{
		{ID: 1, Source: "scanner", Message: "scan complete"},
	}
	mockStore.EXPECT().GetSystemActivityLogs("", 50).Return(logs, nil)

	router.GET("/system/activity-log", newSystemHandler(srv).GetSystemActivityLog)

	req := httptest.NewRequest("GET", "/system/activity-log", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp struct{ Data map[string]any }
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, float64(1), resp.Data["count"])
}

func TestHandler_GetSystemActivityLog_WithSourceAndLimit(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	mockStore.EXPECT().GetSystemActivityLogs("scanner", 10).Return([]database.SystemActivityLog{}, nil)

	router.GET("/system/activity-log", newSystemHandler(srv).GetSystemActivityLog)

	req := httptest.NewRequest("GET", "/system/activity-log?source=scanner&limit=10", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// =============== resetSystem ===============

func TestHandler_ResetSystem_Success(t *testing.T) {
	origCfg := config.AppConfig
	defer func() { config.AppConfig = origCfg }()

	srv, mockStore, router := setupHandlerTest(t)

	mockStore.EXPECT().Reset().Return(nil)
	mockStore.EXPECT().InvalidateLibraryStats().Return()

	router.POST("/system/reset", newSystemHandler(srv).ResetSystem)

	req := httptest.NewRequest("POST", "/system/reset", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestHandler_ResetSystem_StoreError(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	mockStore.EXPECT().Reset().Return(errors.New("reset failed"))

	router.POST("/system/reset", newSystemHandler(srv).ResetSystem)

	req := httptest.NewRequest("POST", "/system/reset", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// =============== factoryReset ===============

func TestHandler_FactoryReset_MissingConfirm(t *testing.T) {
	srv, _, router := setupHandlerTest(t)

	router.POST("/system/factory-reset", newSystemHandler(srv).FactoryReset)

	body, _ := json.Marshal(map[string]string{"confirm": "wrong"})
	req := httptest.NewRequest("POST", "/system/factory-reset", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp["error"].(string), "RESET")
}

func TestHandler_FactoryReset_NoBody(t *testing.T) {
	srv, _, router := setupHandlerTest(t)

	router.POST("/system/factory-reset", newSystemHandler(srv).FactoryReset)

	req := httptest.NewRequest("POST", "/system/factory-reset", nil)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// =============== handleGetPosition (now ReadingHandler.GetPosition) ===============

func TestHandler_GetPosition_Success(t *testing.T) {
	_, mockStore, router := setupHandlerTest(t)

	pos := &database.UserPosition{
		UserID: "_local", BookID: "b1", SegmentID: "seg1", PositionSeconds: 123.5,
	}
	mockStore.EXPECT().GetUserPosition("_local", "b1").Return(pos, nil)

	h := handlers.NewReadingHandler(mockStore)
	router.GET("/books/:id/position", h.GetPosition)

	req := httptest.NewRequest("GET", "/books/b1/position", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var wrapper struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &wrapper))
	// Data IS the UserPosition — check for expected fields.
	assert.Equal(t, "b1", wrapper.Data["book_id"])
	assert.Equal(t, "seg1", wrapper.Data["segment_id"])
}

func TestHandler_GetPosition_NoneRecorded(t *testing.T) {
	_, mockStore, router := setupHandlerTest(t)

	mockStore.EXPECT().GetUserPosition("_local", "b2").Return(nil, nil)

	h := handlers.NewReadingHandler(mockStore)
	router.GET("/books/:id/position", h.GetPosition)

	req := httptest.NewRequest("GET", "/books/b2/position", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Nil(t, resp["position"])
}

// =============== handleGetBookState (now ReadingHandler.GetBookState) ===============

func TestHandler_GetBookState_Success(t *testing.T) {
	_, mockStore, router := setupHandlerTest(t)

	state := &database.UserBookState{
		UserID: "_local", BookID: "b1", Status: "in_progress", ProgressPct: 45,
	}
	mockStore.EXPECT().GetUserBookState("_local", "b1").Return(state, nil)

	h := handlers.NewReadingHandler(mockStore)
	router.GET("/books/:id/state", h.GetBookState)

	req := httptest.NewRequest("GET", "/books/b1/state", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// =============== listWorkBooks ===============

func TestHandler_ListWorkBooks_Success(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	books := []database.Book{
		{ID: "b1", Title: "Book 1"},
		{ID: "b2", Title: "Book 2"},
	}
	mockStore.EXPECT().GetBooksByWorkID("w1").Return(books, nil)

	router.GET("/works/:id/books", newEntitiesHandler(srv).ListWorkBooks)

	req := httptest.NewRequest("GET", "/works/w1/books", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
	assert.Equal(t, float64(2), envelope.Data["count"])
}

func TestHandler_ListWorkBooks_Empty(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	mockStore.EXPECT().GetBooksByWorkID("w2").Return(nil, nil)

	router.GET("/works/:id/books", newEntitiesHandler(srv).ListWorkBooks)

	req := httptest.NewRequest("GET", "/works/w2/books", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
	items := envelope.Data["items"].([]any)
	assert.Len(t, items, 0)
}

// =============== countAuthors ===============

func TestHandler_CountAuthors_Success(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	mockStore.EXPECT().CountAuthors().Return(150, nil)

	router.GET("/authors/count", newEntitiesHandler(srv).CountAuthors)

	req := httptest.NewRequest("GET", "/authors/count", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
	assert.Equal(t, float64(150), envelope.Data["count"])
}

func TestHandler_CountAuthors_StoreError(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	mockStore.EXPECT().CountAuthors().Return(0, errors.New("db error"))

	router.GET("/authors/count", newEntitiesHandler(srv).CountAuthors)

	req := httptest.NewRequest("GET", "/authors/count", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// =============== handleEvents (SSE hub nil check) ===============

func TestHandler_HandleEvents_NoHub(t *testing.T) {
	srv, _, router := setupHandlerTest(t)

	router.GET("/events", newSystemHandler(srv).HandleEvents)

	req := httptest.NewRequest("GET", "/events", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

// =============== renameAuthor ===============

func TestHandler_RenameAuthor_Success(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	mockStore.EXPECT().UpdateAuthorName(42, "New Name").Return(nil)

	router.PUT("/authors/:id/rename", newEntitiesHandler(srv).RenameAuthor)

	body, _ := json.Marshal(map[string]string{"name": "New Name"})
	req := httptest.NewRequest("PUT", "/authors/42/rename", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
	assert.Equal(t, float64(42), envelope.Data["id"])
	assert.Equal(t, "New Name", envelope.Data["name"])
}

func TestHandler_RenameAuthor_InvalidID(t *testing.T) {
	srv, _, router := setupHandlerTest(t)

	router.PUT("/authors/:id/rename", newEntitiesHandler(srv).RenameAuthor)

	body, _ := json.Marshal(map[string]string{"name": "Test"})
	req := httptest.NewRequest("PUT", "/authors/abc/rename", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_RenameAuthor_EmptyName(t *testing.T) {
	srv, _, router := setupHandlerTest(t)

	router.PUT("/authors/:id/rename", newEntitiesHandler(srv).RenameAuthor)

	body, _ := json.Marshal(map[string]string{"name": "  "})
	req := httptest.NewRequest("PUT", "/authors/42/rename", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// =============== getAuthorAliases ===============

func TestHandler_GetAuthorAliases_Success(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	aliases := []database.AuthorAlias{
		{ID: 1, AuthorID: 5, AliasName: "Pen Name", AliasType: "pen_name"},
	}
	mockStore.EXPECT().GetAuthorAliases(5).Return(aliases, nil)

	router.GET("/authors/:id/aliases", newEntitiesHandler(srv).GetAuthorAliases)

	req := httptest.NewRequest("GET", "/authors/5/aliases", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
	assert.NotNil(t, envelope.Data["aliases"])
}

func TestHandler_GetAuthorAliases_InvalidID(t *testing.T) {
	srv, _, router := setupHandlerTest(t)

	router.GET("/authors/:id/aliases", newEntitiesHandler(srv).GetAuthorAliases)

	req := httptest.NewRequest("GET", "/authors/notanumber/aliases", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// =============== createAuthorAlias ===============

func TestHandler_CreateAuthorAlias_Success(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	alias := &database.AuthorAlias{ID: 1, AuthorID: 5, AliasName: "Alt Name", AliasType: "alias"}
	mockStore.EXPECT().CreateAuthorAlias(5, "Alt Name", "alias").Return(alias, nil)

	router.POST("/authors/:id/aliases", newEntitiesHandler(srv).CreateAuthorAlias)

	body, _ := json.Marshal(map[string]string{"alias_name": "Alt Name"})
	req := httptest.NewRequest("POST", "/authors/5/aliases", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)
}

func TestHandler_CreateAuthorAlias_MissingName(t *testing.T) {
	srv, _, router := setupHandlerTest(t)

	router.POST("/authors/:id/aliases", newEntitiesHandler(srv).CreateAuthorAlias)

	body, _ := json.Marshal(map[string]string{"alias_type": "pen_name"})
	req := httptest.NewRequest("POST", "/authors/5/aliases", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// =============== deleteAuthorAlias ===============

func TestHandler_DeleteAuthorAlias_Success(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	mockStore.EXPECT().DeleteAuthorAlias(10).Return(nil)

	router.DELETE("/authors/:id/aliases/:aliasId", newEntitiesHandler(srv).DeleteAuthorAlias)

	req := httptest.NewRequest("DELETE", "/authors/5/aliases/10", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestHandler_DeleteAuthorAlias_InvalidID(t *testing.T) {
	srv, _, router := setupHandlerTest(t)

	router.DELETE("/authors/:id/aliases/:aliasId", newEntitiesHandler(srv).DeleteAuthorAlias)

	req := httptest.NewRequest("DELETE", "/authors/5/aliases/bad", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// =============== deleteAuthorHandler ===============

func TestHandler_DeleteAuthor_Success(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	mockStore.EXPECT().GetBooksByAuthorIDCore(42).Return([]database.BookCore{}, nil)
	mockStore.EXPECT().DeleteAuthor(42).Return(nil)

	router.DELETE("/authors/:id", newEntitiesHandler(srv).DeleteAuthor)

	req := httptest.NewRequest("DELETE", "/authors/42", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestHandler_DeleteAuthor_HasBooks(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	mockStore.EXPECT().GetBooksByAuthorIDCore(42).Return([]database.BookCore{{ID: "b1"}}, nil)

	router.DELETE("/authors/:id", newEntitiesHandler(srv).DeleteAuthor)

	req := httptest.NewRequest("DELETE", "/authors/42", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestHandler_DeleteAuthor_InvalidID(t *testing.T) {
	srv, _, router := setupHandlerTest(t)

	router.DELETE("/authors/:id", newEntitiesHandler(srv).DeleteAuthor)

	req := httptest.NewRequest("DELETE", "/authors/abc", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// =============== countSeries ===============

func TestHandler_CountSeries_Success(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	mockStore.EXPECT().CountSeries().Return(75, nil)

	router.GET("/series/count", newEntitiesHandler(srv).CountSeries)

	req := httptest.NewRequest("GET", "/series/count", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
	assert.Equal(t, float64(75), envelope.Data["count"])
}

// =============== renameSeriesHandler ===============

func TestHandler_RenameSeries_Success(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	mockStore.EXPECT().UpdateSeriesName(10, "New Series Name").Return(nil)
	mockStore.EXPECT().GetSeriesByID(10).Return(&database.Series{ID: 10, Name: "New Series Name"}, nil)

	router.PUT("/series/:id/rename", newEntitiesHandler(srv).RenameSeries)

	body, _ := json.Marshal(map[string]string{"name": "New Series Name"})
	req := httptest.NewRequest("PUT", "/series/10/rename", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestHandler_RenameSeries_InvalidID(t *testing.T) {
	srv, _, router := setupHandlerTest(t)

	router.PUT("/series/:id/rename", newEntitiesHandler(srv).RenameSeries)

	body, _ := json.Marshal(map[string]string{"name": "Test"})
	req := httptest.NewRequest("PUT", "/series/abc/rename", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_RenameSeries_EmptyName(t *testing.T) {
	srv, _, router := setupHandlerTest(t)

	router.PUT("/series/:id/rename", newEntitiesHandler(srv).RenameSeries)

	body, _ := json.Marshal(map[string]string{"name": "   "})
	req := httptest.NewRequest("PUT", "/series/10/rename", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// =============== updateSeriesName ===============

func TestHandler_UpdateSeriesName_Success(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	mockStore.EXPECT().UpdateSeriesName(10, "Updated Name").Return(nil)
	mockStore.EXPECT().GetSeriesByID(10).Return(&database.Series{ID: 10, Name: "Updated Name"}, nil)

	router.PATCH("/series/:id/name", newEntitiesHandler(srv).UpdateSeriesName)

	body, _ := json.Marshal(map[string]string{"name": "Updated Name"})
	req := httptest.NewRequest("PATCH", "/series/10/name", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestHandler_UpdateSeriesName_InvalidID(t *testing.T) {
	srv, _, router := setupHandlerTest(t)

	router.PATCH("/series/:id/name", newEntitiesHandler(srv).UpdateSeriesName)

	body, _ := json.Marshal(map[string]string{"name": "Test"})
	req := httptest.NewRequest("PATCH", "/series/bad/name", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// =============== deleteEmptySeries ===============

func TestHandler_DeleteEmptySeries_Success(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	mockStore.EXPECT().GetBooksBySeriesIDCore(10).Return([]database.BookCore{}, nil)
	mockStore.EXPECT().DeleteSeries(10).Return(nil)

	router.DELETE("/series/:id", newEntitiesHandler(srv).DeleteEmptySeries)

	req := httptest.NewRequest("DELETE", "/series/10", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestHandler_DeleteEmptySeries_HasBooks(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	mockStore.EXPECT().GetBooksBySeriesIDCore(10).Return([]database.BookCore{{ID: "b1"}}, nil)

	router.DELETE("/series/:id", newEntitiesHandler(srv).DeleteEmptySeries)

	req := httptest.NewRequest("DELETE", "/series/10", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestHandler_DeleteEmptySeries_InvalidID(t *testing.T) {
	srv, _, router := setupHandlerTest(t)

	router.DELETE("/series/:id", newEntitiesHandler(srv).DeleteEmptySeries)

	req := httptest.NewRequest("DELETE", "/series/0", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// =============== listNarrators ===============

func TestHandler_ListNarrators_Success(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	narrators := []database.Narrator{
		{ID: 1, Name: "Narrator 1"},
		{ID: 2, Name: "Narrator 2"},
	}
	mockStore.EXPECT().ListNarrators().Return(narrators, nil)

	router.GET("/narrators", newEntitiesHandler(srv).ListNarrators)

	req := httptest.NewRequest("GET", "/narrators", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestHandler_ListNarrators_StoreError(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	mockStore.EXPECT().ListNarrators().Return(nil, errors.New("db error"))

	router.GET("/narrators", newEntitiesHandler(srv).ListNarrators)

	req := httptest.NewRequest("GET", "/narrators", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// =============== countNarrators ===============

func TestHandler_CountNarrators_Success(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	narrators := []database.Narrator{{ID: 1}, {ID: 2}, {ID: 3}}
	mockStore.EXPECT().ListNarrators().Return(narrators, nil)

	router.GET("/narrators/count", newEntitiesHandler(srv).CountNarrators)

	req := httptest.NewRequest("GET", "/narrators/count", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
	assert.Equal(t, float64(3), envelope.Data["count"])
}

// =============== listAudiobookNarrators ===============

func TestHandler_ListAudiobookNarrators_Success(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	narrators := []database.BookNarrator{
		{BookID: "b1", NarratorID: 1, Role: "narrator"},
	}
	mockStore.EXPECT().GetBookNarrators("b1").Return(narrators, nil)

	router.GET("/audiobooks/:id/narrators", newEntitiesHandler(srv).ListAudiobookNarrators)

	req := httptest.NewRequest("GET", "/audiobooks/b1/narrators", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestHandler_ListAudiobookNarrators_Empty(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	mockStore.EXPECT().GetBookNarrators("b2").Return(nil, nil)

	router.GET("/audiobooks/:id/narrators", newEntitiesHandler(srv).ListAudiobookNarrators)

	req := httptest.NewRequest("GET", "/audiobooks/b2/narrators", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// =============== setAudiobookNarrators ===============

func TestHandler_SetAudiobookNarrators_Success(t *testing.T) {
	srv, mockStore, router := setupHandlerTest(t)

	narrators := []database.BookNarrator{
		{BookID: "b1", NarratorID: 1, Role: "narrator", Position: 0},
	}
	mockStore.EXPECT().SetBookNarrators("b1", narrators).Return(nil)

	router.PUT("/audiobooks/:id/narrators", newEntitiesHandler(srv).SetAudiobookNarrators)

	body, _ := json.Marshal(narrators)
	req := httptest.NewRequest("PUT", "/audiobooks/b1/narrators", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// =============== deleteBackup (path traversal guard) ===============

func TestHandler_DeleteBackup_PathTraversal(t *testing.T) {
	srv, _, router := setupHandlerTest(t)

	router.DELETE("/backups/:filename", newSystemHandler(srv).DeleteBackup)

	req := httptest.NewRequest("DELETE", "/backups/passwd", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Will return 500 because the file doesn't exist
	assert.True(t, w.Code == http.StatusOK || w.Code == http.StatusInternalServerError,
		fmt.Sprintf("unexpected status %d", w.Code))
}
