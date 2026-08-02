// file: internal/server/metadata_handlers_test.go
// version: 1.0.2
// guid: 7a3e2f1b-9c4d-4e8a-b6f0-1d5c2a0e3b7f
// last-edited: 2026-05-03

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

func setupRatingTestServer(t *testing.T) *Server {
	t.Helper()
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
	return srv
}

func TestHandleUpdateBookRating_SetRatings(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := setupRatingTestServer(t)

	store := database.GetGlobalStore()
	bookID := "test-book-rating-1"
	_, err := store.CreateBook(&database.Book{
		ID: bookID, Title: "Test Book", FilePath: "/tmp/" + bookID, Format: "m4b",
	})
	if err != nil {
		t.Fatalf("create book: %v", err)
	}

	body := `{"overall": 4.5, "story": 4.0, "performance": 5.0, "notes": "Great!"}`
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/audiobooks/"+bookID+"/rating", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var envelope struct {
		Data database.Book `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	result := envelope.Data
	if result.ID != bookID {
		t.Errorf("expected book ID %s, got %s", bookID, result.ID)
	}
	if result.UserRatingOverall == nil || *result.UserRatingOverall != 4.5 {
		t.Errorf("expected overall=4.5, got %v", result.UserRatingOverall)
	}
	if result.UserRatingStory == nil || *result.UserRatingStory != 4.0 {
		t.Errorf("expected story=4.0, got %v", result.UserRatingStory)
	}
	if result.UserRatingPerformance == nil || *result.UserRatingPerformance != 5.0 {
		t.Errorf("expected performance=5.0, got %v", result.UserRatingPerformance)
	}
	if result.UserRatingNotes == nil || *result.UserRatingNotes != "Great!" {
		t.Errorf("expected notes='Great!', got %v", result.UserRatingNotes)
	}
}

func TestHandleUpdateBookRating_InvalidValue(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := setupRatingTestServer(t)

	body := `{"overall": 6.0}`
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/audiobooks/anything/rating", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("overall")) {
		t.Errorf("expected 'overall' in error response, got: %s", w.Body.String())
	}
}
