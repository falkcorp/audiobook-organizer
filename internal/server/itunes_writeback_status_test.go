// file: internal/server/itunes_writeback_status_test.go
// version: 1.0.0
// guid: 7e4c1a92-5d8b-4f36-9a20-c3b6e8d1f457
// last-edited: 2026-10-07

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	"github.com/gin-gonic/gin"
)

func TestITunesWritebackStatusHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// No batcher: 503, not a panic.
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/itunes/writeback/status", nil)
	(&Server{}).itunesWritebackStatusHandler(c)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("no batcher: status %d, want 503", w.Code)
	}

	b := itunesservice.NewWriteBackBatcher(time.Hour, itunesservice.WriteBackBatcherConfig{AutoWriteBack: true}, nil)
	t.Cleanup(func() { _ = b.Stop(t.Context()) })
	b.Enqueue("book-1")
	s := &Server{writeBackBatcher: b}

	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/itunes/writeback/status", nil)
	s.itunesWritebackStatusHandler(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Data itunesservice.WriteBackQueueStatus `json:"data"`
	}
	var flat itunesservice.WriteBackQueueStatus
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	_ = json.Unmarshal(w.Body.Bytes(), &flat)
	if body.Data.PendingUpdates != 1 && flat.PendingUpdates != 1 {
		t.Fatalf("pending_updates not reported: %s", w.Body.String())
	}

	// Release with a bad limit is rejected.
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/itunes/writeback/held/release?limit=x", nil)
	s.itunesWritebackReleaseHeldHandler(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad limit: status %d, want 400", w.Code)
	}
}
