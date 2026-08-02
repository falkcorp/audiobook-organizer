// file: internal/server/reading_handlers_test.go
// version: 1.1.1
// guid: 4f9a2c1d-5b8e-4f70-a7d6-2e8c0f1b9a57

package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/gin-gonic/gin"
)

func setupReadingTestServer(t *testing.T) *Server {
	t.Helper()
	// Spec 3.6 features live on PebbleDB (SQLite has no-op stubs),
	// so override the default SQLite test setup with a PebbleStore.
	pebblePath := t.TempDir() + "/pebble"
	store, err := database.NewPebbleStore(pebblePath)
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	origStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	t.Cleanup(func() {
		database.SetGlobalStore(origStore)
		store.Close()
	})

	srv := NewServer(store)

	_, err = store.CreateBook(&database.Book{
		ID: "b1", Title: "Test Book", FilePath: "/tmp/b1", Format: "m4b",
	})
	if err != nil {
		t.Fatalf("create book: %v", err)
	}
	for i, segID := range []string{"s1", "s2", "s3"} {
		_ = store.CreateBookFile(&database.BookFile{
			ID: segID, BookID: "b1", FilePath: "/tmp/" + segID,
			TrackNumber: i + 1, Duration: 600,
		})
	}
	return srv
}

func TestReading_SetAndGetPosition(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := setupReadingTestServer(t)

	// POST position
	body, _ := json.Marshal(map[string]interface{}{
		"segment_id": "s1", "position_seconds": 300,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/books/b1/position", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("POST position: %d %s", w.Code, w.Body.String())
	}

	// GET position
	req = httptest.NewRequest(http.MethodGet, "/api/v1/books/b1/position", nil)
	w = httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET position: %d", w.Code)
	}
	var resp struct {
		Data *database.UserPosition `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data == nil || resp.Data.SegmentID != "s1" {
		t.Errorf("got position %+v", resp.Data)
	}
}

func TestReading_StateComputed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := setupReadingTestServer(t)

	// Listen to 300s of a 1800s book → 16% in_progress.
	body, _ := json.Marshal(map[string]interface{}{
		"segment_id": "s1", "position_seconds": 300,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/books/b1/position", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	req = httptest.NewRequest(http.MethodGet, "/api/v1/books/b1/state", nil)
	w = httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET state: %d", w.Code)
	}
	var resp struct {
		Data *database.UserBookState `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data == nil {
		t.Fatal("state nil")
	}
	if resp.Data.Status != database.UserBookStatusInProgress {
		t.Errorf("Status = %q, want in_progress", resp.Data.Status)
	}
	if resp.Data.ProgressPct != 16 {
		t.Errorf("ProgressPct = %d, want 16", resp.Data.ProgressPct)
	}
}

func TestReading_PatchStatusOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := setupReadingTestServer(t)

	// Manually mark abandoned.
	body, _ := json.Marshal(map[string]string{"status": database.UserBookStatusAbandoned})
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/books/b1/status", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH status: %d %s", w.Code, w.Body.String())
	}

	// Verify persisted + manual.
	state, _ := database.GetGlobalStore().GetUserBookState("_local", "b1")
	if state == nil || state.Status != database.UserBookStatusAbandoned || !state.StatusManual {
		t.Errorf("state after PATCH: %+v", state)
	}

	// DELETE clears manual.
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/books/b1/status", nil)
	w = httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE status: %d %s", w.Code, w.Body.String())
	}
	state, _ = database.GetGlobalStore().GetUserBookState("_local", "b1")
	if state != nil && state.StatusManual {
		t.Errorf("StatusManual should be false after clear, got %+v", state)
	}
}

func TestReading_PatchInvalidStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := setupReadingTestServer(t)

	body, _ := json.Marshal(map[string]string{"status": "bogus"})
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/books/b1/status", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 on invalid status, got %d", w.Code)
	}
}

func TestReading_ListByStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := setupReadingTestServer(t)

	// Mark b1 finished.
	body, _ := json.Marshal(map[string]string{"status": database.UserBookStatusFinished})
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/books/b1/status", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	srv.router.ServeHTTP(httptest.NewRecorder(), req)

	// GET /me/finished
	req = httptest.NewRequest(http.MethodGet, "/api/v1/me/finished", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET me/finished: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			States []database.UserBookState `json:"states"`
			Count  int                      `json:"count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.Count != 1 {
		t.Errorf("finished count = %d, want 1", resp.Data.Count)
	}
}
