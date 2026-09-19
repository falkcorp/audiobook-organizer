// file: internal/server/handlers/reading_failclosed_test.go
// version: 1.0.0
// guid: 0a6d2e4f-8b13-4c7e-95f2-d3b8a1c6e470
// last-edited: 2026-09-19

package handlers

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/gin-gonic/gin"
)

type unreadableReadingStore struct{ ReadingStore }

func (unreadableReadingStore) GetUserBookState(string, string) (*database.UserBookState, error) {
	return nil, errors.New("transient read failure")
}

// An unreadable state row is a transient failure (503), not a 500, and the
// status write it would have made is not made (readstatus fails closed).
func TestReadingStatusWrites_UnreadableStateIs503(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	h := NewReadingHandler(unreadableReadingStore{store})
	for _, tc := range []struct {
		name   string
		method string
		fn     gin.HandlerFunc
		body   string
	}{
		{"set", http.MethodPatch, h.SetBookStatus, `{"status":"finished"}`},
		{"clear", http.MethodDelete, h.ClearBookStatus, ""},
	} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(tc.method, "/api/v1/books/b1/status", bytes.NewBufferString(tc.body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Params = gin.Params{{Key: "id", Value: "b1"}}
		tc.fn(c)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status with unreadable state = %d %s, want 503", tc.name, rec.Code, rec.Body.String())
		}
	}
}
